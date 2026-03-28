// Package orchestrator provides the main orchestration logic.
package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"symphonia/internal/agent"
	"symphonia/internal/agent/factory"
	"symphonia/internal/config"
	"symphonia/internal/logging"
	"symphonia/internal/tracker"
	"symphonia/internal/workspace"
)

// Orchestrator manages the polling loop and issue dispatch.
type Orchestrator struct {
	cfg       *config.Config
	tracker   tracker.Tracker
	workspace *workspace.Manager

	mu      sync.RWMutex
	running map[string]*RunningEntry
	done    map[string]bool
	claimed map[string]bool

	pollInterval time.Duration
	stopCh       chan struct{}
	wg           sync.WaitGroup
}

// RunningEntry represents a currently running issue.
type RunningEntry struct {
	IssueID    string
	Identifier string
	SessionID  string
	Workspace  string
	StartTime  time.Time
	Agent      agent.Agent
	CancelFunc context.CancelFunc
}

// New creates a new orchestrator.
func New(cfg *config.Config, t tracker.Tracker, wm *workspace.Manager) *Orchestrator {
	return &Orchestrator{
		cfg:       cfg,
		tracker:   t,
		workspace: wm,
		running:   make(map[string]*RunningEntry),
		done:      make(map[string]bool),
		claimed:   make(map[string]bool),
	}
}

// Start begins the orchestration loop.
func (o *Orchestrator) Start(ctx context.Context) error {
	o.pollInterval = time.Duration(o.cfg.Polling.IntervalMs) * time.Millisecond
	o.stopCh = make(chan struct{})

	logging.Info("Orchestrator starting")

	if err := o.workspace.EnsureRoot(); err != nil {
		return fmt.Errorf("workspace root: %w", err)
	}

	o.pollCycle(ctx)

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		ticker := time.NewTicker(o.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-o.stopCh:
				return
			case <-ticker.C:
				o.pollCycle(ctx)
			}
		}
	}()
	return nil
}

// Stop gracefully stops the orchestrator.
func (o *Orchestrator) Stop() {
	close(o.stopCh)
	o.wg.Wait()

	o.mu.Lock()
	defer o.mu.Unlock()
	for _, entry := range o.running {
		if entry.CancelFunc != nil {
			entry.CancelFunc()
		}
		if entry.Agent != nil {
			entry.Agent.Stop()
		}
	}
}

// pollCycle performs one polling cycle.
func (o *Orchestrator) pollCycle(ctx context.Context) {
	candidates, err := o.tracker.FetchCandidateIssues()
	if err != nil {
		logging.ErrLog("Failed to fetch candidates", logging.Err(err))
		return
	}

	logging.Info("Fetched candidate issues", logging.Int("count", len(candidates)))

	o.reconcile(ctx)
	o.dispatchEligible(ctx, candidates)
}

// dispatchEligible dispatches issues that are eligible.
func (o *Orchestrator) dispatchEligible(ctx context.Context, candidates []tracker.Issue) {
	o.mu.Lock()
	defer o.mu.Unlock()

	maxConcurrent := o.cfg.Agent.MaxConcurrentAgents
	availableSlots := maxConcurrent - len(o.running)
	if availableSlots <= 0 {
		return
	}

	var eligible []tracker.Issue
	for _, issue := range candidates {
		if o.isEligible(&issue) {
			eligible = append(eligible, issue)
			o.claimed[issue.ID] = true
			if len(eligible) >= availableSlots {
				break
			}
		}
	}

	for _, issue := range eligible {
		go o.runIssue(ctx, &issue)
	}
}

func (o *Orchestrator) isEligible(issue *tracker.Issue) bool {
	if _, running := o.running[issue.ID]; running {
		return false
	}
	if o.claimed[issue.ID] {
		return false
	}
	if o.done[issue.ID] {
		return false
	}
	return isActiveState(issue.State, o.cfg.Tracker.ActiveStates)
}

func isActiveState(state string, activeStates []string) bool {
	normalized := strings.ToLower(strings.TrimSpace(state))
	for _, active := range activeStates {
		if strings.ToLower(strings.TrimSpace(active)) == normalized {
			return true
		}
	}
	return false
}

func isTerminalState(state string, terminalStates []string) bool {
	normalized := strings.ToLower(strings.TrimSpace(state))
	for _, terminal := range terminalStates {
		if strings.ToLower(strings.TrimSpace(terminal)) == normalized {
			return true
		}
	}
	return false
}

// runIssue runs a single issue.
func (o *Orchestrator) runIssue(ctx context.Context, issue *tracker.Issue) {
	sessionID := uuid.New().String()

	logging.Info("Starting agent run",
		logging.String("issue_id", issue.ID),
		logging.String("issue_identifier", issue.Identifier),
		logging.String("session_id", sessionID),
	)

	ws, err := o.workspace.CreateForIssue(issue.ID, issue.Identifier)
	if err != nil {
		logging.ErrLog("Failed to create workspace", logging.String("issue_id", issue.ID), logging.Err(err))
		o.completeIssue(issue.ID)
		return
	}

	o.workspace.RunHook(o.cfg.Hooks.AfterCreate, ws, issue.ID, issue.Identifier)
	o.workspace.RunHook(o.cfg.Hooks.BeforeRun, ws, issue.ID, issue.Identifier)

	issueCtx, cancel := context.WithCancel(ctx)
	entry := &RunningEntry{
		IssueID:    issue.ID,
		Identifier: issue.Identifier,
		SessionID:  sessionID,
		Workspace:  ws,
		StartTime:  time.Now(),
		CancelFunc: cancel,
	}

	o.mu.Lock()
	o.running[issue.ID] = entry
	o.mu.Unlock()

	agentAgent, err := factory.NewAgent(o.cfg.Agent.Kind, o.cfg.Agent.Command, o.cfg.Agent.Args)
	if err != nil {
		logging.ErrLog("Failed to create agent", logging.String("issue_id", issue.ID), logging.Err(err))
		cancel()
		o.completeIssue(issue.ID)
		return
	}
	entry.Agent = agentAgent

	if err := agentAgent.StartSession(issueCtx, agent.SessionConfig{
		Workspace: ws,
		Env:       o.cfg.Agent.Env,
		MaxTurns:  o.cfg.Agent.MaxTurns,
	}); err != nil {
		logging.Warn("Agent session failed", logging.String("issue_id", issue.ID), logging.Err(err))
		agentAgent.Stop()
		cancel()
		o.completeIssue(issue.ID)
		return
	}

	prompt := fmt.Sprintf(`You are working on Linear issue %s

Title: %s
Current status: %s

Description:
%s

Work only in the provided workspace. Focus on completing the task.`, issue.Identifier, issue.Title, issue.State, issue.Description)

	if err := agentAgent.SendPrompt(ctx, prompt); err != nil {
		logging.ErrLog("Failed to send prompt", logging.String("issue_id", issue.ID), logging.Err(err))
		agentAgent.Stop()
		cancel()
		o.completeIssue(issue.ID)
		return
	}

	for event := range agentAgent.Events() {
		if event.Type == agent.EventAgentEnd || event.Type == agent.EventError {
			break
		}
	}

	o.workspace.RunHook(o.cfg.Hooks.AfterRun, ws, issue.ID, issue.Identifier)
	agentAgent.Stop()
	cancel()
	o.completeIssue(issue.ID)

	logging.Info("Completed agent run",
		logging.String("issue_id", issue.ID),
		logging.String("issue_identifier", issue.Identifier),
		logging.String("session_id", sessionID),
	)
}

func (o *Orchestrator) completeIssue(issueID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.running, issueID)
	delete(o.claimed, issueID)
	o.done[issueID] = true
}

func (o *Orchestrator) reconcile(ctx context.Context) {
	o.mu.RLock()
	var runningIDs []string
	for id := range o.running {
		runningIDs = append(runningIDs, id)
	}
	o.mu.RUnlock()

	if len(runningIDs) == 0 {
		return
	}

	states, err := o.tracker.FetchIssueStatesByIDs(runningIDs)
	if err != nil {
		logging.ErrLog("Reconciliation failed", logging.Err(err))
		return
	}

	for _, issue := range states {
		if isTerminalState(issue.State, o.cfg.Tracker.TerminalStates) {
			o.cancelIssue(issue.ID)
		}
	}
}

func (o *Orchestrator) cancelIssue(issueID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if entry, ok := o.running[issueID]; ok {
		if entry.CancelFunc != nil {
			entry.CancelFunc()
		}
		if entry.Agent != nil {
			entry.Agent.Stop()
		}
		delete(o.running, issueID)
		logging.Info("Cancelled issue due to state change", logging.String("issue_id", issueID))
	}
}

// Status returns the current orchestrator status.
func (o *Orchestrator) Status() *Status {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return &Status{
		RunningCount: len(o.running),
		DoneCount:    len(o.done),
		Running: func() []*RunningEntry {
			r := make([]*RunningEntry, 0, len(o.running))
			for _, e := range o.running {
				r = append(r, e)
			}
			return r
		}(),
	}
}

// Status represents the orchestrator status.
type Status struct {
	RunningCount int
	DoneCount    int
	Running      []*RunningEntry
}