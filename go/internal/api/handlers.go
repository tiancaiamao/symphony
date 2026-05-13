// Package api provides HTTP API handlers.
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"symphonia/internal/scheduler"
	"symphonia/internal/store"
)

// Handlers holds the API handlers.
type Handlers struct {
	db        *sql.DB
	scheduler *scheduler.Scheduler
}

// NewHandlers creates a new Handlers instance.
func NewHandlers(db *sql.DB, sched *scheduler.Scheduler) *Handlers {
	return &Handlers{
		db:        db,
		scheduler: sched,
	}
}

// GetTasks handles GET /api/tasks - list all tasks.
func (h *Handlers) GetTasks(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")

	var tasks []store.Task
	var err error

	if state != "" {
		tasks, err = store.ListTasksByState(h.db, state)
	} else {
		tasks, err = store.ListTasks(h.db)
	}

	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to list tasks: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"tasks": tasks,
		"count": len(tasks),
	})
}

// CreateTask handles POST /api/tasks - create a new task.
func (h *Handlers) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req store.TaskCreate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if req.Title == "" {
		respondError(w, http.StatusBadRequest, "Title is required")
		return
	}

	task := &store.Task{
		ID:          uuid.New().String(),
		Title:       req.Title,
		Description: req.Description,
		// State defaults to StateInbox in store.CreateTask
	}

	if err := store.CreateTask(h.db, task); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to create task: "+err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, task)
}

// GetTask handles GET /api/tasks/{id} - get a single task.
func (h *Handlers) GetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondError(w, http.StatusBadRequest, "Task ID is required")
		return
	}

	task, err := store.GetTask(h.db, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to get task: "+err.Error())
		return
	}

	if task == nil {
		respondError(w, http.StatusNotFound, "Task not found")
		return
	}

	respondJSON(w, http.StatusOK, task)
}

// UpdateTask handles PUT /api/tasks/{id} - update a task.
func (h *Handlers) UpdateTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondError(w, http.StatusBadRequest, "Task ID is required")
		return
	}

	var req store.TaskUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// Get existing task
	task, err := store.GetTask(h.db, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to get task: "+err.Error())
		return
	}

	if task == nil {
		respondError(w, http.StatusNotFound, "Task not found")
		return
	}

	// Update fields
	title := task.Title
	description := task.Description

	if req.Title != nil {
		title = *req.Title
	}
	if req.Description != nil {
		description = *req.Description
	}
	
	// Update basic fields
	if err := store.UpdateTask(h.db, id, title, description); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to update task: "+err.Error())
		return
	}
	
	// Update state if provided
	if req.State != nil && *req.State != task.State {
		if err := store.UpdateTaskState(h.db, id, *req.State, ""); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to update task state: "+err.Error())
			return
		}
	}

	// Return updated task
	task, _ = store.GetTask(h.db, id)
	respondJSON(w, http.StatusOK, task)
}

// DeleteTask handles DELETE /api/tasks/{id} - delete a task.
func (h *Handlers) DeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondError(w, http.StatusBadRequest, "Task ID is required")
		return
	}

	// Get task before deletion to check for workspace cleanup
	task, err := store.GetTask(h.db, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to get task: "+err.Error())
		return
	}
	if task == nil {
		respondError(w, http.StatusNotFound, "Task not found")
		return
	}

	// Clean up workspace if it exists
	if task.Workspace != "" {
		// Run before_remove hook to clean up git worktree
		if h.scheduler != nil {
			if err := h.scheduler.CleanupWorkspace(r.Context(), task.Workspace, task.ID, task.ID); err != nil {
				respondError(w, http.StatusInternalServerError, "Failed to cleanup workspace: "+err.Error())
				return
			}
		}
	}

	// Delete task from database
	if err := store.DeleteTask(h.db, id); err != nil {
		respondError(w, http.StatusNotFound, "Task not found: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"message": "Task deleted",
		"id":      id,
	})
}

// RetryTask handles POST /api/tasks/{id}/retry - manually retry a failed task.
func (h *Handlers) RetryTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondError(w, http.StatusBadRequest, "Task ID is required")
		return
	}

	if err := h.scheduler.RetryTask(r.Context(), id); err != nil {
		respondError(w, http.StatusBadRequest, "Failed to retry task: "+err.Error())
		return
	}

	// Return updated task
	task, _ := store.GetTask(h.db, id)
	respondJSON(w, http.StatusOK, task)
}

// GetStatus handles GET /api/status - get scheduler status.
func (h *Handlers) GetStatus(w http.ResponseWriter, r *http.Request) {
	runningTasks := h.scheduler.GetRunningTasks()

	type RunInfo struct {
		TaskID     string `json:"task_id"`
		Title      string `json:"title"`
		Workspace  string `json:"workspace"`
		StartTime  string `json:"start_time"`
		ElapsedMs  int64  `json:"elapsed_ms"`
	}

	runs := make([]RunInfo, 0, len(runningTasks))
	for _, rt := range runningTasks {
		runs = append(runs, RunInfo{
			TaskID:    rt.Task.ID,
			Title:     rt.Task.Title,
			Workspace: rt.Task.Workspace,
			StartTime: rt.StartTime.Format(time.RFC3339),
			ElapsedMs: time.Since(rt.StartTime).Milliseconds(),
		})
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"running_count": len(runningTasks),
		"running":       runs,
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
	})
}

// Health handles GET /health - health check.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// Helper functions

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}