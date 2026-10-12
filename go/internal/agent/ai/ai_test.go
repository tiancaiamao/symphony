package ai

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"symphonia/internal/agent"
	"symphonia/internal/logging"
)

// TestMain initializes the global logger; production wires this up in main.
func TestMain(m *testing.M) {
	logging.Init("error")
	os.Exit(m.Run())
}

// fakeAIScript is a stand-in for the `ai` CLI. It implements just enough of
// the surface internal/agent/ai uses, and records every invocation so tests
// can assert on the flags Symphony passes:
//
//	serve --id-file <f>   -> writes ~/.ai/runs/<id>/run.json, publishes the ID,
//	                         then parks in `sleep` (survives across turns)
//	send --id <id> ...    -> prints the final assistant text, exits with the
//	                         code from FAKE_AI_EXIT (default 0)
//	kill --id <id>        -> stops the parked serve process
const fakeAIScript = `#!/bin/sh
set -u
LOG="${FAKE_AI_LOG:?}"
EXIT="${FAKE_AI_EXIT:-/dev/null}"
sub="$1"; shift

case "$sub" in
serve)
  echo "serve args: $*" >> "$LOG"
  idfile=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --id-file) idfile="$2"; shift 2;;
      *) shift;;
    esac
  done
  if [ -f "$HOME/fake-ai-fail-start" ]; then
    echo "failServe: simulated startup failure" >> "$LOG"
    exit 1
  fi
  sleep 0.15
  id=$(printf '%06x' $$)
  mkdir -p "$HOME/.ai/runs/$id"
  printf '{"id":"%s","pid":%d,"status":"running"}\n' "$id" "$$" > "$HOME/.ai/runs/$id/run.json"
  printf '%s\n' "$id" > "$idfile"
  exec sleep 300
  ;;
send)
  echo "send args: $*" >> "$LOG"
  msg=$(cat)
  echo "send stdin: $msg" >> "$LOG"
  if [ -f "$EXIT" ]; then
    exit "$(cat "$EXIT")"
  fi
  echo "final assistant text: $msg"
  exit 0
  ;;
kill)
  echo "kill args: $*" >> "$LOG"
  id=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --id) id="$2"; shift 2;;
      *) shift;;
    esac
  done
  pid=$(sed -n 's/.*"pid":\([0-9]*\).*/\1/p' "$HOME/.ai/runs/$id/run.json" 2>/dev/null)
  if [ -n "$pid" ]; then kill "$pid" 2>/dev/null; fi
  exit 0
  ;;
esac
exit 0
`

// fakeCLI is a test double for the ai binary.
type fakeCLI struct {
	bin      string
	logPath  string
	exitPath string
}

func newFakeCLI(t *testing.T) *fakeCLI {
	t.Helper()
	// isolate ~/.ai (run.json) per test
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	f := &fakeCLI{
		bin:      filepath.Join(dir, "ai"),
		logPath:  filepath.Join(dir, "calls.log"),
		exitPath: filepath.Join(dir, "exit"),
	}
	if err := os.WriteFile(f.bin, []byte(fakeAIScript), 0o755); err != nil {
		t.Fatalf("write fake ai: %v", err)
	}
	t.Setenv("FAKE_AI_LOG", f.logPath)
	t.Setenv("FAKE_AI_EXIT", f.exitPath)
	return f
}

// setExit controls the exit code of the next `ai send`.
func (f *fakeCLI) setExit(t *testing.T, code int) {
	t.Helper()
	if err := os.WriteFile(f.exitPath, []byte(strconv.Itoa(code)), 0o644); err != nil {
		t.Fatalf("write exit code: %v", err)
	}
}

func (f *fakeCLI) calls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read calls: %v", err)
	}
	return string(data)
}

// waitForCalls waits until the recorded calls contain want.
func (f *fakeCLI) waitForCalls(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(f.calls(t), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in calls:\n%s", want, f.calls(t))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startAgent spawns a worker with the fake CLI and waits for its run ID.
func startAgent(t *testing.T, workspace string) (*AIAgent, *fakeCLI) {
	t.Helper()
	fake := newFakeCLI(t)
	ag, err := New(fake.bin, []string{"serve"}, workspace)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := ag.StartSession(context.Background(), agent.SessionConfig{Workspace: workspace}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	fake.waitForCalls(t, "serve args:")
	return ag, fake
}

// drain reads the single terminal event a turn produces.
func drain(t *testing.T, ag *AIAgent) string {
	t.Helper()
	select {
	case ev, ok := <-ag.Events():
		if !ok {
			t.Fatal("events channel closed before an event arrived")
		}
		if ev.Type == agent.EventError {
			if msg, _ := ev.Data["error"].(string); msg != "" {
				t.Logf("error event: %s", msg)
			}
		}
		return ev.Type
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for an agent event")
		return ""
	}
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// TestTurnEndEmitsAgentEndAndStopsWorker covers the happy path: the run ID is
// discovered via --id-file, the turn is awaited synchronously, exit 0 maps to
// EventAgentEnd, and the worker is torn down afterwards.
func TestTurnEndEmitsAgentEndAndStopsWorker(t *testing.T) {
	workspace := t.TempDir()
	ag, fake := startAgent(t, workspace)

	// PID comes from the worker's run.json, which is what the scheduler stores
	// for orphan reaping after a restart.
	pid := ag.PID()
	if pid <= 0 {
		t.Fatalf("PID() = %d, want the worker pid", pid)
	}
	if !alive(pid) {
		t.Fatalf("worker pid %d is not alive", pid)
	}

	if err := ag.SendPrompt(context.Background(), "fix the flaky test"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if got := drain(t, ag); got != agent.EventAgentEnd {
		t.Fatalf("event = %q, want %q", got, agent.EventAgentEnd)
	}

	// The prompt goes in on stdin (ARG_MAX safe) and the wait is uncapped, so
	// a long turn is never cut short by the CLI's 4m default.
	calls := fake.calls(t)
	if !strings.Contains(calls, "--wait") || !strings.Contains(calls, "--summary") {
		t.Errorf("missing --wait/--summary in:\n%s", calls)
	}
	if !strings.Contains(calls, "--timeout 0") {
		t.Errorf("send must default to an uncapped wait, got:\n%s", calls)
	}
	if !strings.Contains(calls, "send stdin: fix the flaky test") {
		t.Errorf("prompt was not piped on stdin:\n%s", calls)
	}
	if !strings.Contains(calls, "--id-file") {
		t.Errorf("serve must be started with --id-file:\n%s", calls)
	}
	// The task identifier is the workspace basename.
	if !strings.Contains(calls, "--name "+filepath.Base(workspace)) {
		t.Errorf("serve must be named after the task:\n%s", calls)
	}

	// Symphony sends exactly one prompt per agent, so the worker must be
	// stopped once the turn ends instead of leaking a detached `ai serve`.
	if err := ag.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !waitGone(t, pid) {
		t.Errorf("worker pid %d still alive after Stop", pid)
	}
	fake.waitForCalls(t, "kill args: --id")
}

// TestTimeoutEmitsErrorAndLeavesWorkerRunning is the GOTCHA 1 regression: a
// timed-out turn is an error, and the worker must survive it.
func TestTimeoutEmitsErrorAndLeavesWorkerRunning(t *testing.T) {
	workspace := t.TempDir()
	ag, fake := startAgent(t, workspace)
	pid := ag.PID()
	fake.setExit(t, exitCodeTimeout)

	if err := ag.SendPrompt(context.Background(), "long running turn"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if got := drain(t, ag); got != agent.EventError {
		t.Fatalf("event = %q, want %q (a timeout must not look like a completion)",
			got, agent.EventError)
	}
	if !alive(pid) {
		t.Error("worker was killed on timeout; its turn is still in flight")
	}

	if err := ag.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !waitGone(t, pid) {
		t.Errorf("worker pid %d still alive after Stop", pid)
	}
}

// TestSendFailureEmitsError is the GOTCHA 2 regression: a dead worker surfaces
// as a failing `ai send`, i.e. EventError, never EventAgentEnd.
func TestSendFailureEmitsError(t *testing.T) {
	workspace := t.TempDir()
	ag, fake := startAgent(t, workspace)
	pid := ag.PID()
	fake.setExit(t, 1)

	if err := ag.SendPrompt(context.Background(), "prompt"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	select {
	case ev := <-ag.Events():
		if ev.Type != agent.EventError {
			t.Fatalf("event = %q, want %q", ev.Type, agent.EventError)
		}
		if msg, _ := ev.Data["error"].(string); !strings.Contains(msg, "exit 1") {
			t.Errorf("error event should carry the exit code, got %q", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for an agent event")
	}
	if ag.PID() <= 0 {
		t.Error("PID() should still resolve after a failed turn")
	}

	if err := ag.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !waitGone(t, pid) {
		t.Errorf("worker pid %d still alive after Stop", pid)
	}
}

// TestSendPromptHonoursContextDeadline checks that a caller deadline caps the
// wait instead of the CLI's 4m default.
func TestSendPromptHonoursContextDeadline(t *testing.T) {
	workspace := t.TempDir()
	ag, fake := startAgent(t, workspace)
	defer ag.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := ag.SendPrompt(ctx, "prompt"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if got := drain(t, ag); got != agent.EventAgentEnd {
		t.Fatalf("event = %q, want %q", got, agent.EventAgentEnd)
	}

	fake.waitForCalls(t, "send args:")
	timeout := sendTimeout(t, fake.calls(t))
	if timeout <= 80*time.Second || timeout > 90*time.Second {
		t.Fatalf("--timeout = %s, want just under the 90s deadline", timeout)
	}
}

// TestSendPromptRejectsExpiredContext avoids handing `ai send` a nonsensical
// timeout when the caller's deadline has already passed.
func TestSendPromptRejectsExpiredContext(t *testing.T) {
	workspace := t.TempDir()
	ag, fake := startAgent(t, workspace)
	defer ag.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := ag.SendPrompt(ctx, "prompt"); err == nil {
		t.Fatal("SendPrompt with a dead context should fail")
	}
	if calls := fake.calls(t); strings.Contains(calls, "send args:") {
		t.Errorf("ai send must not run with an expired context:\n%s", calls)
	}
}

// TestSendCommandForwardsToPrompt covers the deleted Command abstraction: ACP
// has no command message, so a slash command is just prompt text.
func TestSendCommandForwardsToPrompt(t *testing.T) {
	workspace := t.TempDir()
	ag, fake := startAgent(t, workspace)
	defer ag.Stop()

	if err := ag.SendCommand(context.Background(), agent.Command{Type: "/review"}); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if got := drain(t, ag); got != agent.EventAgentEnd {
		t.Fatalf("event = %q, want %q", got, agent.EventAgentEnd)
	}
	if !strings.Contains(fake.calls(t), "send stdin: /review") {
		t.Errorf("command was not forwarded as prompt text:\n%s", fake.calls(t))
	}
}

// TestStartSessionFailsFastWhenWorkerDies guards the id-file handshake: a
// worker that dies during startup must fail immediately, not park the task in
// "running" until the 60s timeout.
func TestStartSessionFailsFastWhenWorkerDies(t *testing.T) {
	workspace := t.TempDir()
	fake := newFakeCLI(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "fake-ai-fail-start"), nil, 0o644); err != nil {
		t.Fatalf("write fail marker: %v", err)
	}

	ag, err := New(fake.bin, []string{"serve"}, workspace)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	if err := ag.StartSession(context.Background(), agent.SessionConfig{Workspace: workspace}); err == nil {
		t.Fatal("StartSession should fail when the worker dies during startup")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("StartSession took %s to fail; it must not wait for the full timeout", elapsed)
	}
}

// TestStopIsIdempotent: the scheduler may stop an agent that already ended.
func TestStopIsIdempotent(t *testing.T) {
	workspace := t.TempDir()
	ag, _ := startAgent(t, workspace)
	pid := ag.PID()

	if err := ag.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := ag.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if !waitGone(t, pid) {
		t.Errorf("worker pid %d still alive after Stop", pid)
	}

	select {
	case _, ok := <-ag.Events():
		if ok {
			t.Error("events channel should be closed after Stop")
		}
	case <-time.After(2 * time.Second):
		t.Error("events channel was not closed after Stop")
	}
}

// TestWorkerStopsItselfAfterTurnEnds: Symphony runs one prompt per agent and
// hands the agent straight to Agent.Stop-less teardown, so the worker must not
// leak a detached `ai serve` process when the turn ends.
func TestWorkerStopsItselfAfterTurnEnds(t *testing.T) {
	workspace := t.TempDir()
	ag, fake := startAgent(t, workspace)
	pid := ag.PID()

	if err := ag.SendPrompt(context.Background(), "prompt"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if got := drain(t, ag); got != agent.EventAgentEnd {
		t.Fatalf("event = %q, want %q", got, agent.EventAgentEnd)
	}

	if !waitGone(t, pid) {
		t.Errorf("worker pid %d leaked after the turn ended (no Stop() was called)", pid)
	}
	fake.waitForCalls(t, "kill args: --id")

	// The scheduler's global shutdown still calls Stop() afterwards.
	if err := ag.Stop(); err != nil {
		t.Fatalf("Stop after self-stop: %v", err)
	}
}

// TestPIDPrefersRunJSONOverSpawnedProcess: after a Symphony restart the spawned
// handle is gone but a surviving worker is still reapable, so the pid recorded
// by `ai serve` wins over the (possibly stale) local one.
func TestPIDPrefersRunJSONOverSpawnedProcess(t *testing.T) {
	workspace := t.TempDir()
	ag, _ := startAgent(t, workspace)
	defer ag.Stop()

	if ag.cmd == nil || ag.cmd.Process == nil {
		t.Fatal("expected a spawned worker")
	}
	spawned := ag.cmd.Process.Pid

	const recorded = 999999
	runJSON := filepath.Join(os.Getenv("HOME"), ".ai", "runs", ag.runID, "run.json")
	if err := os.WriteFile(runJSON, []byte(`{"id":"`+ag.runID+`","pid":`+strconv.Itoa(recorded)+`}`), 0o644); err != nil {
		t.Fatalf("rewrite run.json: %v", err)
	}

	if got := ag.PID(); got != recorded {
		t.Errorf("PID() = %d, want the run.json pid %d (spawned was %d)", got, recorded, spawned)
	}
	if got := pidFromRunMeta("does-not-exist"); got != 0 {
		t.Errorf("pidFromRunMeta(unknown) = %d, want 0", got)
	}
}

// sendTimeout extracts the value passed to `ai send --timeout`.
func sendTimeout(t *testing.T, calls string) time.Duration {
	t.Helper()
	for _, line := range strings.Split(calls, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "--timeout" && i+1 < len(fields) {
				d, err := time.ParseDuration(fields[i+1])
				if err != nil {
					t.Fatalf("bad --timeout value %q: %v", fields[i+1], err)
				}
				return d
			}
		}
	}
	t.Fatalf("no --timeout flag in calls:\n%s", calls)
	return 0
}

// waitGone polls until pid is no longer alive.
func waitGone(t *testing.T, pid int) bool {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if !alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
