# Symphony-Go Implementation Tasks

## Project Setup
- [ ] Initialize Go module: `go mod init github.com/user/symphony`
- [ ] Create directory structure: `cmd/symphony`, `internal/{store,agent,scheduler,workspace,api}`, `web`
- [ ] Add dependencies to go.mod:
  - `modernc.org/sqlite v1.29.1`
  - `gopkg.in/yaml.v3 v3.0.1`

## Phase 1: Core Service

### 1.1 Database Layer
- [ ] Create `internal/store/models.go` - Define Task struct with fields:
  - ID, Title, Description, State, CreatedAt, UpdatedAt, Workspace, AgentPID, RetryCount, LastError
- [ ] Create `internal/store/db.go` - Implement InitDB with schema migration:
  - CREATE TABLE tasks (...)
  - CREATE TABLE retry_queue (...)
- [ ] Add CreateTask function in db.go
- [ ] Add GetTask function in db.go
- [ ] Add ListTasks function in db.go
- [ ] Add UpdateTaskState function in db.go
- [ ] Add DeleteTask function in db.go
- [ ] Write unit tests for store package

### 1.2 Agent Runner
- [ ] Create `internal/agent/protocol.go` - Define RPC types:
  - PromptRequest struct (Type, Message)
  - RPCResponse struct (Type, Content, Error)
- [ ] Create `internal/agent/runner.go` - Implement StartAgent:
  - Exec cmd: `~/project/ai/bin/ai --mode rpc --session <workspace>/.session.jsonl`
  - Set cmd.Dir = workspace
  - Get stdin/stdout pipes
- [ ] Implement SendPrompt function - Send PromptRequest JSON to stdin
- [ ] Implement StreamEvents function - Read RPCResponse from stdout line-by-line
- [ ] Handle agent_exit event - Return success/failure status
- [ ] Handle agent_output/agent_error events - Log output
- [ ] Add StopAgent function - Kill process by PID
- [ ] Write integration test with mock ai binary

### 1.3 Workspace Manager
- [ ] Create `internal/workspace/manager.go`
- [ ] Implement CreateWorkspace(root, taskID) - Create dir: `<root>/<taskID>/`
- [ ] Implement RemoveWorkspace(workspace) - Delete workspace dir
- [ ] Add workspace path validation (prevent directory traversal)

### 1.4 State Machine
- [ ] Create `internal/scheduler/state_machine.go`
- [ ] Define state constants: StateTodo, StateRunning, StateDone, StateFailed
- [ ] Implement CanTransition(from, to string) bool
- [ ] Implement Transition(task *Task, to string) - Update state, timestamp
- [ ] Add state transition rules:
  - todo → running (if canStart)
  - running → done (on agent exit success)
  - running → failed (on agent exit error or max retries)
  - failed → running (on retry)

### 1.5 Scheduler
- [ ] Create `internal/scheduler/scheduler.go`
- [ ] Define Scheduler struct with: DB, AgentRunner, WorkspaceManager, Config
- [ ] Implement Run(ctx) - Start ticker (30s), call reconcile on each tick
- [ ] Implement reconcile() - Core loop:
  - List all tasks from DB
  - For each task: handle based on state
- [ ] Implement handleTodoTask - Check concurrency, start agent if < max_concurrent
- [ ] Implement handleRunningTask - Check agent health (process alive)
- [ ] Implement handleFailedTask - Check retry queue, retry if delay elapsed
- [ ] Implement handleDoneTask - Cleanup workspace, remove from DB (optional)
- [ ] Add retry queue logic - Exponential backoff: base_delay * (2 ^ retry_count)
- [ ] Add graceful shutdown - Stop ticker, wait for running agents

### 1.6 Configuration
- [ ] Create `config.yaml` with default values (database, workspace, agent, retry, server)
- [ ] Create `internal/config/config.go` - Define Config struct
- [ ] Implement LoadConfig(path) - Parse YAML file
- [ ] Add environment variable expansion (e.g., `~/.symphony` → `/home/user/.symphony`)
- [ ] Add config validation (required fields, valid paths)

### 1.7 Main Entry Point
- [ ] Create `cmd/symphony/main.go`
- [ ] Load config from `~/.symphony/config.yaml` or `./config.yaml`
- [ ] Initialize database
- [ ] Create scheduler instance
- [ ] Start scheduler with context
- [ ] Handle OS signals (SIGINT, SIGTERM) for graceful shutdown

## Phase 2: Web UI

### 2.1 HTTP Server
- [ ] Create `internal/api/server.go`
- [ ] Implement NewServer(port) - Create http.Server
- [ ] Add routes: /, /api/tasks, /health, static files
- [ ] Serve static files from `web/` directory
- [ ] Start server in goroutine

### 2.2 REST API Handlers
- [ ] Create `internal/api/handlers.go`
- [ ] Implement GetTasks handler - GET /api/tasks → JSON array
- [ ] Implement CreateTask handler - POST /api/tasks (JSON body)
- [ ] Implement GetTask handler - GET /api/tasks/:id
- [ ] Implement UpdateTask handler - PUT /api/tasks/:id
- [ ] Implement RetryTask handler - POST /api/tasks/:id/retry
- [ ] Implement Health handler - GET /health → {"status":"ok"}
- [ ] Add error handling and HTTP status codes
- [ ] Add request validation (required fields, valid state values)

### 2.3 Kanban Frontend
- [ ] Create `web/index.html` - Basic HTML structure:
  - Header with title
  - Kanban board: 3 columns (Todo, Running, Done)
  - Task card template
  - Create task button
  - Modal for task details
- [ ] Create `web/style.css` - Simple styling:
  - Kanban columns layout (flexbox)
  - Task card styling
  - Modal styling
  - Responsive design
- [ ] Create `web/app.js` - Frontend logic:
  - Fetch tasks from /api/tasks
  - Render tasks into columns based on state
  - Poll for updates every 5 seconds
  - Create task form submission
  - Edit task modal
  - Retry button handler
- [ ] Add task creation form (title, description fields)
- [ ] Add task details modal (show all fields, actions)
- [ ] Add manual refresh button

### 2.4 Integrate API with Scheduler
- [ ] Share DB instance between scheduler and API handlers
- [ ] Ensure thread-safe DB access (SQLite handles this)
- [ ] Add CORS headers if needed (for development)

## Phase 3: Polish & Testing

### 3.1 Error Handling
- [ ] Add structured logging (log/slog)
- [ ] Add error wrapping with context
- [ ] Handle database errors gracefully
- [ ] Handle agent process failures (crash, timeout)

### 3.2 Testing
- [ ] Write unit tests for store package (CRUD operations)
- [ ] Write unit tests for state machine (transitions)
- [ ] Write integration test for agent runner (with mock binary)
- [ ] Write E2E test: Create task → agent runs → task done
- [ ] Add test coverage for critical paths

### 3.3 Documentation
- [ ] Create README.md with:
  - Project overview
  - Architecture diagram
  - Setup instructions
  - Configuration reference
  - API documentation
  - Usage examples
- [ ] Add code comments for exported functions
- [ ] Add example config.yaml

### 3.4 Graceful Shutdown
- [ ] Implement graceful shutdown in scheduler:
  - Stop accepting new tasks
  - Wait for running agents to complete (with timeout)
  - Persist state to database
- [ ] Handle OS signals (SIGINT, SIGTERM)
- [ ] Close database connection cleanly

### 3.5 Metrics (Optional)
- [ ] Add Prometheus metrics endpoint (/metrics)
- [ ] Track: tasks_created, tasks_completed, tasks_failed, agents_running
- [ ] Track: reconcile_duration, db_query_duration

## Deployment

### Build & Run
- [ ] Build binary: `go build -o symphony cmd/symphony/main.go`
- [ ] Create default config at `~/.symphony/config.yaml`
- [ ] Run: `./symphony`
- [ ] Test Web UI at http://localhost:8080

### Systemd Service (Optional)
- [ ] Create systemd unit file: `/etc/systemd/system/symphony.service`
- [ ] Configure service to start on boot
- [ ] Add log rotation

## Verification Checklist

Before marking project complete:
- [ ] Can create task via Web UI
- [ ] Task triggers agent execution in workspace
- [ ] State transitions correctly (todo→running→done)
- [ ] Failed tasks retry with exponential backoff
- [ ] Kanban UI shows real-time task status
- [ ] Service recovers state after restart
- [ ] Graceful shutdown works (no data loss)
- [ ] All tests pass
- [ ] README is complete and accurate