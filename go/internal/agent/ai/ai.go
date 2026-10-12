// Package ai provides the ai agent implementation.
//
// It talks to the `ai` CLI (github.com/tiancaiamao/ai) through its public
// command surface only — no internal file protocols:
//
//	ai serve --id-file <file> --name <name> [--model <id>]
//	    Long-lived worker. Publishes its run ID into --id-file once the ACP
//	    socket is live; that ID is the correlation key for everything else.
//	    The process outlives turns, so it is stopped explicitly.
//
//	ai send --id <runID> --wait --timeout <d> --summary
//	    Runs one prompt synchronously. Exit codes: 0 = turn completed,
//	    2 = timed out (the turn is still in flight), anything else = error.
//	    The prompt is fed on stdin to stay clear of ARG_MAX.
//
//	ai kill --id <runID>
//	    Stops the worker.
//
// Event mapping (the old ndjson client conflated "stdout closed" and "process
// exited" with agent_end, which turned crashes into false completions):
//
//	ai send exit 0               -> agent.EventAgentEnd
//	ai send exit 2 (timeout)     -> agent.EventError  (worker left alive)
//	ai send exit non-zero, other -> agent.EventError
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"symphonia/internal/agent"
	"symphonia/internal/logging"
)

const (
	// startTimeout bounds how long StartSession waits for `ai serve` to
	// publish its run ID.
	startTimeout = 60 * time.Second
	// idPollInterval is the re-read period of the id file while starting.
	idPollInterval = 50 * time.Millisecond
	// stopGracePeriod is how long `ai kill` may take before the worker's
	// process group is signalled directly.
	stopGracePeriod = 5 * time.Second
	// maxLoggedOutput caps how much CLI output is echoed into Symphony's log.
	maxLoggedOutput = 4000
	// stderrTailLines is how many `ai serve` stderr lines are kept for error
	// reporting.
	stderrTailLines = 8
)

// exitCodeTimeout is `ai send`'s exit code for "the turn is still in flight".
const exitCodeTimeout = 2

// New creates a new ai agent instance. The process is not started here; call
// StartSession for that (the scheduler constructs the agent, then starts it).
//
// baseArgs are the subcommand and any flags coming from config (e.g.
// ["serve"]); ai.go appends everything else it needs.
func New(binaryPath string, baseArgs []string, workspace string) (*AIAgent, error) {
	if strings.TrimSpace(binaryPath) == "" {
		return nil, fmt.Errorf("ai: empty binary path")
	}
	return &AIAgent{
		binPath:   binaryPath,
		baseArgs:  append([]string{}, baseArgs...),
		workspace: workspace,
		events:    make(chan agent.Event, 100),
		exited:    make(chan struct{}),
		output:    newTailBuffer(stderrTailLines),
	}, nil
}

// AIAgent implements the agent.Agent interface using the ai CLI.
type AIAgent struct {
	mu        sync.Mutex
	binPath   string
	baseArgs  []string
	workspace string

	cmd     *exec.Cmd
	idFile  string
	runID   string
	events  chan agent.Event
	stopped bool

	exited  chan struct{}
	exitErr error
	output  *tailBuffer
}

// Name returns the agent name.
func (a *AIAgent) Name() string { return "ai" }

// PID returns the process ID of the worker, or 0 if it is not running.
//
// It prefers the pid recorded by `ai serve` in ~/.ai/runs/<runID>/run.json so
// the value stays correct across a Symphony restart (after a restart the
// spawned handle is gone, but a surviving worker is still an orphan we must be
// able to signal).
func (a *AIAgent) PID() int {
	a.mu.Lock()
	runID := a.runID
	var spawned int
	if a.cmd != nil && a.cmd.Process != nil {
		spawned = a.cmd.Process.Pid
	}
	a.mu.Unlock()

	if runID != "" {
		if pid := pidFromRunMeta(runID); pid > 0 {
			return pid
		}
	}
	return spawned
}

// StartSession spawns the `ai serve` worker and waits for it to publish its
// run ID. It returns as soon as the worker's ACP socket is live; no prompt is
// sent here.
func (a *AIAgent) StartSession(ctx context.Context, cfg agent.SessionConfig) error {
	a.mu.Lock()
	if a.runID != "" {
		a.mu.Unlock()
		return fmt.Errorf("ai: session already started (run %s)", a.runID)
	}
	if a.cmd != nil && a.cmd.Process != nil {
		a.mu.Unlock()
		return fmt.Errorf("ai: worker already running")
	}
	a.mu.Unlock()

	idFile, err := os.CreateTemp("", "symphony-ai-*.id")
	if err != nil {
		return fmt.Errorf("ai: create id file: %w", err)
	}
	idFile.Close()
	idFileName := idFile.Name()
	// Consumed below; the file only ever holds one run ID.
	defer os.Remove(idFileName)

	workspace := cfg.Workspace
	if workspace == "" {
		workspace = a.workspace
	}

	args := append([]string{}, a.baseArgs...)
	args = append(args, "--id-file", idFileName)
	// Symphony workspaces are named after the task identifier.
	if name := filepath.Base(workspace); name != "" && name != "." && name != string(filepath.Separator) {
		args = append(args, "--name", name)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	// cfg.MaxTurns is deliberately not forwarded: one Symphony session spans
	// several prompts, and a per-session turn cap would abort later rounds.

	// exec.Command (not CommandContext): the worker's lifetime is owned by
	// Stop(), not by the prompt's context. A cancelled prompt must never take
	// a still-working agent down with it.
	cmd := exec.Command(a.binPath, args...)
	cmd.Dir = workspace
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), envSlice(cfg.Env)...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// `ai serve` also puts itself in its own process group, so the whole group
	// can be signalled (which is what the scheduler's orphan kill does).
	cmd.Stdout = a.output
	cmd.Stderr = a.output

	logging.Info("ai: starting worker",
		logging.String("binary", a.binPath),
		logging.String("args", strings.Join(args, " ")),
		logging.String("workspace", workspace))

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ai: start serve: %w", err)
	}

	a.mu.Lock()
	a.cmd = cmd
	a.mu.Unlock()

	// Reap the worker whenever it exits, so it never becomes a zombie.
	go a.wait()

	runID, err := a.readRunID(idFileName)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.runID = runID
	if a.workspace == "" {
		a.workspace = workspace
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	a.mu.Unlock()

	logging.Info("ai: worker ready",
		logging.String("run_id", runID),
		logging.Int("pid", pid))
	return nil
}

// SendPrompt runs one prompt synchronously and emits exactly one terminal
// event (see the package comment for the exit-code mapping).
//
// It blocks until the turn finishes: Symphony's scheduler sends one prompt per
// agent and then ranges over Events(), so the event is queued before this
// returns and the ordering is preserved.
func (a *AIAgent) SendPrompt(ctx context.Context, prompt string) error {
	a.mu.Lock()
	runID := a.runID
	spawned := 0
	if a.cmd != nil && a.cmd.Process != nil {
		spawned = a.cmd.Process.Pid
	}
	a.mu.Unlock()

	if runID == "" {
		return fmt.Errorf("ai: session not started")
	}

	promptFile, err := os.CreateTemp("", "symphony-ai-prompt-*")
	if err != nil {
		return fmt.Errorf("ai: write prompt: %w", err)
	}
	if _, err := promptFile.WriteString(prompt); err != nil {
		promptFile.Close()
		os.Remove(promptFile.Name())
		return fmt.Errorf("ai: write prompt: %w", err)
	}
	if _, err := promptFile.Seek(0, 0); err != nil {
		promptFile.Close()
		os.Remove(promptFile.Name())
		return fmt.Errorf("ai: rewind prompt: %w", err)
	}
	defer func() {
		promptFile.Close()
		os.Remove(promptFile.Name())
	}()

	// A caller that has already given up must not reach the CLI at all.
	if err := ctx.Err(); err != nil {
		return err
	}

	// GOTCHA 1: `ai send --wait` defaults to a 4m timeout and exits 2 when it
	// fires, which would report a still-running turn as failed. Default to
	// unlimited, and only cap the wait when the caller imposed a deadline.
	timeout := "0"
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline).Round(time.Millisecond)
		if remaining <= 0 {
			return ctx.Err()
		}
		if remaining < time.Millisecond {
			remaining = time.Millisecond
		}
		timeout = remaining.String()
	}

	args := []string{"send", "--id", runID, "--wait", "--timeout", timeout, "--summary"}
	cmd := exec.CommandContext(ctx, a.binPath, args...)
	cmd.Dir = a.workspace
	cmd.Stdin = promptFile
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	logging.Debug("ai: sending prompt",
		logging.String("run_id", runID),
		logging.Int("prompt_bytes", len(prompt)),
		logging.String("timeout", timeout))

	start := time.Now()
	err = cmd.Run()
	elapsed := time.Since(start)

	// The final assistant text is the only place the turn's outcome is
	// visible; `ai` keeps its own trace, but Symphony's log must carry it too.
	if text := truncate(strings.TrimSpace(stdout.String()), maxLoggedOutput); text != "" {
		logging.Info("ai: turn output",
			logging.String("run_id", runID),
			logging.Int("elapsed_ms", int(elapsed.Milliseconds())),
			logging.String("text", text))
	}

	if err == nil {
		logging.Info("ai: turn completed", logging.String("run_id", runID))
		a.emit(agent.Event{Type: agent.EventAgentEnd})
		// Symphony runs one prompt per agent and drops the agent afterwards,
		// so the worker would otherwise linger as a detached `ai serve`.
		// Runs in the background: the event is already queued.
		go a.stopWorker()
		return nil
	}

	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}

	if exitCode == exitCodeTimeout {
		logging.Warn("ai: turn timed out, worker still in flight",
			logging.String("run_id", runID),
			logging.Int("spawned_pid", spawned))
		a.emit(agent.Event{
			Type: agent.EventError,
			Data: map[string]interface{}{
				"error":   fmt.Sprintf("turn timed out after %s; the worker is still running (ai kill --id %s)", timeout, runID),
				"run_id":  runID,
				"timeout": timeout,
			},
		})
		return nil
	}

	logging.ErrLog("ai: send failed",
		logging.String("run_id", runID),
		logging.Int("exit_code", exitCode),
		logging.String("stderr", truncate(strings.TrimSpace(stderr.String()), maxLoggedOutput)))

	a.emit(agent.Event{
		Type: agent.EventError,
		Data: map[string]interface{}{
			"error":     fmt.Sprintf("ai send failed (exit %d): %s", exitCode, truncate(strings.TrimSpace(stderr.String()), maxLoggedOutput)),
			"run_id":    runID,
			"exit_code": exitCode,
		},
	})
	return nil
}

// SendCommand forwards a registered slash command as the first token of a
// prompt. ACP has no separate command message: text whose first token names a
// registered command runs synchronously.
func (a *AIAgent) SendCommand(ctx context.Context, cmd agent.Command) error {
	return a.SendPrompt(ctx, cmd.Type)
}

// Events returns the event channel. It is closed by Stop.
func (a *AIAgent) Events() <-chan agent.Event { return a.events }

// Stop stops the worker: graceful `ai kill` first, escalating to SIGTERM/SIGKILL
// on the process group if the worker does not exit. It is idempotent.
func (a *AIAgent) Stop() error {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return nil
	}
	a.stopped = true
	runID := a.runID
	var proc *os.Process
	if a.cmd != nil {
		proc = a.cmd.Process
	}
	close(a.events)
	a.mu.Unlock()

	// `ai kill` refuses to act on a run that is no longer alive, which is a
	// normal outcome here (worker crashed or already stopped) — not an error.
	if runID != "" {
		killCtx, cancel := context.WithTimeout(context.Background(), stopGracePeriod)
		defer cancel()
		out, err := exec.CommandContext(killCtx, a.binPath, "kill", "--id", runID).
			CombinedOutput()
		if err != nil {
			logging.Debug("ai: kill worker (may already be gone)",
				logging.String("run_id", runID),
				logging.Err(err),
				logging.String("output", truncate(strings.TrimSpace(string(out)), maxLoggedOutput)))
		} else {
			logging.Info("ai: worker stopped", logging.String("run_id", runID))
		}
	}

	if proc != nil {
		a.terminate(proc)
	}
	return nil
}

// terminate reaps the worker process, escalating if it ignores `ai kill`.
func (a *AIAgent) terminate(proc *os.Process) {
	if !a.waitExited(stopGracePeriod) {
		logging.Warn("ai: worker did not stop after kill, sending SIGTERM",
			logging.Int("pid", proc.Pid))
		_ = syscall.Kill(-proc.Pid, syscall.SIGTERM)
		if !a.waitExited(stopGracePeriod) {
			logging.Warn("ai: worker did not stop after SIGTERM, sending SIGKILL",
				logging.Int("pid", proc.Pid))
			_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
			a.waitExited(stopGracePeriod)
		}
	}
}

// stopWorker is the internal teardown used once Symphony is done with the
// worker. Stop() remains callable (and idempotent) from the outside.
func (a *AIAgent) stopWorker() {
	if err := a.Stop(); err != nil {
		logging.Debug("ai: stop worker", logging.Err(err))
	}
}

// wait reaps the worker process and records how it exited.
func (a *AIAgent) wait() {
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		close(a.exited)
		return
	}

	err := cmd.Wait()
	a.mu.Lock()
	a.exitErr = err
	a.mu.Unlock()

	logging.Debug("ai: worker exited", logging.Int("pid", cmd.Process.Pid), logging.Err(err))
	close(a.exited)
}

// waitExited reports whether the worker process was reaped within d.
func (a *AIAgent) waitExited(d time.Duration) bool {
	select {
	case <-a.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// readRunID polls the id file written by `ai serve`. The file only appears
// once the ACP socket is live, so a run ID here means `ai send` can connect
// right away. It fails fast if the worker dies during startup (bad --role,
// socket error, agent setup failure), which would otherwise hang the task.
func (a *AIAgent) readRunID(idFileName string) (string, error) {
	deadline := time.Now().Add(startTimeout)
	for {
		data, err := os.ReadFile(idFileName)
		if err == nil {
			if id := strings.TrimSpace(string(data)); id != "" {
				return id, nil
			}
		}

		select {
		case <-a.exited:
			a.mu.Lock()
			exitErr := a.exitErr
			a.mu.Unlock()
			return "", fmt.Errorf("ai: worker exited during startup (exit: %v; last output: %s)",
				exitErr, a.output.String())
		default:
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("ai: timed out after %s waiting for run ID (last output: %s)",
				startTimeout, a.output.String())
		}
		time.Sleep(idPollInterval)
	}
}

// emit queues an event. It never blocks: the scheduler drains the channel
// right after SendPrompt returns, and a full buffer means nobody is listening.
func (a *AIAgent) emit(ev agent.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return
	}
	select {
	case a.events <- ev:
	default:
		logging.Warn("ai: event dropped, nobody is consuming events",
			logging.String("type", ev.Type))
	}
}

// runMeta is the subset of ~/.ai/runs/<id>/run.json we need.
type runMeta struct {
	ID     string `json:"id"`
	PID    int    `json:"pid"`
	Status string `json:"status"`
	Name   string `json:"name"`
}

// pidFromRunMeta reads the worker's pid out of its run.json, or 0.
func pidFromRunMeta(runID string) int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(home, ".ai", "runs", runID, "run.json"))
	if err != nil {
		return 0
	}
	var meta runMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return 0
	}
	return meta.PID
}

// envSlice turns a config env map into KEY=VALUE entries.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	return out
}

// tailBuffer keeps the last n lines written to it.
type tailBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		t.lines = append(t.lines, line)
		if len(t.lines) > t.max {
			t.lines = t.lines[len(t.lines)-t.max:]
		}
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}

// truncate shortens s to n bytes, marking the cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("... [truncated %d bytes]", len(s)-n)
}
