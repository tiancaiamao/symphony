// Package agent provides a pluggable agent system.
package agent

import "context"

// Agent is the interface all agent implementations must satisfy.
type Agent interface {
	Name() string
	StartSession(ctx context.Context, cfg SessionConfig) error
	SendPrompt(ctx context.Context, prompt string) error
	SendCommand(ctx context.Context, cmd Command) error
	Events() <-chan Event
	Stop() error
}

// SessionConfig contains settings for an agent session.
type SessionConfig struct {
	Workspace string
	Env      map[string]string
	Model    string
	MaxTurns int
}

// Command represents agent control commands.
type Command struct {
	Type string
	Args map[string]interface{}
}

// Event type constants.
const (
	EventServerStart    = "server_start"
	EventAgentStart     = "agent_start"
	EventAgentEnd       = "agent_end"
	EventTurnStart      = "turn_start"
	EventTurnEnd        = "turn_end"
	EventMessageStart   = "message_start"
	EventMessageUpdate  = "message_update"
	EventMessageEnd     = "message_end"
	EventToolStart      = "tool_execution_start"
	EventToolEnd        = "tool_execution_end"
	EventUsage          = "usage"
	EventError          = "error"
)

// Event represents a structured agent event.
type Event struct {
	Type string
	Data map[string]interface{}
	Raw  []byte
}