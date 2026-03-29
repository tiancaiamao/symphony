package store

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

func TestInitDB(t *testing.T) {
	// Create temp file
	tmpFile, err := os.CreateTemp("", "symphony-test-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	db, err := InitDB(tmpPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	// Verify tables exist
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query tasks table: %v", err)
	}

	err = db.QueryRow("SELECT COUNT(*) FROM retry_queue").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query retry_queue table: %v", err)
	}
}

func TestCreateAndGetTask(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	task := &Task{
		ID:          "test-1",
		Title:       "Test Task",
		Description: "Test Description",
		State:       StateTodo,
	}

	err := CreateTask(db, task)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// Verify task was created
	got, err := GetTask(db, "test-1")
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	if got == nil {
		t.Fatal("Expected task, got nil")
	}

	if got.Title != task.Title {
		t.Errorf("Title mismatch: got %q, want %q", got.Title, task.Title)
	}

	if got.State != StateTodo {
		t.Errorf("State mismatch: got %q, want %q", got.State, StateTodo)
	}

	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestListTasks(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create multiple tasks
	for i := 1; i <= 3; i++ {
		task := &Task{
			ID:    string(rune('a' + i)),
			Title: "Task",
			State: StateTodo,
		}
		if err := CreateTask(db, task); err != nil {
			t.Fatalf("CreateTask failed: %v", err)
		}
	}

	tasks, err := ListTasks(db)
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}

	if len(tasks) != 3 {
		t.Errorf("Expected 3 tasks, got %d", len(tasks))
	}
}

func TestListTasksByState(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create tasks in different states
	states := []string{StateTodo, StateTodo, StateRunning, StateDone}
	for i, state := range states {
		task := &Task{
			ID:    string(rune('a' + i)),
			Title: "Task " + state,
			State: state,
		}
		if err := CreateTask(db, task); err != nil {
			t.Fatalf("CreateTask failed: %v", err)
		}
	}

	todoTasks, err := ListTasksByState(db, StateTodo)
	if err != nil {
		t.Fatalf("ListTasksByState failed: %v", err)
	}

	if len(todoTasks) != 2 {
		t.Errorf("Expected 2 todo tasks, got %d", len(todoTasks))
	}
}

func TestUpdateTaskState(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	task := &Task{
		ID:    "test-1",
		Title: "Test Task",
		State: StateTodo,
	}
	CreateTask(db, task)

	// Update to running
	err := UpdateTaskState(db, "test-1", StateRunning, "")
	if err != nil {
		t.Fatalf("UpdateTaskState failed: %v", err)
	}

	got, _ := GetTask(db, "test-1")
	if got.State != StateRunning {
		t.Errorf("State should be running, got %q", got.State)
	}

	if got.StartedAt == nil {
		t.Error("StartedAt should be set when state changes to running")
	}

	// Update to done
	err = UpdateTaskState(db, "test-1", StateDone, "")
	if err != nil {
		t.Fatalf("UpdateTaskState failed: %v", err)
	}

	got, _ = GetTask(db, "test-1")
	if got.CompletedAt == nil {
		t.Error("CompletedAt should be set when state changes to done")
	}
}

func TestUpdateTask(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	task := &Task{
		ID:          "test-1",
		Title:       "Original Title",
		Description: "Original Description",
		State:       StateTodo,
	}
	CreateTask(db, task)

	err := UpdateTask(db, "test-1", "New Title", "New Description")
	if err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}

	got, _ := GetTask(db, "test-1")
	if got.Title != "New Title" {
		t.Errorf("Title should be updated, got %q", got.Title)
	}
}

func TestDeleteTask(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	task := &Task{
		ID:    "test-1",
		Title: "Test Task",
		State: StateTodo,
	}
	CreateTask(db, task)

	err := DeleteTask(db, "test-1")
	if err != nil {
		t.Fatalf("DeleteTask failed: %v", err)
	}

	got, _ := GetTask(db, "test-1")
	if got != nil {
		t.Error("Task should be deleted")
	}
}

func TestRetryQueue(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	task := &Task{
		ID:          "test-1",
		Title:       "Test Task",
		State:       StateFailed,
		RetryCount:  1,
	}
	CreateTask(db, task)

	// Add to retry queue
	nextRetry := time.Now().Add(10 * time.Second)
	err := AddToRetryQueue(db, "test-1", nextRetry)
	if err != nil {
		t.Fatalf("AddToRetryQueue failed: %v", err)
	}

	// Get retryable tasks (should be empty since retry is in future)
	tasks, err := GetRetryableTasks(db)
	if err != nil {
		t.Fatalf("GetRetryableTasks failed: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("Expected 0 retryable tasks, got %d", len(tasks))
	}

	// Add task with past retry time
	pastRetry := time.Now().Add(-1 * time.Second)
	AddToRetryQueue(db, "test-1", pastRetry)

	tasks, err = GetRetryableTasks(db)
	if err != nil {
		t.Fatalf("GetRetryableTasks failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("Expected 1 retryable task, got %d", len(tasks))
	}

	// Remove from retry queue
	err = RemoveFromRetryQueue(db, "test-1")
	if err != nil {
		t.Fatalf("RemoveFromRetryQueue failed: %v", err)
	}

	tasks, _ = GetRetryableTasks(db)
	if len(tasks) != 0 {
		t.Errorf("Expected 0 retryable tasks after removal, got %d", len(tasks))
	}
}

func TestIncrementRetryCount(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	task := &Task{
		ID:         "test-1",
		Title:      "Test Task",
		State:      StateFailed,
		RetryCount: 0,
	}
	CreateTask(db, task)

	count, err := IncrementRetryCount(db, "test-1")
	if err != nil {
		t.Fatalf("IncrementRetryCount failed: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected retry count 1, got %d", count)
	}

	count, _ = IncrementRetryCount(db, "test-1")
	if count != 2 {
		t.Errorf("Expected retry count 2, got %d", count)
	}
}

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "symphony-test-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	db, err := InitDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	t.Cleanup(func() {
		os.Remove(tmpFile.Name())
	})

	return db
}