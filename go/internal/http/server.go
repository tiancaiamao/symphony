// Package http provides the HTTP status server.
package http

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"symphonia/internal/api"
	"symphonia/internal/scheduler"
)

//go:embed web/*
var webFS embed.FS

// Server is the HTTP status server.
type Server struct {
	addr      string
	db        *sql.DB
	scheduler *scheduler.Scheduler
	mux       *http.ServeMux
	templates *template.Template
	handlers  *api.Handlers
	webFS     http.FileSystem
}

// New creates a new HTTP server.
func New(port int, db *sql.DB, sched *scheduler.Scheduler) *Server {
	// Create sub-filesystem for web files
	subFS, _ := fs.Sub(webFS, "web")

	s := &Server{
		addr:      fmt.Sprintf(":%d", port),
		db:        db,
		scheduler: sched,
		mux:       http.NewServeMux(),
		handlers:  api.NewHandlers(db, sched),
		webFS:     http.FS(subFS),
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// API routes
	s.mux.HandleFunc("GET /api/tasks", s.handlers.GetTasks)
	s.mux.HandleFunc("POST /api/tasks", s.handlers.CreateTask)
	s.mux.HandleFunc("GET /api/tasks/{id}", s.handlers.GetTask)
	s.mux.HandleFunc("PUT /api/tasks/{id}", s.handlers.UpdateTask)
	s.mux.HandleFunc("DELETE /api/tasks/{id}", s.handlers.DeleteTask)
	s.mux.HandleFunc("POST /api/tasks/{id}/retry", s.handlers.RetryTask)
	s.mux.HandleFunc("GET /api/status", s.handlers.GetStatus)
	s.mux.HandleFunc("GET /health", s.handlers.Health)

	// Static files - serve from web subdirectory with debug logging
	staticHandler := http.StripPrefix("/static", http.FileServer(s.webFS))
	s.mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("[DEBUG] Static request: %s\n", r.URL.Path)
		staticHandler.ServeHTTP(w, r)
	})

	// Kanban UI
	s.mux.HandleFunc("GET /", s.handleRoot)
	s.mux.HandleFunc("GET /board", s.handleBoard)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"service": "symphony",
		"version": "1.0.0",
		"endpoints": []string{
			"GET  /",
			"GET  /board - Kanban UI",
			"GET  /api/tasks",
			"POST /api/tasks",
			"GET  /api/tasks/{id}",
			"PUT  /api/tasks/{id}",
			"DELETE /api/tasks/{id}",
			"POST /api/tasks/{id}/retry",
			"GET  /api/status",
			"GET  /health",
		},
	})
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	// Serve the kanban HTML directly from webFS
	tmpl, err := template.ParseFS(webFS, "web/index.html")
	if err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title": "Symphony - Task Board",
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}
}

// Start begins serving HTTP requests.
func (s *Server) Start() error {
	return http.ListenAndServe(s.addr, s.mux)
}

// Stop gracefully stops the server.
func (s *Server) Stop() error {
	// TODO: implement graceful shutdown
	return nil
}

// respondJSON is a helper for JSON responses.
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}