// Package store provides SQLite-based task persistence.
package store

import "time"

// Task states.
const (
	StateInbox                 = "inbox"
	StateTodo                  = "todo"
	StateRunning               = "running"
	StateRunningReview        = "running-review"
	StateRunningAddressComment = "running-address-comment"
	StateSelfReview            = "self-review"
	StateAddressComment        = "address-comment"
	StateDone                  = "done"
	StateArchive               = "archive"
	StateFailed                = "failed"
)

// Task represents a work item to be processed by an agent.
type Task struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Workspace   string     `json:"workspace,omitempty"`
	AgentPID    int        `json:"agent_pid,omitempty"`
	RetryCount  int        `json:"retry_count"`
	LastError   string     `json:"last_error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	PRNumber    int        `json:"pr_number,omitempty"`
	PRURL       string     `json:"pr_url,omitempty"`
	PRState     string     `json:"pr_state,omitempty"`
}

// RetryEntry represents a scheduled retry for a failed task.
type RetryEntry struct {
	TaskID      string    `json:"task_id"`
	NextRetryAt time.Time `json:"next_retry_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// TaskCreate is the payload for creating a new task.
type TaskCreate struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// TaskUpdate is the payload for updating a task.
type TaskUpdate struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	State       *string `json:"state,omitempty"`
}