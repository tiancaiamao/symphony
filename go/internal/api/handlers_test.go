package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"symphonia/internal/scheduler"
	"symphonia/internal/store"
)

func TestMain(m *testing.M) {
	// Run tests
	os.Exit(m.Run())
}

// setupTestAPI creates a test database and API handlers
func setupTestAPI(t *testing.T) (*sql.DB, *Handlers, func()) {
	t.Helper()

	// Create temp database
	tmpFile, err := os.CreateTemp("", "symphony-api-test-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	db, err := store.InitDB(tmpPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	// Create a scheduler (can be nil for basic tests)
	sched := &scheduler.Scheduler{}
	handlers := NewHandlers(db, sched)

	cleanup := func() {
		db.Close()
		os.Remove(tmpPath)
	}

	return db, handlers, cleanup
}

// createTestTask is a helper to create a task for testing
func createTestTask(t *testing.T, db *sql.DB, id, title, description, state string) *store.Task {
	t.Helper()
	task := &store.Task{
		ID:          id,
		Title:       title,
		Description: description,
		State:       state,
	}
	if err := store.CreateTask(db, task); err != nil {
		t.Fatalf("Failed to create test task: %v", err)
	}
	return task
}

func TestUpdateTask_PutEndpoint(t *testing.T) {
	db, handlers, cleanup := setupTestAPI(t)
	defer cleanup()

	// Create a test task
	taskID := "test-update-1"
	createTestTask(t, db, taskID, "Original Title", "Original Description", store.StateTodo)

	tests := []struct {
		name           string
		taskID         string
		payload        store.TaskUpdate
		wantStatus     int
		wantTitle      string
		wantDesc       string
		wantState      string
		checkResponse  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name:       "update title only",
			taskID:     taskID,
			payload:    store.TaskUpdate{Title: strPtr("New Title")},
			wantStatus: http.StatusOK,
			wantTitle:  "New Title",
			wantDesc:   "Original Description",
			wantState:  store.StateTodo,
		},
		{
			name:       "update description only",
			taskID:     taskID,
			payload:    store.TaskUpdate{Description: strPtr("New Description")},
			wantStatus: http.StatusOK,
			wantTitle:  "New Title", // From previous test
			wantDesc:   "New Description",
			wantState:  store.StateTodo,
		},
		{
			name:       "update both title and description",
			taskID:     taskID,
			payload:    store.TaskUpdate{Title: strPtr("Updated Title"), Description: strPtr("Updated Description")},
			wantStatus: http.StatusOK,
			wantTitle:  "Updated Title",
			wantDesc:   "Updated Description",
			wantState:  store.StateTodo,
		},
		{
			name:       "update state to running",
			taskID:     taskID,
			payload:    store.TaskUpdate{State: strPtr(store.StateRunning)},
			wantStatus: http.StatusOK,
			wantTitle:  "Updated Title",
			wantDesc:   "Updated Description",
			wantState:  store.StateRunning,
			checkResponse: func(t *testing.T, resp map[string]interface{}) {
				if startedAt, ok := resp["started_at"]; ok {
					if startedAt == nil {
						t.Error("started_at should be set when state changes to running")
					}
				}
			},
		},
		{
			name:       "update state to done",
			taskID:     taskID,
			payload:    store.TaskUpdate{State: strPtr(store.StateDone)},
			wantStatus: http.StatusOK,
			wantTitle:  "Updated Title",
			wantDesc:   "Updated Description",
			wantState:  store.StateDone,
			checkResponse: func(t *testing.T, resp map[string]interface{}) {
				if completedAt, ok := resp["completed_at"]; ok {
					if completedAt == nil {
						t.Error("completed_at should be set when state changes to done")
					}
				}
			},
		},
		{
			name:       "update all fields at once",
			taskID:     taskID,
			payload:    store.TaskUpdate{Title: strPtr("All Updated"), Description: strPtr("All Desc Updated"), State: strPtr(store.StateTodo)},
			wantStatus: http.StatusOK,
			wantTitle:  "All Updated",
			wantDesc:   "All Desc Updated",
			wantState:  store.StateTodo,
		},
		{
			name:       "non-existent task",
			taskID:     "does-not-exist",
			payload:    store.TaskUpdate{Title: strPtr("New Title")},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "empty task ID",
			taskID:     "",
			payload:    store.TaskUpdate{Title: strPtr("New Title")},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request body
			body, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("Failed to marshal payload: %v", err)
			}

			// Create request
			req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+tt.taskID, bytes.NewReader(body))
			req.SetPathValue("id", tt.taskID)
			rec := httptest.NewRecorder()

			// Call handler
			handlers.UpdateTask(rec, req)

			// Check status
			if rec.Code != tt.wantStatus {
				t.Errorf("UpdateTask() status = %d, want %d, body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}

			// For successful updates, verify response
			if tt.wantStatus == http.StatusOK {
				var resp map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("Failed to unmarshal response: %v", err)
				}

				if title, ok := resp["title"].(string); ok && title != tt.wantTitle {
					t.Errorf("Title = %q, want %q", title, tt.wantTitle)
				}

				if desc, ok := resp["description"].(string); ok && desc != tt.wantDesc {
					t.Errorf("Description = %q, want %q", desc, tt.wantDesc)
				}

				if state, ok := resp["state"].(string); ok && state != tt.wantState {
					t.Errorf("State = %q, want %q", state, tt.wantState)
				}

				// Run custom checks
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}
		})
	}
}

func TestUpdateTask_InvalidJSON(t *testing.T) {
	_, handlers, cleanup := setupTestAPI(t)
	defer cleanup()

	taskID := "test-invalid-json"
	createTestTask(t, handlers.db, taskID, "Test", "Test", store.StateTodo)

	req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+taskID, bytes.NewReader([]byte("invalid json")))
	req.SetPathValue("id", taskID)
	rec := httptest.NewRecorder()

	handlers.UpdateTask(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("UpdateTask() status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestUpdateTask_EmptyUpdate(t *testing.T) {
	db, handlers, cleanup := setupTestAPI(t)
	defer cleanup()

	taskID := "test-empty-update"
	originalTitle := "Original Title"
	originalDesc := "Original Description"
	createTestTask(t, db, taskID, originalTitle, originalDesc, store.StateTodo)

	// Send empty update payload
	payload := store.TaskUpdate{}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+taskID, bytes.NewReader(body))
	req.SetPathValue("id", taskID)
	rec := httptest.NewRecorder()

	handlers.UpdateTask(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("UpdateTask() status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)

	// Verify nothing changed
	if resp["title"] != originalTitle {
		t.Errorf("Title should remain unchanged")
	}
	if resp["description"] != originalDesc {
		t.Errorf("Description should remain unchanged")
	}
}

// strPtr is a helper to get a pointer to a string
func strPtr(s string) *string {
	return &s
}