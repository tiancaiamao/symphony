# Implementation Plan: Symphony-Go Service

## Tech Stack

- **Language**: Go 1.21+
- **Database**: SQLite 3 (via `modernc.org/sqlite` or `github.com/mattn/go-sqlite3`)
- **Agent**: `~/project/ai` (RPC mode via stdin/stdout)
- **Web UI**: Embedded Go HTTP server + vanilla JS
- **Config**: YAML (`gopkg.in/yaml.v3`)

## Project Structure

```
~/project/symphony/
├── cmd/
│   └── symphony/
│       └── main.go              # Entry point
├── internal/
│   ├── agent/
│   │   ├── runner.go            # Agent execution (ai RPC)
│   │   └── protocol.go          # RPC types (PromptRequest, Event parsing)
│   ├── scheduler/
│   │   ├── scheduler.go         # Poll loop + reconcile
│   │   └── state_machine.go     # Task state transitions
│   ├── store/
│   │   ├── db.go                # SQLite operations
│   │   └── models.go            # Task struct, queries
│   ├── workspace/
│   │   └── manager.go           # Workspace create/cleanup
│   └── api/
│       ├── server.go            # HTTP server + routes
│       └── handlers.go          # REST endpoints
├── web/
│   ├── index.html               # Kanban UI
│   ├── style.css
│   └── app.js
├── config.yaml                  # Default config
├── go.mod
└── go.sum
```

## Phase 1: Core Service (MVP)

### 1.1 Database Setup
- **File**: `internal/store/db.go`
- **Schema** (from spec.md):
  ```sql
  CREATE TABLE IF NOT EXISTS tasks (
      id TEXT PRIMARY KEY,
      title TEXT NOT NULL,
      description TEXT,
      state TEXT NOT NULL,
      created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
      workspace TEXT,
      agent_pid INTEGER,
      retry_count INTEGER DEFAULT 0,
      last_error TEXT
  );

  CREATE TABLE IF NOT EXISTS retry_queue (
      task_id TEXT PRIMARY KEY,
      next_retry_at TIMESTAMP,
      FOREIGN KEY (task_id) REFERENCES tasks(id)
  );
  ```
- **Functions**:
  - `InitDB(path string) (*sql.DB, error)`
  - `CreateTask(db *sql.DB, task *Task) error`
  - `GetTask(db *sql.DB, id string) (*Task, error)`
  - `ListTasks(db *sql.DB) ([]Task, error)`
  - `UpdateTaskState(db *sql.DB, id string, state string, errorMsg string) error`

### 1.2 Agent Runner (RPC Integration)
- **File**: `internal/agent/protocol.go`
- **Types** (from ai codebase):
  ```go
  type PromptRequest struct {
      Type    string `json:"type"`
      Message string `json:"message"`
  }

  type RPCResponse struct {
      Type    string          `json:"type"`
      Content json.RawMessage `json:"content,omitempty"`
      Error   string          `json:"error,omitempty"`
  }
  ```
- **File**: `internal/agent/runner.go`
  - `StartAgent(task *Task, workspace string) (*Process, error)`
  - Send `PromptRequest{Type: "prompt", Message: task.Description}`
  - Stream `RPCResponse` events (agent_output, agent_exit, agent_error)
  - Handle `agent_exit` → mark done/failed

### 1.3 Workspace Manager
- **File**: `internal/workspace/manager.go`
- **Functions**:
  - `CreateWorkspace(root string, taskID string) (string, error)`
  - `RemoveWorkspace(workspace string) error`
- **Directory**: `~/.symphony/workspaces/<task-id>/`

### 1.4 Scheduler + State Machine
- **File**: `internal/scheduler/scheduler.go`
  - Poll loop (30s interval)
  - Call `reconcile()` each tick
- **File**: `internal/scheduler/state_machine.go`
  - `todo → running`: If `canStart()`, call `agent.StartAgent()`, set `state=running`
  - `running → done/failed`: On `agent_exit`, update state
  - `failed → retry`: Add to retry queue with exponential backoff
  - `done → cleanup`: Remove workspace

### 1.5 Configuration
- **File**: `config.yaml`
  ```yaml
  database:
    path: ~/.symphony/symphony.db
  workspace:
    root: ~/.symphony/workspaces
  agent:
    path: ~/project/ai
    max_concurrent: 3
    timeout: 30m
  retry:
    max_retries: 3
    base_delay: 10s
    max_delay: 5m
  server:
    port: 8080
  ```
- **Load**: `gopkg.in/yaml.v3`

### 1.6 Main Entry
- **File**: `cmd/symphony/main.go`
  - Load config
  - Init DB
  - Start scheduler
  - (Phase 2) Start HTTP server

## Phase 2: Web UI

### 2.1 HTTP Server
- **File**: `internal/api/server.go`
  - `http.ListenAndServe(:8080, router)`
  - Serve static files from `web/`
  - REST endpoints

### 2.2 REST API Endpoints
- **File**: `internal/api/handlers.go`
  - `GET /api/tasks` → `store.ListTasks()`
  - `POST /api/tasks` → `store.CreateTask()`
  - `GET /api/tasks/:id` → `store.GetTask()`
  - `PUT /api/tasks/:id` → Update title/description
  - `POST /api/tasks/:id/retry` → Manual retry (reset retry_count)
  - `GET /health` → `{status: "ok"}`

### 2.3 Kanban Frontend
- **File**: `web/index.html`
  - 3 columns: Todo, Running, Done
  - Task cards: title, status, created time
  - Modal for create/edit
- **File**: `web/app.js`
  - Fetch `/api/tasks` every 5s
  - Render Kanban
  - POST/PUT via fetch API

## Phase 3: Polish

- Graceful shutdown (stop agents, persist state)
- Metrics (optional Prometheus)
- Error handling improvements
- Documentation (README)

## RPC Protocol Details

From `~/project/ai/pkg/rpc/types.go`:
```go
type PromptRequest struct {
    Type    string `json:"type"`    // "prompt"
    Message string `json:"message"` // Task description
}

type RPCResponse struct {
    Type    string          `json:"type"`    // "agent_output", "agent_exit", "agent_error"
    Content json.RawMessage `json:"content,omitempty"`
    Error   string          `json:"error,omitempty"`
}
```

## Dependencies

```go
require (
    modernc.org/sqlite v1.29.1
    gopkg.in/yaml.v3 v3.0.1
)
```

## Testing Strategy

1. **Unit Tests**: Store, State Machine
2. **Integration**: Agent Runner (mock ai binary)
3. **E2E**: Create task → agent runs → done

## Success Criteria

- [ ] Can create task via API
- [ ] Agent runs task in workspace
- [ ] State transitions work (todo→running→done)
- [ ] Retry logic works
- [ ] Kanban UI shows tasks