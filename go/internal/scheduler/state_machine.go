// Package scheduler provides task scheduling and state management.
package scheduler

import (
	"fmt"

	"symphonia/internal/store"
)

// State transition rules.
var validTransitions = map[string][]string{
	store.StateTodo:    {store.StateRunning},
	store.StateRunning: {store.StateDone, store.StateFailed},
	store.StateDone:    {}, // Terminal state
	store.StateFailed:  {store.StateRunning}, // Allow retry
}

// CanTransition checks if a state transition is valid.
func CanTransition(from, to string) bool {
	allowed, exists := validTransitions[from]
	if !exists {
		return false
	}
	for _, state := range allowed {
		if state == to {
			return true
		}
	}
	return false
}

// Transition attempts to transition a task to a new state.
// Returns an error if the transition is invalid.
func Transition(task *store.Task, to string) error {
	if task == nil {
		return fmt.Errorf("nil task")
	}

	if !CanTransition(task.State, to) {
		return fmt.Errorf("invalid transition from %s to %s for task %s", task.State, to, task.ID)
	}

	task.State = to
	return nil
}

// IsTerminal returns true if the state is a terminal state (no further transitions).
func IsTerminal(state string) bool {
	allowed, exists := validTransitions[state]
	return exists && len(allowed) == 0
}

// CanRetry returns true if a failed task can be retried.
func CanRetry(task *store.Task, maxRetries int) bool {
	if task == nil || task.State != store.StateFailed {
		return false
	}
	return task.RetryCount < maxRetries
}

// GetValidTransitions returns all valid target states for a given state.
func GetValidTransitions(from string) []string {
	if allowed, exists := validTransitions[from]; exists {
		return allowed
	}
	return nil
}