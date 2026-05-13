// Package ai provides the ai agent implementation.
package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"

	"symphonia/internal/agent"
)

// New creates a new ai agent instance.
func New(binaryPath string, args []string, workspace string) (*AIAgent, error) {
	cmd := exec.Command(binaryPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Set working directory during creation
	if workspace != "" {
		cmd.Dir = workspace
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	events := make(chan agent.Event, 100)
	done := make(chan struct{})

	ai := &AIAgent{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
		events: events,
		done:   done,
	}

	go ai.readStderr(stderr)
	return ai, nil
}

// AIAgent implements the agent.Agent interface using the ai CLI.
type AIAgent struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	events  chan agent.Event
	done    chan struct{}
	stopped bool
}

func (a *AIAgent) Name() string { return "ai" }

// PID returns the process ID of the running agent, or 0 if not running
func (a *AIAgent) PID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process.Pid
	}
	return 0
}

func (a *AIAgent) StartSession(ctx context.Context, cfg agent.SessionConfig) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Debug: log command details
	fmt.Printf("[AGENT] Starting: %s %v\n", a.cmd.Path, a.cmd.Args)
	fmt.Printf("[AGENT] Working directory: %s\n", a.cmd.Dir)

	// Set environment variables
	if len(cfg.Env) > 0 {
		env := a.cmd.Env
		if env == nil {
			env = []string{}
		}
		for k, v := range cfg.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		a.cmd.Env = env
		fmt.Printf("[AGENT] Environment variables set: %d\n", len(cfg.Env))
	}

	if err := a.cmd.Start(); err != nil {
		return fmt.Errorf("start process: %w", err)
	}

	pid := a.cmd.Process.Pid
	fmt.Printf("[AGENT] Started successfully (PID: %d)\n", pid)

	// Start a goroutine to monitor process exit
	go a.monitorProcess()

	go a.readStdout()
	return nil
}

func (a *AIAgent) SendPrompt(ctx context.Context, prompt string) error {
	return a.send(map[string]interface{}{"type": "prompt", "message": prompt})
}

func (a *AIAgent) SendCommand(ctx context.Context, cmd agent.Command) error {
	return a.send(map[string]interface{}{"type": cmd.Type})
}

func (a *AIAgent) Events() <-chan agent.Event { return a.events }

func (a *AIAgent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return nil
	}
	a.stopped = true
	close(a.done)
	close(a.events)
	if a.cmd.Process != nil {
		syscall.Kill(-a.cmd.Process.Pid, syscall.SIGTERM)
		a.cmd.Wait()
	}
	return nil
}

func (a *AIAgent) send(msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	fmt.Printf("[AGENT SEND] Sending message: %s\n", string(data))
	_, err = a.stdin.Write(append(data, '\n'))
	if err != nil {
		fmt.Printf("[AGENT SEND] Write error: %v\n", err)
	}
	return err
}

func (a *AIAgent) readStdout() {
	dec := json.NewDecoder(a.stdout)
	fmt.Printf("[AGENT STDOUT] Starting to read events\n")
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			// Send agent_end event when stdout closes or decoding fails
			fmt.Printf("[AGENT STDOUT] Stream closed or error: %v\n", err)
			select {
			case a.events <- agent.Event{Type: agent.EventAgentEnd, Data: nil}:
			case <-a.done:
				return
			}
			return
		}
		var base struct{ Type string }
		if json.Unmarshal(raw, &base) != nil {
			fmt.Printf("[AGENT STDOUT] Failed to parse event type: %s\n", string(raw))
			continue
		}
		var data map[string]interface{}
		json.Unmarshal(raw, &data)
		fmt.Printf("[AGENT STDOUT] Event: %s\n", base.Type)
		select {
		case a.events <- agent.Event{Type: base.Type, Data: data, Raw: raw}:
		case <-a.done:
			return
		}
	}
}

func (a *AIAgent) readStderr(stderr io.Reader) {
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		// Log stderr for debugging - this is crucial for troubleshooting
		line := sc.Text()
		fmt.Printf("[AGENT STDERR] %s\n", line)
	}
	if err := sc.Err(); err != nil {
		fmt.Printf("[AGENT STDERR] Read error: %v\n", err)
	}
}

// monitorProcess waits for the agent process to exit and logs the exit status
func (a *AIAgent) monitorProcess() {
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	pid := cmd.Process.Pid
	fmt.Printf("[AGENT MONITOR] Monitoring PID %d for exit\n", pid)

	// Wait for the process to exit
	err := cmd.Wait()

	fmt.Printf("[AGENT MONITOR] PID %d exited with error: %v\n", pid, err)

	if err != nil {
		// Process exited with an error
		fmt.Printf("[AGENT MONITOR] Exit error type: %T\n", err)
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Printf("[AGENT MONITOR] Exit code: %d\n", exitErr.ExitCode())
			fmt.Printf("[AGENT MONITOR] Stderr: %s\n", string(exitErr.Stderr))
		}
	}

	// Send agent_end event if not already stopped
	select {
	case <-a.done:
		fmt.Printf("[AGENT MONITOR] Agent already stopped\n")
	default:
		fmt.Printf("[AGENT MONITOR] Sending agent_end event due to process exit\n")
		select {
		case a.events <- agent.Event{Type: agent.EventAgentEnd, Data: nil}:
		case <-a.done:
		}
	}
}