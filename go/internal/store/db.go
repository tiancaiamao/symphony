// Package store provides SQLite-based task persistence.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// InitDB initializes the SQLite database with schema migrations.
func InitDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("enable WAL: %w", err)
	}

	// Create tasks table
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		description TEXT,
		state TEXT NOT NULL DEFAULT 'todo',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		workspace TEXT,
		agent_pid INTEGER,
		retry_count INTEGER DEFAULT 0,
		last_error TEXT,
		started_at TIMESTAMP,
		completed_at TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS retry_queue (
		task_id TEXT PRIMARY KEY,
		next_retry_at TIMESTAMP NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state);
	CREATE INDEX IF NOT EXISTS idx_retry_queue_next ON retry_queue(next_retry_at);
	`

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create schema: %w", err)
	}

	// Migration: Add PR fields if not exist (for existing databases)
	migrations := []string{
		`ALTER TABLE tasks ADD COLUMN pr_number INTEGER DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN pr_url TEXT`,
		`ALTER TABLE tasks ADD COLUMN pr_state TEXT`,
	}
	for _, m := range migrations {
		// Ignore errors if column already exists
		db.Exec(m)
	}

	return db, nil
}

// CreateTask inserts a new task into the database.
func CreateTask(db *sql.DB, task *Task) error {
	now := time.Now()
	task.CreatedAt = now
	task.UpdatedAt = now
	if task.State == "" {
		task.State = StateInbox
	}

	_, err := db.Exec(`
		INSERT INTO tasks (id, title, description, state, created_at, updated_at, workspace, agent_pid, retry_count, last_error, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, task.ID, task.Title, task.Description, task.State, task.CreatedAt, task.UpdatedAt, task.Workspace, task.AgentPID, task.RetryCount, task.LastError, task.StartedAt, task.CompletedAt)

	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}

	return nil
}

// GetTask retrieves a task by ID.
func GetTask(db *sql.DB, id string) (*Task, error) {
	task := &Task{}
	var startedAt, completedAt sql.NullTime
	var workspace, lastError, prURL, prState sql.NullString
	var agentPID, prNumber sql.NullInt64

	err := db.QueryRow(`
		SELECT id, title, description, state, created_at, updated_at, workspace, agent_pid, retry_count, last_error, started_at, completed_at, pr_number, pr_url, pr_state
		FROM tasks WHERE id = ?
	`, id).Scan(
		&task.ID, &task.Title, &task.Description, &task.State,
		&task.CreatedAt, &task.UpdatedAt,
		&workspace, &agentPID, &task.RetryCount, &lastError,
		&startedAt, &completedAt, &prNumber, &prURL, &prState,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query task: %w", err)
	}

	task.Workspace = workspace.String
	task.AgentPID = int(agentPID.Int64)
	task.LastError = lastError.String
	task.PRNumber = int(prNumber.Int64)
	task.PRURL = prURL.String
	task.PRState = prState.String
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}

	return task, nil
}

// ListTasks retrieves all tasks from the database.
func ListTasks(db *sql.DB) ([]Task, error) {
	rows, err := db.Query(`
		SELECT id, title, description, state, created_at, updated_at, workspace, agent_pid, retry_count, last_error, started_at, completed_at, pr_number, pr_url, pr_state
		FROM tasks ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		task := Task{}
		var startedAt, completedAt sql.NullTime
		var workspace, lastError, prURL, prState sql.NullString
		var agentPID, prNumber sql.NullInt64

		err := rows.Scan(
			&task.ID, &task.Title, &task.Description, &task.State,
			&task.CreatedAt, &task.UpdatedAt,
			&workspace, &agentPID, &task.RetryCount, &lastError,
			&startedAt, &completedAt, &prNumber, &prURL, &prState,
		)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}

		task.Workspace = workspace.String
		task.AgentPID = int(agentPID.Int64)
		task.LastError = lastError.String
		task.PRNumber = int(prNumber.Int64)
		task.PRURL = prURL.String
		task.PRState = prState.String
		if startedAt.Valid {
			task.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			task.CompletedAt = &completedAt.Time
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}

// ListTasksByState retrieves tasks filtered by state.
func ListTasksByState(db *sql.DB, state string) ([]Task, error) {
	rows, err := db.Query(`
		SELECT id, title, description, state, created_at, updated_at, workspace, agent_pid, retry_count, last_error, started_at, completed_at, pr_number, pr_url, pr_state
		FROM tasks WHERE state = ? ORDER BY created_at DESC
	`, state)
	if err != nil {
		return nil, fmt.Errorf("query tasks by state: %w", err)
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		task := Task{}
		var startedAt, completedAt sql.NullTime
		var workspace, lastError sql.NullString
		var agentPID sql.NullInt64

		err := rows.Scan(
			&task.ID, &task.Title, &task.Description, &task.State,
			&task.CreatedAt, &task.UpdatedAt,
			&workspace, &agentPID, &task.RetryCount, &lastError,
			&startedAt, &completedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}

		task.Workspace = workspace.String
		task.AgentPID = int(agentPID.Int64)
		task.LastError = lastError.String
		if startedAt.Valid {
			task.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			task.CompletedAt = &completedAt.Time
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}

// UpdateTaskState updates a task's state and related fields.
func UpdateTaskState(db *sql.DB, id string, state string, errorMsg string) error {
	now := time.Now()

	var startedAt, completedAt interface{}
	if state == StateRunning {
		startedAt = now
	} else {
		startedAt = nil
	}

	if state == StateDone || state == StateFailed {
		completedAt = now
	} else {
		completedAt = nil
	}

	result, err := db.Exec(`
		UPDATE tasks SET state = ?, updated_at = ?, last_error = ?, started_at = ?, completed_at = ? WHERE id = ?
	`, state, now, errorMsg, startedAt, completedAt, id)
	if err != nil {
		return fmt.Errorf("update task state: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("task not found: %s", id)
	}

	return nil
}

// UpdateTask updates a task's mutable fields.
func UpdateTask(db *sql.DB, id string, title string, description string) error {
	now := time.Now()

	result, err := db.Exec(`
		UPDATE tasks SET title = ?, description = ?, updated_at = ? WHERE id = ?
	`, title, description, now, id)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("task not found: %s", id)
	}

	return nil
}

// IncrementRetryCount increments the retry count for a task.
func IncrementRetryCount(db *sql.DB, id string) (int, error) {
	now := time.Now()

	var retryCount int
	err := db.QueryRow(`SELECT retry_count FROM tasks WHERE id = ?`, id).Scan(&retryCount)
	if err != nil {
		return 0, fmt.Errorf("get retry count: %w", err)
	}

	retryCount++
	_, err = db.Exec(`UPDATE tasks SET retry_count = ?, updated_at = ? WHERE id = ?`, retryCount, now, id)
	if err != nil {
		return 0, fmt.Errorf("update retry count: %w", err)
	}

	return retryCount, nil
}

// SetTaskWorkspace sets the workspace path for a task.
func SetTaskWorkspace(db *sql.DB, id string, workspace string) error {
	_, err := db.Exec(`UPDATE tasks SET workspace = ?, updated_at = ? WHERE id = ?`, workspace, time.Now(), id)
	if err != nil {
		return fmt.Errorf("set task workspace: %w", err)
	}
	return nil
}

// SetTaskPID sets the agent PID for a task.
func SetTaskPID(db *sql.DB, id string, pid int) error {
	_, err := db.Exec(`UPDATE tasks SET agent_pid = ?, updated_at = ? WHERE id = ?`, pid, time.Now(), id)
	if err != nil {
		return fmt.Errorf("set task pid: %w", err)
	}
	return nil
}

// DeleteTask removes a task from the database.
func DeleteTask(db *sql.DB, id string) error {
	// First delete from retry_queue if exists
	_, _ = db.Exec(`DELETE FROM retry_queue WHERE task_id = ?`, id)

	result, err := db.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("task not found: %s", id)
	}

	return nil
}

// --- Retry Queue Operations ---

// AddToRetryQueue schedules a task for retry.
func AddToRetryQueue(db *sql.DB, taskID string, nextRetryAt time.Time) error {
	_, err := db.Exec(`
		INSERT OR REPLACE INTO retry_queue (task_id, next_retry_at, created_at)
		VALUES (?, ?, ?)
	`, taskID, nextRetryAt, time.Now())
	if err != nil {
		return fmt.Errorf("add to retry queue: %w", err)
	}
	return nil
}

// GetRetryableTasks returns tasks that are ready to be retried.
// GetRetryableTasks returns tasks that are ready to be retried.
func GetRetryableTasks(db *sql.DB) ([]Task, error) {
	rows, err := db.Query(`
		SELECT t.id, t.title, t.description, t.state, t.created_at, t.updated_at, t.workspace, t.agent_pid, t.retry_count, t.last_error, t.started_at, t.completed_at, t.pr_number, t.pr_url, t.pr_state
		FROM tasks t
		JOIN retry_queue r ON t.id = r.task_id
		WHERE r.next_retry_at <= ? AND t.state = ?
		ORDER BY r.next_retry_at ASC
	`, time.Now(), StateFailed)
	if err != nil {
		return nil, fmt.Errorf("query retryable tasks: %w", err)
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		task := Task{}
		var startedAt, completedAt sql.NullTime
		var workspace, lastError, prURL, prState sql.NullString
		var agentPID, prNumber sql.NullInt64

		err := rows.Scan(
			&task.ID, &task.Title, &task.Description, &task.State,
			&task.CreatedAt, &task.UpdatedAt,
			&workspace, &agentPID, &task.RetryCount, &lastError,
			&startedAt, &completedAt, &prNumber, &prURL, &prState,
		)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}

		task.Workspace = workspace.String
		task.AgentPID = int(agentPID.Int64)
		task.LastError = lastError.String
		task.PRNumber = int(prNumber.Int64)
		task.PRURL = prURL.String
		task.PRState = prState.String
		if startedAt.Valid {
			task.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			task.CompletedAt = &completedAt.Time
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}

// RemoveFromRetryQueue removes a task from the retry queue.
func RemoveFromRetryQueue(db *sql.DB, taskID string) error {
	_, err := db.Exec(`DELETE FROM retry_queue WHERE task_id = ?`, taskID)
	if err != nil {
		return fmt.Errorf("remove from retry queue: %w", err)
	}
	return nil
}
// --- PR Operations ---

// SetTaskPR sets the PR information for a task.
func SetTaskPR(db *sql.DB, id string, prNumber int, prURL string) error {
	_, err := db.Exec(`UPDATE tasks SET pr_number = ?, pr_url = ?, pr_state = 'open', updated_at = ? WHERE id = ?`, prNumber, prURL, time.Now(), id)
	if err != nil {
		return fmt.Errorf("set task PR: %w", err)
	}
	return nil
}

// SetTaskPRState sets the PR state for a task.
func SetTaskPRState(db *sql.DB, id string, prState string) error {
	_, err := db.Exec(`UPDATE tasks SET pr_state = ?, updated_at = ? WHERE id = ?`, prState, time.Now(), id)
	if err != nil {
		return fmt.Errorf("set task PR state: %w", err)
	}
	return nil
}
