# Handoff: Restore Symphony's `ai` agent integration

**Goal:** make the Go implementation run again against the current `ai` CLI.
**Scope:** one package rewrite plus one config line. Nothing else.

---

## 1. What is broken

`go/internal/agent/ai/ai.go` is a **newline-delimited JSON** client for a
long-deleted `ai rpc` subcommand. It spawns the binary and speaks a bare
type-tagged protocol with no `id`, no handshake and no method names:

| Operation | On the wire (old) |
|---|---|
| send prompt | `{"type":"prompt","message":"..."}` |
| send command | `{"type":"<cmd.Type>"}` |
| read events | one JSON object per line, dispatched by `type` |

The config that points at it is `go/CONFIG.md:138-140`:

```yaml
agent:
  kind: ai
  command: ~/project/ai/bin/ai
  args: ["rpc"]        # <-- this subcommand no longer exists
```

`ai`'s current subcommands are:

```
models  acp  run  serve  watch  ls  send  kill  history  login-codex
```

There is no `rpc`. The protocol layer was replaced by **ACP** (newline-delimited
JSON-RPC 2.0) plus a socket-based `serve`/`send`/`watch`/`kill` control surface.

---

## 2. Verified facts about the current `ai` CLI

All confirmed against `~/project/ai` at HEAD.

### `ai serve` flags (`subcommand/run/run.go`)

| Flag | Meaning |
|---|---|
| `--id-file <path>` | writes the 6-char run ID to a file after startup — **use this as the handshake** |
| `--name <str>` | human-readable run name |
| `--role <name>` | loads `~/.ai/roles/<name>/agent.yaml` |
| `--model <id>` | model override |
| `--max-turns <n>` | max turns (0 = unlimited) |
| `--timeout <d>` | total execution timeout (0 = unlimited) |
| `--session <path>` | session file path |
| `--system-prompt <text>` | supports `@<file>` to load from a file |
| `--input <text>` / `--input-file <path>` | initial prompt |
| `--http <addr>` | debug HTTP server |

`ai serve` calls `syscall.Setpgid(0, 0)` to detach into its own process group,
and exits non-zero if the run ends in `failed`.

### `ai send` (`subcommand/send/send.go`)

| Flag | Meaning |
|---|---|
| `--id <runID>` | run ID **or any prefix**; auto-selects by cwd if omitted |
| `--wait` | block until the turn finishes and stream the response |
| `--timeout <d>` | **default 4m; `0` = unlimited** |
| `--summary` | with `--wait`: only final assistant text, suppress tool output |

`ai send` also accepts the message on **stdin** ("stdin content is prepended to
the argument message") — use `cat prompt.txt | ai send ...` to avoid `ARG_MAX`.

### Important: `agent_end` does **not** mean "process exited"

`subcommand/run/run.go:374-377`:

> `agent_end` lines to events.jsonl — the contract `ai ls` uses for idle
> detection. **`Serve` stays alive across turns (`agent_end` only means the
> current prompt finished).**

So a long-lived `ai serve` worker survives across turns, and the prompt-level
completion signal is what matters, not process lifetime. This is a *feature*
for Symphony: `StateAddressComment` review rounds can reuse the same session
instead of losing context.

### `ai kill <runID>` stops a run.

`~/.ai/runs/<id>/run.json` contains `pid` (see `RunMeta.PID`).

---

## 3. Critical gotchas — do not skip

### GOTCHA 1: `ai send --wait` defaults to a 4-minute timeout

The default `--timeout` is `4m`. On expiry it exits **2** and prints:

```
--- timeout after 4m0s: agent's turn is STILL IN FLIGHT, the response above is INCOMPLETE ---
```

If Symphony uses the default, a long agent turn is reported as failed, the task
enters retry, and the still-running agent becomes an orphan.

**You must pass `--timeout 0`** on every `ai send --wait` invocation.

### GOTCHA 2: process death must map to `EventError`, never `EventAgentEnd`

The old `ai.go` had two paths to `agent_end`: stdout EOF, and
`monitorProcess` observing process exit. Both disappear in the new design. In
the new design a **crashed `ai serve` process surfaces as `ai send` failing**
(control socket gone / non-zero exit).

Symphony's scheduler treats `EventAgentEnd` as "go check whether a PR or commits
were actually produced" (`verifyTaskCompletion`), and `EventError` as failure.
Mapping a crash to `EventAgentEnd` produces a **false "completed"** signal.

Mapping to apply:

```
ai send exit 0                      -> EventAgentEnd
ai send exit 2 (timeout)            -> EventError (and the worker is still alive — do not kill)
ai send exit non-zero, other        -> EventError
```

---

## 4. New contract for `go/internal/agent/ai/ai.go`

Keep the existing `agent.Agent` interface
(`Name/StartSession/SendPrompt/SendCommand/Events/Stop`) unchanged so the
scheduler needs no edits. Rewrite the implementation:

```
StartSession(cfg):
  spawn: ai serve --id-file <tmpIdFile> --name <taskID>
                  [--system-prompt @<tmpPromptFile>]   (or --role <name>)
                  [--model <cfg.Model>]
                  [--max-turns <cfg.MaxTurns>]
                  [--session <path>]
          with cmd.Dir = cfg.Workspace, cmd.Start()
  poll <tmpIdFile> until non-empty -> runID
  store runID; do NOT block here

SendPrompt(prompt):
  write prompt to a temp file
  run synchronously:  cat <f> | ai send --id <runID> --wait --timeout 0 --summary
  exit 0             -> emit EventAgentEnd
  exit 2             -> emit EventError
  exit non-zero      -> emit EventError

Stop():
  ai kill --id <runID>

PID():
  read ~/.ai/runs/<runID>/run.json -> .pid

SendCommand(cmd):   DELETE (see §5)
```

**Why `ai send --wait` instead of tailing `events.jsonl`:** it depends only on
the public CLI surface, not on a kernel-internal file protocol; and every turn
(first and every review round) uses one identical mechanism.

Rationale for `--id-file` as the handshake: the run ID is the correlation key
for everything afterwards (`ai send`, `ai kill`, `~/.ai/runs/<id>/`), and
parsing it out of stdout is fragile.

### Orphan / PID handling

Symphony's existing `handleRunningTask` orphan kill uses
`syscall.Kill(-task.AgentPID, SIGTERM)` to kill the whole process group. This
still works because `ai serve` calls `Setpgid(0, 0)`. `PID()` should prefer
`run.json`'s `pid` over the spawned PID so the value stays correct across a
Symphony restart.

---

## 5. Delete the `Command` abstraction

`agent.Command` / `SendCommand` exist only to send `{"type": <cmd.Type>}` over
the old wire format. ACP has no separate command message: a registered slash
command runs by putting it as the **first token** of `session/prompt` text
(see `pkg/protocol/acp.go`: "Text whose first token names a REGISTERED command
runs synchronously and answers right away with end_turn").

So: remove `SendCommand` from the interface implementation (or make it a no-op
that forwards text into `SendPrompt`). Nothing in `scheduler.go` calls it.

---

## 6. Config change

`go/CONFIG.md:138-140`:

```yaml
agent:
  kind: ai
  command: ai              # or an absolute path
  args: ["serve"]          # was: ["rpc"]
```

Only the subcommand changes; all other flags are appended by `ai.go`.
`factory.go`'s `case "codex"` also routes to `ai.New` — harmless, leave it.

---

## 7. Do NOT change

The scheduler is **not** broken. An earlier review incorrectly claimed
`s.runTask` blocks the reconcile loop — that is wrong; it is already wrapped in
`go func(){...}` at `scheduler.go:922`. **Do not "fix" anything here.**

Leave untouched:

- `Scheduler.Run` / `reconcile` ticker loop
- the 9-state machine (`StateTodo` … `StateArchive`)
- `MaxConcurrentAgents` capacity check and the retry queue
- `handleRunningTask` liveness checks and orphan kill
- `internal/workspace` (git worktree management)
- `internal/http` / webui
- the `gh` CLI wrappers
- `verifyTaskCompletion`

---

## 8. Acceptance tests

| # | Test | Pass criterion |
|---|---|---|
| 1 | One trivial issue with the ready label, run end to end | Task reaches `Done`; PR exists |
| 2 | **A task that legitimately takes > 4 minutes** | Not truncated at 4m (validates GOTCHA 1) |
| 3 | Kill Symphony mid-run, restart it | Orphan recognised and reaped; no duplicate PR opened |
| 4 | Three issues concurrently | Three agents genuinely run in parallel |
| 5 | **Force an agent to crash mid-turn** | Goes through `EventError` → retry, **not** a false "completed" (validates GOTCHA 2) |

Tests 2 and 5 are regression tests for the two gotchas. They are both
"looked like it was working, but had already gone wrong" failure modes — do not
skip them.

---

## 9. Source references

| What | Where |
|---|---|
| Old ndjson client to replace | `go/internal/agent/ai/ai.go` |
| Agent interface to keep | `go/internal/agent/agent.go` |
| Factory (kind dispatch) | `go/internal/agent/factory/factory.go` |
| Config defaults | `go/internal/config/config.go:93-94` |
| Documented config | `go/CONFIG.md:137-140` |
| The goroutine that already exists | `go/internal/scheduler/scheduler.go:922` |
| `EventAgentEnd` / `EventError` consumers | `go/internal/scheduler/scheduler.go:984-1060` |
| `ai serve` flags | `~/project/ai/subcommand/run/run.go` |
| `ai send` flags + exit codes | `~/project/ai/subcommand/send/send.go` |
| `Serve stays alive across turns` | `~/project/ai/subcommand/run/run.go:374-377` |
| `RunMeta` (pid, status, session) | `~/project/ai/subcommand/run/tui/meta.go:28-38` |

---

## 10. Out of scope

Do not port or extend the Elixir implementation under `elixir/`. It is the
upstream OpenAI reference implementation and only integrates Codex; it is not a
second implementation of the `ai` backend.