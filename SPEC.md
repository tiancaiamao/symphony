# Symphony-Go Service Specification

## Problem Statement

Current wf-xxx workflow system has architectural issues:
- **Dependency inversion**: Agent drives workflow via /cron, instead of workflow polling and triggering agent
- **No visualization**: No Web UI to see inbox/in-progress tasks
- **Poor observability**: Only model files, hard to track workflow state

## Solution

Build a **Go service** that:
1. **Polls** task source (GitHub Issues or simple kanban)
2. **Manages** isolated workspaces per task
3. **Triggers** agent execution (~/project/ai in RPC mode)
4. **Provides** Web UI kanban for visualization
5. **Persists** state and coordinates execution

## Architecture

```
┌─────────────────────────────────────────────────┐
│         Web UI (Kanban + Status)                │
│  - View inbox/in-progress/done tasks            │
│  - Create/edit tasks                            │
│  - Trigger manual actions                       │
└─────────────────┬───────────────────────────────┘
                  │ HTTP API
                  ↓
┌─────────────────────────────────────────────────┐
│       Symphony-Go Service                       │
│                                                 │
│  ┌──────────────┐  ┌────────────────────────┐  │
│  │   Scheduler  │  │   Workspace Manager    │  │
│  │  (Poll Loop) │  │  (Create/Cleanup)      │  │
│  └──────┬───────┘  └────────────────────────┘  │
│         │                                       │
│         ├─→ State Machine (todo/running/done)  │
│         │                                       │
│         ├─→ Retry Queue (exponential backoff)  │
│         │                                       │
│         └─→ Agent Runner (ai RPC)              │
│                                                 │
│  ┌──────────────────────────────────────────┐  │
│  │      Task Source Adapter                  │  │
│  │  - GitHub Issues (primary)                │  │
│  │  - Simple JSON file (fallback)            │  │
│  └──────────────────────────────────────────┘  │
└─────────────────────────────────────────────────┘
                  │
                  ↓
         ~/project/ai (Agent)
```

## Core Components

### 1. Task Source Adapter

**Primary: GitHub Issues**
- Use GitHub Issues + GitHub Projects as kanban
- Poll GitHub API for issues with specific labels/states
- Support Projects API for kanban columns

**Fallback: Simple JSON**
```json
{
  "tasks": [
    {
      "id": "task-001",
      "title": "Implement feature X",
      "state": "todo",
      "created_at": "2024-03-27T10:00:00Z",
      "labels": ["feature"]
    }
  ]
}
```

### 2. Workspace Manager

- Create isolated workspace per task
- Path: `~/.symphony/workspaces/<task-id>/`
- Lifecycle:
  - `after_create`: Initialize (git clone, etc.)
  - `before_run`: Prepare (git pull, etc.)
  - `after_run`: Cleanup
  - `before_remove`: Finalize

### 3. Scheduler (Poll Loop)

```go
func (s *Scheduler) Run(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    for {
        select {
        case <-ticker.C:
            s.reconcile()
        case <-ctx.Done():
            return
        }
    }
}

func (s *Scheduler) reconcile() {
    // 1. Poll task source
    tasks := s.taskSource.List()
    
    // 2. For each task
    for _, task := range tasks {
        // Check current state
        state := s.stateStore.Get(task.ID)
        
        // State machine transitions
        switch state {
        case "todo":
            if s.canStart() {
                s.startAgent(task)
            }
        case "running":
            s.checkAgentHealth(task)
        case "done":
            s.cleanup(task)
        }
    }
    
    // 3. Process retry queue
    s.processRetries()
}
```

### 4. Agent Runner

Integrate with `~/project/ai` in RPC mode:

```go
type AgentRunner struct {
    aiPath string // ~/project/ai
}

func (r *AgentRunner) Start(task *Task, workspace string) error {
    // 1. Start ai process
    cmd := exec.Command(
        filepath.Join(r.aiPath, "bin/ai"),
        "--mode", "rpc",
        "--session", filepath.Join(workspace, ".session.jsonl"),
    )
    cmd.Dir = workspace
    
    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    cmd.Start()
    
    // 2. Send initial prompt
    prompt := map[string]interface{}{
        "type": "prompt",
        "message": task.Prompt,
    }
    json.NewEncoder(stdin).Encode(prompt)
    
    // 3. Read events
    scanner := bufio.NewScanner(stdout)
    for scanner.Scan() {
        var event map[string]interface{}
        json.Unmarshal(scanner.Bytes(), &event)
        
        switch event["type"] {
        case "agent_exit":
            return r.handleExit(event)
        case "agent_output":
            log.Println(event["content"])
        }
    }
    
    return nil
}
```

### 5. State Machine

```
         ┌──────────┐
         │   todo   │
         └────┬─────┘
              │ canStart()
              ↓
         ┌──────────┐
         │ running  │◄──────┐
         └────┬─────┘       │
              │             │
    ┌─────────┴──────┬──────┴─────┐
    │                │            │
    ↓                ↓            │
┌────────┐      ┌────────┐   retry
│  done  │      │ failed │      │
└────────┘      └────┬───┘      │
                     │ max_retry│
                     └──────────┘
```

States:
- **todo**: Ready to start
- **running**: Agent executing
- **done**: Completed successfully
- **failed**: Failed after max retries

### 6. Web UI (Kanban)

**Tech Stack**: Embedded Go server + minimal frontend
- Backend: Go HTTP server (embedded in service)
- Frontend: Simple HTML/CSS/JS (no heavy framework)

**Features**:
- Kanban board (todo/in-progress/done columns)
- Task cards with title, status, created time
- Click to see task details
- Manual actions (retry, cancel)

**API Endpoints**:
```
GET  /api/tasks          - List all tasks
GET  /api/tasks/:id      - Get task details
POST /api/tasks          - Create task
PUT  /api/tasks/:id      - Update task
POST /api/tasks/:id/retry - Retry failed task
GET  /health             - Health check
GET  /                   - Kanban UI
```

## Configuration

**Config file**: `~/.symphony/config.yaml`

```yaml
# Task source
task_source:
  type: github  # or "json"
  github:
    repo: "owner/repo"
    labels: ["symphony"]
    poll_interval: 30s
  json:
    path: ~/.symphony/tasks.json

# Workspace
workspace:
  root: ~/.symphony/workspaces
  hooks:
    after_create: "git clone {{repo_url}} ."
    before_run: "git pull"

# Agent
agent:
  path: ~/project/ai
  max_concurrent: 3
  timeout: 30m

# Retry
retry:
  max_retries: 3
  base_delay: 10s
  max_delay: 5m

# Server
server:
  port: 8080
```

## State Persistence

**Database**: `~/.symphony/symphony.db` (SQLite)

- All task state persisted in SQLite
- In-memory cache for fast access
- Atomic updates via transactions
- Auto-recovery on restart

**Retry Queue**: In-memory with SQLite backup
```sql
CREATE TABLE retry_queue (
    task_id TEXT PRIMARY KEY,
    next_retry_at TIMESTAMP,
    FOREIGN KEY (task_id) REFERENCES tasks(id)
);
```

## Implementation Plan

### Phase 1: Core Service (MVP)
- [ ] Task source adapter (GitHub Issues + JSON fallback)
- [ ] Scheduler with poll loop
- [ ] State machine (todo/running/done/failed)
- [ ] Agent runner (ai RPC integration)
- [ ] Workspace manager
- [ ] Retry queue with exponential backoff
- [ ] State persistence
- [ ] Basic logging

**Timeline**: 2-3 days

### Phase 2: Web UI
- [ ] HTTP server
- [ ] REST API endpoints
- [ ] Kanban UI (HTML/CSS/JS)
- [ ] Task creation/editing
- [ ] Real-time updates (polling)

**Timeline**: 1-2 days

### Phase 3: Polish
- [ ] Error handling improvements
- [ ] Metrics (Prometheus optional)
- [ ] Graceful shutdown
- [ ] Configuration validation
- [ ] Documentation

**Timeline**: 1 day

## Success Criteria

1. ✅ Can create tasks via Web UI
2. ✅ Tasks automatically trigger agent execution
3. ✅ Can view task status on kanban board
4. ✅ Failed tasks retry automatically
5. ✅ Service recovers state after restart
6. ✅ Web UI shows inbox/in-progress/done clearly

## Non-Goals

- Linear integration (keep it simple)
- Multi-tenancy
- Complex permissions
- Rich workflow customization (keep it simple)
- Distributed execution

## Open Questions

1. ~~**GitHub Projects vs Simple JSON**~~: **DECIDED: SQLite database**
   - ✅ SQLite provides concurrency control
   - ✅ Single file deployment
   - Future: Add GitHub Issues sync (Phase 3)

2. **Frontend Framework**: ~~Use framework (React/Vue) or vanilla JS?~~
   - ✅ **DECIDED: Vanilla JS** (keep it lightweight)

3. ~~**Real-time Updates**~~: ~~WebSocket or polling?~~
   - ✅ **DECIDED: Polling** (simpler, good enough)

4. ~~**Workspace Isolation**~~: ~~Docker or direct filesystem?~~
   - ✅ **DECIDED: Direct filesystem** (simpler, can add Docker later)

5. **Concurrency**: How to handle concurrent edits?
   - ✅ **DECIDED: SQLite** (built-in ACID, no manual locking)

## References

- Original SPEC.md: `/Users/genius/project/symphony/SPEC.md`
- Existing wf-xxx skills: `~/.aiclaw/skills/wf-*`
- Agent project: `~/project/ai`uggestion: Vanilla JS + simple CSS (keep it lightweight)

3. **Real-time Updates**: WebSocket or polling?
   - Suggestion: Polling (simpler, good enough)

4. **Workspace Isolation**: Docker or direct filesystem?
   - Suggestion: Direct filesystem (simpler, can add Docker later)

## References

- Original SPEC.md: `/Users/genius/project/symphony/SPEC.md`
- Existing wf-xxx skills: `~/.aiclaw/skills/wf-*`
- Agent project: `~/project/ai`