// Package scheduler provides task scheduling and state management.
package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"symphonia/internal/agent"
	"symphonia/internal/agent/factory"
	"symphonia/internal/config"
	"symphonia/internal/logging"
	"symphonia/internal/store"
	"symphonia/internal/workspace"
)

// processExists checks if a process with the given PID is running and not a zombie
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	
	if runtime.GOOS == "windows" {
		// Windows: use tasklist
		cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid))
		return cmd.Run() == nil
	}
	
	// Unix-like systems: use ps to check process state
	if runtime.GOOS == "darwin" {
		// macOS: use ps -o state
		cmd := exec.Command("ps", "-o", "state=", "-p", fmt.Sprintf("%d", pid))
		output, err := cmd.Output()
		if err != nil {
			return false // Process not found
		}
		state := strings.TrimSpace(string(output))
		// Check for zombie (Z) or empty state
		return state != "" && state != "Z"
	}
	
	// Linux: use /proc/<pid>/stat to check process state
	if statBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		// Format: pid (comm) state ...
		fields := strings.Fields(string(statBytes))
		if len(fields) >= 3 {
			state := fields[2]
			// Z = zombie, D = uninterruptible sleep (disk wait)
			return state != "Z"
		}
		return false
	}
	
	// Fallback: use kill(pid, 0) to check existence
	err := syscall.Kill(pid, 0)
	if err == syscall.ESRCH {
		return false // No such process
	}
	// EPERM means process exists but we don't have permission, assume alive
	return true
}

// Scheduler manages task execution and state transitions.
type Scheduler struct {
	db        *sql.DB
	cfg       *config.Config
	workspace *workspace.Manager

	mu      sync.RWMutex
	running map[string]*RunningTask
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// RunningTask represents a currently executing task.
type RunningTask struct {
	Task        *store.Task
	Agent       agent.Agent
	CancelFunc  context.CancelFunc
	StartTime   time.Time
}

// SchedulerConfig holds scheduler-specific configuration.
type SchedulerConfig struct {
	PollInterval    time.Duration
	MaxConcurrent   int
	MaxRetries      int
	RetryBaseDelay  time.Duration
	RetryMaxDelay   time.Duration
}

// NewScheduler creates a new scheduler instance.
func NewScheduler(db *sql.DB, cfg *config.Config, wm *workspace.Manager) *Scheduler {
	return &Scheduler{
		db:        db,
		cfg:       cfg,
		workspace: wm,
		running:   make(map[string]*RunningTask),
	}
}

// Run starts the scheduler loop.
func (s *Scheduler) Run(ctx context.Context) error {
	s.stopCh = make(chan struct{})

	pollInterval := time.Duration(s.cfg.Polling.IntervalMs) * time.Millisecond
	logging.Info("Scheduler starting", logging.Duration("poll_interval", pollInterval))

	// Initial reconcile
	s.reconcile(ctx)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.reconcile(ctx)
			}
		}
	}()

	return nil
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	if s.stopCh != nil {
		close(s.stopCh)
	}
	s.wg.Wait()

	// Stop all running agents
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rt := range s.running {
		if rt.CancelFunc != nil {
			rt.CancelFunc()
		}
		if rt.Agent != nil {
			rt.Agent.Stop()
		}
	}
}

// reconcile performs one full reconciliation cycle.
func (s *Scheduler) reconcile(ctx context.Context) {
	// Process retry queue first
	s.processRetries(ctx)

	// Handle tasks by state
	tasks, err := store.ListTasks(s.db)
	if err != nil {
		logging.ErrLog("Failed to list tasks", logging.Err(err))
		return
	}

	for i := range tasks {
		task := &tasks[i]
		switch task.State {
		case store.StateTodo:
			s.handleTodoTask(ctx, task)
		case store.StateRunning:
			s.handleRunningTask(ctx, task)
		case store.StateSelfReview:
			s.handleSelfReviewTask(ctx, task)
		case store.StateAddressComment:
			s.handleAddressCommentTask(ctx, task)
		case store.StateFailed:
			// Already handled by processRetries
		case store.StateDone:
			s.handleDoneTask(ctx, task)
		case store.StateArchive:
			s.handleArchiveTask(ctx, task)
		}
	}
}

// handleTodoTask attempts to start a todo task if capacity allows.
func (s *Scheduler) handleTodoTask(ctx context.Context, task *store.Task) {
	s.mu.RLock()
	runningCount := len(s.running)
	s.mu.RUnlock()

	maxConcurrent := s.cfg.Agent.MaxConcurrentAgents
	if runningCount >= maxConcurrent {
		logging.Debug("Max concurrent agents reached, skipping task",
			logging.String("task_id", task.ID),
			logging.Int("running", runningCount),
			logging.Int("max", maxConcurrent))
		return
	}

	s.startTask(ctx, task)
}

// handleRunningTask checks on a running task's health.
func (s *Scheduler) handleRunningTask(ctx context.Context, task *store.Task) {
	s.mu.RLock()
	rt, exists := s.running[task.ID]
	s.mu.RUnlock()

	if !exists {
		// Check if the agent process is actually running
		if task.AgentPID > 0 {
			// Verify if the process exists
			if processExists(task.AgentPID) {
				// Process is running but not in our map
				// This could be:
				// 1. Server restart (shouldn't happen with current code)
				// 2. User manually dragged task to running (most likely)
				// Either way, we can't re-connect to the existing process
				// Kill it and mark as failed so user can retry properly

				logging.Warn("Found running agent process not in scheduler map (possibly manually dragged to running). Killing process and marking as failed",
					logging.String("task_id", task.ID),
					logging.Int("pid", task.AgentPID))

				// Kill the orphaned process
				syscall.Kill(-task.AgentPID, syscall.SIGTERM)
				store.SetTaskPID(s.db, task.ID, 0)

				// Mark as failed with clear message
				store.UpdateTaskState(s.db, task.ID, store.StateFailed,
					"Task was manually moved to running. Please use the retry button to start the task properly.")
				return
			}
		}
		
		// Task is marked running but not in our map and process is dead - orphaned state
		logging.Warn("Found orphaned running task, marking as failed",
			logging.String("task_id", task.ID),
			logging.Int("agent_pid", task.AgentPID))

		// Kill the orphaned agent process if it's still running
		if task.AgentPID > 0 && processExists(task.AgentPID) {
			logging.Warn("Killing orphaned agent process",
				logging.String("task_id", task.ID),
				logging.Int("pid", task.AgentPID))
			syscall.Kill(-task.AgentPID, syscall.SIGTERM)
			store.SetTaskPID(s.db, task.ID, 0)
		}

		store.UpdateTaskState(s.db, task.ID, store.StateFailed, "Orphaned task: not found in scheduler")
		return
	}

	// Check if agent is still alive (basic check via PID if available)
	// The agent runner will update the state when it completes
	logging.Debug("Running task health check",
		logging.String("task_id", task.ID),
		logging.Duration("elapsed", time.Since(rt.StartTime)))
}

// handleFailedTask handles a failed task (retry logic).
func (s *Scheduler) handleFailedTask(ctx context.Context, task *store.Task) {
	// Retry logic is handled in processRetries
}

// handleDoneTask checks if PR is merged and archives the task.
func (s *Scheduler) handleDoneTask(ctx context.Context, task *store.Task) {
	// Skip if no workspace (already archived or never had one)
	if task.Workspace == "" {
		return
	}

	// Check if workspace directory still exists
	if _, err := os.Stat(task.Workspace); os.IsNotExist(err) {
		// Workspace was cleaned up, but we still need to check PR status
		logging.Info("Workspace no longer exists, but will check PR status",
			logging.String("task_id", task.ID),
			logging.String("workspace", task.Workspace))
		// Clear the workspace field since it's gone
		store.SetTaskWorkspace(s.db, task.ID, "")
	}

	// Fetch and save PR info if not already saved
	if task.PRNumber == 0 {
		prNumber, prURL, err := s.getPRInfo(task)
		if err != nil {
			logging.Debug("Failed to get PR info",
				logging.String("task_id", task.ID),
				logging.Err(err))
		} else if prNumber > 0 {
			logging.Info("Saving PR info",
				logging.String("task_id", task.ID),
				logging.Int("pr_number", prNumber))
			if err := store.SetTaskPR(s.db, task.ID, prNumber, prURL); err != nil {
				logging.ErrLog("Failed to save PR info",
					logging.String("task_id", task.ID),
					logging.Err(err))
			}
			task.PRNumber = prNumber
			task.PRURL = prURL
			task.PRState = "open"
		}
	}

	// Check if PR has been merged or closed
	closed, reason, err := s.checkPRClosed(task)
	if err != nil {
		logging.Debug("Failed to check PR status",
			logging.String("task_id", task.ID),
			logging.Err(err))
		return
	}

	if closed {
		logging.Info("PR "+reason+", moving task to archive",
			logging.String("task_id", task.ID))
		// Transition to archive state
		store.UpdateTaskState(s.db, task.ID, store.StateArchive, reason)
		return
	}

	// Check if there are new human comments on the PR
	hasNewComments, err := s.checkForNewComments(task)
	if err != nil {
		logging.Debug("Failed to check PR comments",
			logging.String("task_id", task.ID),
			logging.Err(err))
		return
	}

	if hasNewComments {
		logging.Info("New comments detected on PR, moving to address-comment state",
			logging.String("task_id", task.ID))
		// Transition to address-comment state to handle the feedback
		store.UpdateTaskState(s.db, task.ID, store.StateAddressComment, "New comments on PR")
	}
}

// handleSelfReviewTask handles tasks in self-review state.
func (s *Scheduler) handleSelfReviewTask(ctx context.Context, task *store.Task) {
	// Check if already running
	s.mu.RLock()
	_, exists := s.running[task.ID]
	s.mu.RUnlock()

	if exists {
		logging.Debug("Self-review task already running",
			logging.String("task_id", task.ID))
		return
	}

	// Start self-review agent
	logging.Info("Starting self-review for task",
		logging.String("task_id", task.ID))

	s.startTask(ctx, task)
}

// handleAddressCommentTask handles tasks in address-comment state.
func (s *Scheduler) handleAddressCommentTask(ctx context.Context, task *store.Task) {
	// Check if already running
	s.mu.RLock()
	_, exists := s.running[task.ID]
	s.mu.RUnlock()

	if exists {
		logging.Debug("Address-comment task already running",
			logging.String("task_id", task.ID))
		return
	}

	// Start address-comment agent
	logging.Info("Starting address-comment for task",
		logging.String("task_id", task.ID))

	s.startTask(ctx, task)
}

// handleArchiveTask cleans up workspace for archived tasks.
func (s *Scheduler) handleArchiveTask(ctx context.Context, task *store.Task) {
	// Skip if already cleaned up
	if task.Workspace == "" {
		return
	}

	// Remove workspace directory
	if err := s.workspace.Remove(task.Workspace); err != nil {
		logging.ErrLog("Failed to cleanup workspace",
			logging.String("task_id", task.ID),
			logging.String("workspace", task.Workspace),
			logging.Err(err))
		return
	}

	logging.Info("Workspace cleaned up",
		logging.String("task_id", task.ID),
		logging.String("workspace", task.Workspace))

	// Clear workspace field in database (logical cleanup)
	if err := store.SetTaskWorkspace(s.db, task.ID, ""); err != nil {
		logging.ErrLog("Failed to clear workspace field",
			logging.String("task_id", task.ID),
			logging.Err(err))
	}
}

// checkPRClosed checks if the PR for a task has been merged or closed.
func (s *Scheduler) checkPRClosed(task *store.Task) (bool, string, error) {
	// Run gh pr view to check PR status (both merged and closed)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use -R flag to specify repo, so we don't need to be in a specific directory
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(task.PRNumber),
		"-R", "tiancaiamao/ai", // TODO: make this configurable
		"--json", "state,mergedAt,closedAt",
		"--jq", "{state: .state, mergedAt: .mergedAt, closedAt: .closedAt}")

	// Set working directory to workspace if it exists
	if task.Workspace != "" {
		if _, err := os.Stat(task.Workspace); err == nil {
			cmd.Dir = task.Workspace
		}
	}

	output, err := cmd.Output()
	if err != nil {
		// If no PR exists, return false
		if strings.Contains(err.Error(), "no pull requests") || strings.Contains(err.Error(), "not found") {
			return false, "", nil
		}
		return false, "", fmt.Errorf("gh pr view: %w", err)
	}

	// Parse JSON response
	var result struct {
		State    string `json:"state"`
		MergedAt string `json:"mergedAt"`
		ClosedAt string `json:"closedAt"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return false, "", fmt.Errorf("parse PR status: %w", err)
	}

	// Check if PR is merged or closed
	if result.State == "MERGED" || result.MergedAt != "" && result.MergedAt != "null" {
		return true, "PR merged", nil
	}

	if result.State == "CLOSED" || result.ClosedAt != "" && result.ClosedAt != "null" {
		return true, "PR closed", nil
	}

	return false, "", nil
}

// checkForNewComments checks if there are new comments on the PR since the task completed.
func (s *Scheduler) checkForNewComments(task *store.Task) (bool, error) {
	if task.PRNumber == 0 {
		return false, nil
	}

	// Run gh pr view to get comments
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use -R flag to specify repo, so we don't need to be in a specific directory
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(task.PRNumber),
		"-R", "tiancaiamao/ai", // TODO: make this configurable
		"--json", "comments",
		"--jq", ".comments[] | [.createdAt, .author.login, .body] | @json")

	// Set working directory to workspace if it exists
	if task.Workspace != "" {
		if _, err := os.Stat(task.Workspace); err == nil {
			cmd.Dir = task.Workspace
		}
	}

	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("gh pr view comments: %w", err)
	}

	// Parse the output - each line is a JSON array: [createdAt, authorLogin, body]
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	// Get the completion time as a reference point
	var completionTime time.Time
	if task.CompletedAt != nil && !task.CompletedAt.IsZero() {
		completionTime = *task.CompletedAt
	} else {
		// If no completion time, use updated_at
		completionTime = task.UpdatedAt
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "null" {
			continue
		}

		// Parse JSON array
		var commentData []json.RawMessage
		if err := json.Unmarshal([]byte(line), &commentData); err != nil {
			continue
		}

		if len(commentData) < 3 {
			continue
		}

		// Extract timestamp
		var createdAtStr string
		if err := json.Unmarshal(commentData[0], &createdAtStr); err != nil {
			continue
		}

		// Parse timestamp
		createdAt, err := time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			continue
		}

		// Skip comments created before or at completion time
		if !createdAt.After(completionTime) {
			continue
		}

		// Extract author to filter out bot comments
		var authorLogin string
		if err := json.Unmarshal(commentData[1], &authorLogin); err != nil {
			continue
		}

		// Skip bot comments (common bot names)
		isBot := strings.Contains(authorLogin, "bot") ||
			strings.Contains(authorLogin, "Bot") ||
			authorLogin == "github-actions[bot]" ||
			authorLogin == "dependabot[bot]"

		if isBot {
			continue
		}

		// Found a new human comment!
		logging.Info("Found new comment on PR",
			logging.String("task_id", task.ID),
			logging.String("author", authorLogin))

		return true, nil
	}

	return false, nil
}

// processRetries checks for tasks ready to be retried.
func (s *Scheduler) processRetries(ctx context.Context) {
	tasks, err := store.GetRetryableTasks(s.db)
	if err != nil {
		logging.ErrLog("Failed to get retryable tasks", logging.Err(err))
		return
	}

	for i := range tasks {
		task := &tasks[i]

		// Remove from retry queue
		store.RemoveFromRetryQueue(s.db, task.ID)

		// Check if we have capacity
		s.mu.RLock()
		runningCount := len(s.running)
		s.mu.RUnlock()

		if runningCount >= s.cfg.Agent.MaxConcurrentAgents {
			// Re-add to retry queue with short delay
			store.AddToRetryQueue(s.db, task.ID, time.Now().Add(10*time.Second))
			continue
		}

		logging.Info("Retrying task",
			logging.String("task_id", task.ID),
			logging.Int("retry_count", task.RetryCount))

		// Increment retry count
		newCount, err := store.IncrementRetryCount(s.db, task.ID)
		if err != nil {
			logging.ErrLog("Failed to increment retry count", logging.Err(err))
			continue
		}
		task.RetryCount = newCount

		// Start the task - startTask will handle the state transition to running
		// This prevents race condition where handleRunningTask sees running in DB
		// but task not yet in s.running map
		s.startTask(ctx, task)
	}
}

// startTask begins execution of a task.
func (s *Scheduler) startTask(ctx context.Context, task *store.Task) {
	// IMPORTANT: First, kill any old agent process that might still be running
	// This can happen after server restart or crash
	if task.AgentPID > 0 {
		if processExists(task.AgentPID) {
			logging.Warn("Killing old agent process before starting new one",
				logging.String("task_id", task.ID),
				logging.Int("old_pid", task.AgentPID))
			// Kill the entire process group
			syscall.Kill(-task.AgentPID, syscall.SIGTERM)
			time.Sleep(100 * time.Millisecond) // Give it time to terminate
		}
		// Clear the PID in database
		store.SetTaskPID(s.db, task.ID, 0)
	}

	// Create workspace - only clean it for new tasks (todo state)
	// For self-review and address-comment, preserve the existing workspace
	if err := s.workspace.EnsureRoot(); err != nil {
		logging.ErrLog("Failed to ensure workspace root", logging.Err(err))
		store.UpdateTaskState(s.db, task.ID, store.StateFailed, fmt.Sprintf("Workspace error: %v", err))
		return
	}

	// Check if we need to create a new workspace
	needCreate := false
	var wsPath string

	if task.State == store.StateTodo {
		// Always create clean workspace for new tasks
		needCreate = true
	} else {
		// For retry, self-review, and address-comment, check if workspace is valid
		if task.Workspace == "" {
			needCreate = true
			logging.Warn("Task has no workspace, creating new one",
				logging.String("task_id", task.ID),
				logging.String("state", string(task.State)))
		} else {
			// Check if workspace contains WORKFLOW.md (indicates valid git worktree)
			workflowPath := filepath.Join(task.Workspace, "WORKFLOW.md")
			if _, err := os.Stat(workflowPath); os.IsNotExist(err) {
				// Workspace is incomplete, recreate it
				needCreate = true
				logging.Warn("Workspace is incomplete (no WORKFLOW.md), recreating",
					logging.String("task_id", task.ID),
					logging.String("workspace", task.Workspace))
			} else {
				// Workspace is valid, use existing one
				wsPath = task.Workspace
			}
		}
	}

	if needCreate {
		// Clean up any existing worktree for this task to avoid conflicts
		if s.cfg.Hooks.BeforeRemove != "" {
			// Use before_remove hook to clean up old worktree
			oldWsPath := s.workspace.WorkspaceForIssue(task.ID)
			if _, err := os.Stat(oldWsPath); err == nil {
				if err := s.workspace.RunHook(s.cfg.Hooks.BeforeRemove, oldWsPath, task.ID, task.ID); err != nil {
					logging.Warn("Failed to cleanup old workspace", logging.Err(err))
				}
			}
		}

		// Create clean workspace
		ws, err := s.workspace.CreateForIssue(task.ID, task.ID)
		if err != nil {
			logging.ErrLog("Failed to create workspace", logging.Err(err))
			store.UpdateTaskState(s.db, task.ID, store.StateFailed, fmt.Sprintf("Workspace error: %v", err))
			return
		}
		wsPath = ws

		// Run after_create hook (git worktree setup)
		if s.cfg.Hooks.AfterCreate != "" {
			if err := s.workspace.RunHook(s.cfg.Hooks.AfterCreate, wsPath, task.ID, task.ID); err != nil {
				logging.Warn("Failed to run after_create hook", logging.Err(err))
			}
		}
	}

	logging.Info("Starting task",
		logging.String("task_id", task.ID),
		logging.String("title", task.Title),
		logging.String("workspace", wsPath))

	// Create agent
	ag, err := factory.NewAgent(s.cfg.Agent.Kind, s.cfg.Agent.Command, s.cfg.Agent.Args, wsPath)
	if err != nil {
		logging.ErrLog("Failed to create agent", logging.Err(err))
		store.UpdateTaskState(s.db, task.ID, store.StateFailed, fmt.Sprintf("Agent error: %v", err))
		s.workspace.Remove(wsPath)
		return
	}

	// Create cancellable context
	taskCtx, cancel := context.WithCancel(ctx)

	rt := &RunningTask{
		Task:       task,
		Agent:      ag,
		CancelFunc: cancel,
		StartTime:  time.Now(),
	}

	// IMPORTANT: Add to running map BEFORE updating database state
	// This prevents race condition where handleRunningTask sees running in DB but not in map
	s.mu.Lock()
	s.running[task.ID] = rt
	s.mu.Unlock()

	// Now update database state - after task is in running map
	if err := store.UpdateTaskState(s.db, task.ID, store.StateRunning, ""); err != nil {
		logging.ErrLog("Failed to transition task to running", logging.Err(err))
		// Remove from running map since we failed to update DB
		s.mu.Lock()
		delete(s.running, task.ID)
		s.mu.Unlock()
		cancel()
		return
	}
	store.SetTaskWorkspace(s.db, task.ID, wsPath)

	// Start task in goroutine
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runTask(taskCtx, rt, task, wsPath)
		
		// Remove from running map after completion
		s.mu.Lock()
		delete(s.running, task.ID)
		s.mu.Unlock()
		
		logging.Debug("Task removed from running map", logging.String("task_id", task.ID))
	}()
}

// runTask executes the agent for a task.
func (s *Scheduler) runTask(ctx context.Context, rt *RunningTask, task *store.Task, wsPath string) {
	logging.Info("runTask: Starting agent", logging.String("task_id", task.ID))

	// Start agent session
	if err := rt.Agent.StartSession(ctx, agent.SessionConfig{
		Workspace: wsPath,
		Env:       s.cfg.Agent.Env,
		MaxTurns:  s.cfg.Agent.MaxTurns,
	}); err != nil {
		logging.ErrLog("Failed to start agent session", logging.String("task_id", task.ID), logging.Err(err))
		s.handleTaskFailure(task, err)
		return
	}

	logging.Info("runTask: Agent session started", logging.String("task_id", task.ID))

	// Set agent PID if available
	// This allows us to track the process after server restart
	if aiAgent, ok := rt.Agent.(interface{ PID() int }); ok {
		if pid := aiAgent.PID(); pid > 0 {
			if err := store.SetTaskPID(s.db, task.ID, pid); err != nil {
				logging.Warn("Failed to set agent PID", logging.Err(err))
			} else {
				logging.Info("Set agent PID",
					logging.String("task_id", task.ID),
					logging.Int("pid", pid))
			}
		}
	}

	// Build prompt from WORKFLOW.md
	prompt := s.buildPrompt(task)
	logging.Info("runTask: Sending prompt", logging.String("task_id", task.ID))

	if err := rt.Agent.SendPrompt(ctx, prompt); err != nil {
		logging.ErrLog("Failed to send prompt", logging.String("task_id", task.ID), logging.Err(err))
		s.handleTaskFailure(task, err)
		return
	}

	logging.Info("runTask: Waiting for events", logging.String("task_id", task.ID))

	// Wait for completion
	eventCount := 0
	for event := range rt.Agent.Events() {
		eventCount++
		logging.Info("runTask: Received event",
			logging.String("task_id", task.ID),
			logging.String("event_type", event.Type),
			logging.Int("event_count", eventCount))

		switch event.Type {
		case agent.EventAgentEnd:
			// Reload task from database to get latest state (PRNumber, etc.)
			// The task object passed to runTask is stale - it doesn't reflect
			// updates made during agent execution (like PR creation)
			latestTask, err := store.GetTask(s.db, task.ID)
			if err != nil {
				logging.ErrLog("Failed to reload task", logging.String("task_id", task.ID), logging.Err(err))
				// Fall back to using stale task object
				latestTask = task
			}

			// Verify that the task actually completed successfully
			// Check if task has PR (for non-review tasks) or other completion indicators
			completedSuccessfully := s.verifyTaskCompletion(latestTask)

			if completedSuccessfully {
				logging.Info("Task completed successfully", logging.String("task_id", task.ID))

				// Remove from running map BEFORE updating database to prevent orphaned task race
				s.mu.Lock()
				delete(s.running, task.ID)
				s.mu.Unlock()

				// Transition based on latest task state:
				// - Self review tasks complete directly to Done
				// - Other tasks transition to Self Review
				if latestTask.State == store.StateSelfReview || latestTask.State == store.StateAddressComment {
					store.UpdateTaskState(s.db, task.ID, store.StateDone, "")
				} else {
					store.UpdateTaskState(s.db, task.ID, store.StateSelfReview, "")
				}
			} else {
				// Task didn't complete properly - mark as failed
				logging.Warn("Task ended without completion (no PR or commits), marking as failed",
					logging.String("task_id", task.ID),
					logging.String("state", string(task.State)))

				s.mu.Lock()
				delete(s.running, task.ID)
				s.mu.Unlock()

				s.handleTaskFailure(task, fmt.Errorf("agent ended without creating PR or completing required work"))
			}
			return

		case agent.EventError:
			errMsg := ""
			if errData, ok := event.Data["error"].(string); ok {
				errMsg = errData
			}
			logging.ErrLog("Task failed with error", logging.String("task_id", task.ID), logging.String("error", errMsg))

			// Remove from running map BEFORE updating database to prevent orphaned task race
			s.mu.Lock()
			delete(s.running, task.ID)
			s.mu.Unlock()

			s.handleTaskFailure(task, fmt.Errorf("%s", errMsg))
			return
		}
	}

	logging.Warn("runTask: Events channel closed without completion event",
		logging.String("task_id", task.ID),
		logging.Int("total_events", eventCount))

	// Agent closed without explicit end - assume success
	logging.Info("Agent finished", logging.String("task_id", task.ID))
	store.UpdateTaskState(s.db, task.ID, store.StateDone, "")
}

// verifyTaskCompletion checks if a task has actually completed successfully.
// Returns true if:
// - Task is in SelfReview/AddressComment state (PR already exists)
// - Task has a PR number (PR was created)
// Returns false if:
// - Task is in Running/Todo state but has no PR
func (s *Scheduler) verifyTaskCompletion(task *store.Task) bool {
	// Tasks in review states are considered complete (PR already exists)
	if task.State == store.StateSelfReview || task.State == store.StateAddressComment {
		return true
	}

	// Check if PR was created
	if task.PRNumber > 0 {
		return true
	}

	// Task ended without creating PR - not completed
	return false
}

// handleTaskFailure handles a task failure with retry logic.
func (s *Scheduler) handleTaskFailure(task *store.Task, err error) {
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	// Check if we can retry
	if task.RetryCount < s.cfg.Agent.MaxRetries {
		// Mark as failed and schedule retry
		store.UpdateTaskState(s.db, task.ID, store.StateFailed, errMsg)

		// Calculate retry delay with exponential backoff
		delay := s.cfg.GetRetryDelay(task.RetryCount)
		nextRetry := time.Now().Add(delay)

		logging.Info("Scheduling task retry",
			logging.String("task_id", task.ID),
			logging.Int("retry_count", task.RetryCount),
			logging.Duration("delay", delay))

		store.AddToRetryQueue(s.db, task.ID, nextRetry)
	} else {
		// Max retries exceeded
		store.UpdateTaskState(s.db, task.ID, store.StateFailed, fmt.Sprintf("Max retries exceeded: %s", errMsg))
		logging.Warn("Task failed permanently", logging.String("task_id", task.ID))
	}
}

// GetRunningCount returns the number of currently running tasks.
func (s *Scheduler) GetRunningCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.running)
}

// GetRunningTasks returns information about running tasks.
func (s *Scheduler) GetRunningTasks() []*RunningTask {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*RunningTask, 0, len(s.running))
	for _, rt := range s.running {
		result = append(result, rt)
	}
	return result
}

// RetryTask manually triggers a retry for a failed task.
func (s *Scheduler) RetryTask(ctx context.Context, taskID string) error {
	task, err := store.GetTask(s.db, taskID)
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}
	if task == nil {
		return fmt.Errorf("task not found: %s", taskID)
	}

	if task.State != store.StateFailed {
		return fmt.Errorf("can only retry failed tasks, current state: %s", task.State)
	}

	// Remove from retry queue if present
	store.RemoveFromRetryQueue(s.db, taskID)

	// Increment retry count
	newCount, err := store.IncrementRetryCount(s.db, taskID)
	if err != nil {
		return fmt.Errorf("increment retry count: %w", err)
	}
	task.RetryCount = newCount

	// Transition to running
	if err := store.UpdateTaskState(s.db, taskID, store.StateRunning, ""); err != nil {
		return fmt.Errorf("update task state: %w", err)
	}
	task.State = store.StateRunning

	// Start the task
	s.startTask(ctx, task)

	return nil
}

// buildPrompt constructs the agent prompt from WORKFLOW.md
func (s *Scheduler) buildPrompt(task *store.Task) string {
	// Try to read WORKFLOW.md from the task's workspace
	var workflowContent string
	var err error

	if task.Workspace != "" {
		workflowPath := filepath.Join(task.Workspace, "WORKFLOW.md")
		content, readErr := os.ReadFile(workflowPath)
		if readErr == nil {
			workflowContent = string(content)
		} else {
			err = readErr
		}
	}

	if workflowContent == "" {
		// Fallback to simple prompt if WORKFLOW.md not found
		logging.Warn("WORKFLOW.md not found, using default prompt",
			logging.String("task_id", task.ID),
			logging.String("workspace", task.Workspace),
			logging.Err(err))

		return fmt.Sprintf(`You are working on the following task:

Title: %s

Description:
%s

Work in the provided workspace. Complete the task and report your results.`, task.Title, task.Description)
	}

	// Replace template variables
	prompt := s.replaceTemplateVars(workflowContent, task)

	return prompt
}

// replaceTemplateVars replaces template variables in the workflow content
func (s *Scheduler) replaceTemplateVars(content string, task *store.Task) string {
	// Simple variable replacement (can be enhanced with proper templating)
	replacements := map[string]string{
		"{{ task.id }}":     task.ID,
		"{{ task.title }}":  task.Title,
		"{{ task.description }}": task.Description,
		"{{ task.state }}":  string(task.State),
		"{{ task.labels }}": "",
		"{{ task.url }}":    fmt.Sprintf("http://localhost:8080/tasks/%s", task.ID),
		"{{.TaskID }}":      task.ID,
		"{{.TaskTitle }}":   task.Title,
		"{{.Repo }}":        "your-org/your-repo", // TODO: from config
		"{{.Workspace }}":   task.Workspace,
	}

	result := content
	for placeholder, value := range replacements {
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}
// getPRInfo fetches PR information for a task.
func (s *Scheduler) getPRInfo(task *store.Task) (prNumber int, prURL string, err error) {
	if task.Workspace == "" {
		return 0, "", fmt.Errorf("no workspace")
	}

	// Run gh pr list to get PR info
	cmd := exec.Command("gh", "pr", "list", "--head", task.ID, "--json", "number,url,state")
	cmd.Dir = task.Workspace
	output, err := cmd.Output()
	if err != nil {
		return 0, "", fmt.Errorf("gh pr list: %w", err)
	}

	// Parse JSON output
	var prs []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(output, &prs); err != nil {
		return 0, "", fmt.Errorf("parse pr list: %w", err)
	}

	if len(prs) == 0 {
		return 0, "", nil // No PR found
	}

	return prs[0].Number, prs[0].URL, nil
}
