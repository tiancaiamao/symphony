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

	// Debug log the command being executed
	fmt.Printf("[DEBUG] Starting agent: %s %v\n", a.cmd.Path, a.cmd.Args)
	fmt.Printf("[DEBUG] Working directory: %s\n", a.cmd.Dir)

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
	}

	if err := a.cmd.Start(); err != nil {
		return fmt.Errorf("start process: %w", err)
	}

	fmt.Printf("[DEBUG] Agent started successfully (PID: %d)\n", a.cmd.Process.Pid)
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
	_, err = a.stdin.Write(append(data, '\n'))
	return err
}

func (a *AIAgent) readStdout() {
	dec := json.NewDecoder(a.stdout)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			// Send agent_end event when stdout closes or decoding fails
			a.events <- agent.Event{Type: agent.EventAgentEnd, Data: nil}
			return
		}
		var base struct{ Type string }
		if json.Unmarshal(raw, &base) != nil {
			continue
		}
		var data map[string]interface{}
		json.Unmarshal(raw, &data)
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
		// Log stderr for debugging
		fmt.Printf("[AGENT STDERR] %s\n", sc.Text())
	}
}