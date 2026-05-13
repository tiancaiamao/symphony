// Package factory provides agent factory functions.
package factory

import (
	"errors"
	"fmt"

	"symphonia/internal/agent"
	"symphonia/internal/agent/ai"
)

var ErrUnknownAgentKind = errors.New("unknown agent kind")

// NewAgent creates an agent of the specified kind.
func NewAgent(kind, command string, args []string, workspace string) (agent.Agent, error) {
	switch kind {
	case "ai":
		return ai.New(command, args, workspace)
	case "codex":
		return ai.New(command, args, workspace) // codex uses same protocol
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownAgentKind, kind)
	}
}