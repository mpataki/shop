# Event-Sourced Architecture Spec

## Status: Draft

## Overview

Redesign Shop's orchestration around event sourcing. All state mutations flow through commands that produce events. Current state is derived by projecting events. This gives us: strong concurrency semantics, full audit trail, crash recovery by design, and a foundation for parallel agent execution.

## Core Concepts

### Commands

Commands represent intent. They are submitted by external actors (CLI, TUI, MCP server, workflow runtime) and processed by a single command processor. Commands can be rejected.

Commands are written to a `commands` table and processed in order. Each command targets a specific **aggregate** (a run). Processing a command reads the current projection, validates the command, executes side effects, and appends events.

### Events

Events represent facts — things that happened. They are immutable and append-only. The full history of a run is its event stream. Events are the source of truth; all other state is derived.

### Projections

A projection is a materialized view of current state, built by folding events. On startup (or when loading a run), we replay the event stream into an in-memory struct. No separate projection tables — the projection lives in memory only.

The `runs` and `executions` tables from the current schema are **removed**. They become in-memory projections rebuilt from events on demand.

### Aggregates

The aggregate is a **Run**. All commands and events belong to a run. Each run has its own event stream. Commands are processed with optimistic locking on the run's aggregate version (the count of events for that run).

## Schema

### `commands` table

```sql
CREATE TABLE commands (
    id TEXT PRIMARY KEY,                    -- UUID
    run_id INTEGER NOT NULL,
    command_type TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',     -- JSON
    status TEXT NOT NULL DEFAULT 'pending', -- pending | processing | processed | failed
    error TEXT,                             -- failure reason if status=failed
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    processed_at TIMESTAMP
);

CREATE INDEX idx_commands_pending ON commands(status, created_at)
    WHERE status = 'pending';
CREATE INDEX idx_commands_run ON commands(run_id);
```

### `events` table

```sql
CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',     -- JSON
    version INTEGER NOT NULL,              -- aggregate version (1-based, sequential per run)
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(run_id, version)
);

CREATE INDEX idx_events_run ON events(run_id, version);
```

### `runs` table (minimal — index only)

```sql
CREATE TABLE runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    version INTEGER NOT NULL DEFAULT 0     -- current aggregate version (event count)
);
```

The `runs` table exists only as an aggregate root with a version counter for optimistic locking. All run state (status, current agent, error, etc.) lives in the projection.

### Removed tables

- `executions` — replaced by projection from events
- `workflow_events` — replaced by `events` (the new canonical event log)

## Command Types

### `StartRun`

Submitted by: CLI / TUI

```json
{
    "workflow_path": "/path/to/workflow.lua",
    "workflow_name": "my-workflow",
    "initial_prompt": "Build a REST API",
    "source_repo": "/path/to/repo"
}
```

Processing:
1. Create workspace (git worktree)
2. Emit `RunStarted` event
3. Submit `ExecuteWorkflow` command

### `ExecuteWorkflow`

Submitted by: `StartRun` processor / `ResumeRun` processor

This is an internal command. It tells the processor to start (or restart) the workflow script. The workflow runtime is created and begins executing. When the runtime calls `run()`, it submits `ExecuteAgent` commands and waits for results.

```json
{}
```

Processing:
1. Load event stream, build projection
2. Create workflow runtime (Lua/JS VM)
3. Execute script — the runtime interacts with the command system (see Workflow Runtime section)
4. Based on outcome, emit terminal event (`RunCompleted`, `RunFailed`, `RunStuck`)

### `ExecuteAgent`

Submitted by: workflow runtime (when script calls `run()`)

```json
{
    "agent_name": "coder",
    "call_index": 3,
    "prompt": "optional override prompt",
    "model": "sonnet"
}
```

Processing:
1. Check projection for completed execution at this call_index (replay)
   - If found with matching agent: return cached result, no events emitted
   - If found with different agent: emit `ReplayInvalidated` from that index forward
2. Generate session ID (UUID)
3. Emit `AgentStarted` event
4. Invoke Claude CLI (side effect)
5. Wait for process exit
6. Drain pending commands for this run (processes `ReportSignal` → emits `SignalReceived`)
7. Read signal from updated projection
8. Emit `AgentCompleted` or `AgentFailed` event
9. Return signal to workflow runtime

### `ReportSignal`

Submitted by: MCP server (when agent calls `report_signal` tool)

```json
{
    "call_index": 3,
    "status": "DONE",
    "summary": "Implemented the REST API",
    "reason": null
}
```

Processing:
1. Validate signal status
2. Emit `SignalReceived` event

This is the key concurrency point. The MCP server is a separate process. It submits a `ReportSignal` command which the processor handles. Since the processor goroutine is blocked waiting on the agent process, `ReportSignal` commands are not processed immediately — they sit in the `commands` table as `pending`.

After the agent process exits, the processor drains all pending commands for the run before continuing. This is when `ReportSignal` gets processed, emitting the `SignalReceived` event. The processor then reads the signal from the freshly-updated projection to emit `AgentCompleted`.

This "process after exit" approach is simple and correct:
- The signal must arrive before (or at) process exit, so the command is always in the table by the time we drain
- No concurrent event writes — the processor is the sole writer for this run's events
- No staging tables or special mailboxes — commands are the universal inbox

### `PauseForHuman`

Submitted by: workflow runtime (when script calls `pause()`)

```json
{
    "call_index": 5,
    "message": "Approve deployment?"
}
```

Processing:
1. Emit `CheckpointStarted` event
2. Start checkpoint agent (Claude session for human interaction)
3. Wait for human response (signal)
4. Emit `CheckpointCompleted` or `RunWaitingHuman` event

### `ProvideHumanInput`

Submitted by: CLI (`shop continue` flow — after human exits Claude session)

```json
{
    "call_index": 3,
    "signal": { "status": "CONTINUE", "summary": "Looks good" }
}
```

Processing:
1. Validate run is in `waiting_human` state (from projection)
2. Emit `HumanInputReceived` event
3. Submit `ResumeRun` command

### `ResumeRun`

Submitted by: CLI (`shop resume`) / `ProvideHumanInput` processor

```json
{}
```

Processing:
1. Emit `RunResumed` event
2. Submit `ExecuteWorkflow` command (which replays from events)

### `KillRun`

Submitted by: CLI / TUI

```json
{}
```

Processing:
1. Find active agent PID from projection
2. Send SIGKILL to process group
3. Emit `RunKilled` event

### `StopRun`

Submitted by: CLI / TUI

```json
{
    "reason": "No longer needed"
}
```

Processing:
1. Validate run is in `waiting_human`
2. Emit `RunStopped` event

### `DeleteRun`

Submitted by: CLI / TUI

```json
{}
```

Processing:
1. Clean up workspace (git worktree, files)
2. Emit `RunDeleted` event
3. (Events are kept for audit — a `RunDeleted` event marks the run as logically deleted)

## Event Types

All events have: `run_id`, `version`, `event_type`, `payload`, `created_at`.

### Run lifecycle

| Event | Payload | Notes |
|-------|---------|-------|
| `RunStarted` | `{workflow_path, workflow_name, initial_prompt, workspace_path}` | First event for any run |
| `RunResumed` | `{}` | After resume from crash or human input |
| `RunCompleted` | `{}` | Terminal — workflow finished successfully |
| `RunFailed` | `{error}` | Terminal — unrecoverable error |
| `RunStuck` | `{reason}` | Terminal — workflow called `stuck()` |
| `RunWaitingHuman` | `{reason, call_index, session_id}` | Waiting for human input |
| `RunKilled` | `{}` | Terminal — user killed it |
| `RunStopped` | `{reason}` | Terminal — user stopped a waiting run |
| `RunDeleted` | `{}` | Logical deletion marker |

### Agent lifecycle

| Event | Payload | Notes |
|-------|---------|-------|
| `AgentStarted` | `{agent_name, call_index, session_id, pid, prompt, model}` | Agent process launched |
| `AgentCompleted` | `{agent_name, call_index, signal}` | Agent finished, signal received |
| `AgentFailed` | `{agent_name, call_index, error, exit_code}` | Agent process failed |
| `SignalReceived` | `{call_index, signal}` | MCP reported signal (audit/correlation) |

### Checkpoint lifecycle

| Event | Payload | Notes |
|-------|---------|-------|
| `CheckpointStarted` | `{call_index, message, session_id}` | pause() initiated |
| `CheckpointCompleted` | `{call_index, signal}` | Human responded to checkpoint |
| `HumanInputReceived` | `{call_index, signal}` | Human provided input for NEEDS_HUMAN |

### Workflow runtime

| Event | Payload | Notes |
|-------|---------|-------|
| `ReplayInvalidated` | `{from_call_index, reason}` | Determinism violation, forward cache discarded |
| `LogMessage` | `{message}` | Workflow called `log()` |

## Projection: RunState

The in-memory projection for a run, built by folding events:

```go
type RunState struct {
    ID             int
    CreatedAt      time.Time
    Version        int              // event count

    // Derived from events
    Status         RunStatus        // pending, running, complete, failed, stuck, waiting_human, killed, deleted
    WorkflowPath   string
    WorkflowName   string
    InitialPrompt  string
    WorkspacePath  string
    Error          string
    WaitingReason  string
    WaitingSessionID string
    CurrentAgent   string

    // Execution history
    Executions     []ExecutionState // ordered by call_index

    // Log
    LogMessages    []LogEntry
}

type ExecutionState struct {
    AgentName    string
    CallIndex    int
    SessionID    string
    PID          int
    Status       ExecStatus       // started, completed, failed, waiting_human
    Signal       map[string]any   // nil until completed
    Prompt       string
    Model        string
    StartedAt    time.Time
    CompletedAt  *time.Time
}
```

### Projection function

```
func ProjectRun(events []Event) *RunState
```

Each event type has a handler that mutates the RunState:

- `RunStarted` → set status=running, populate metadata fields
- `AgentStarted` → append to Executions, set CurrentAgent
- `AgentCompleted` → update execution status+signal, clear CurrentAgent
- `AgentFailed` → update execution status+error
- `SignalReceived` → (informational, maybe update execution signal for audit)
- `RunWaitingHuman` → set status=waiting_human, set WaitingReason+SessionID
- `HumanInputReceived` → update execution signal
- `RunResumed` → set status=running
- `RunCompleted` → set status=complete
- `RunFailed` → set status=failed, set Error
- `RunStuck` → set status=stuck, set WaitingReason
- `RunKilled` → set status=killed
- `RunStopped` → set status=stuck
- `RunDeleted` → set status=deleted
- `LogMessage` → append to LogMessages
- `CheckpointStarted` → append to Executions (agent=_checkpoint)
- `CheckpointCompleted` → update execution

## Command Processor

### Architecture

Single goroutine per run. When a run is active, a processor goroutine owns it. The processor:

1. Polls for pending commands for its run
2. Loads current projection (from events)
3. Processes command:
   a. Validate against current state
   b. Execute side effects (invoke Claude, create workspace, etc.)
   c. Append events with optimistic locking
4. Mark command as processed (or failed)
5. Repeat

### Optimistic Locking

When appending events:

```sql
INSERT INTO events (run_id, event_type, payload, version)
VALUES (?, ?, ?, ?);
-- version = current projection version + 1
-- UNIQUE(run_id, version) constraint prevents conflicts
```

If the insert fails (version conflict), the processor reloads the projection and retries the command. This handles the MCP server submitting `ReportSignal` concurrently with the processor.

### Startup / Recovery

On startup:
1. Query all runs with non-terminal last events
2. For each: check if there's an active process (PID from projection)
3. If process is dead but run is non-terminal: the run needs recovery
4. Submit appropriate recovery command (usually `ResumeRun`)

### Process Manager

The processor needs a way to start processes and wait for them. This is a sub-component:

```
type ProcessManager interface {
    StartAgent(ctx context.Context, opts AgentOpts) (pid int, done <-chan ProcessResult, err error)
    Kill(pid int) error
}
```

This encapsulates Claude CLI invocation. The processor calls `StartAgent`, gets back a channel, and selects on it alongside new commands.

## Workflow Runtime Integration

### The execution loop

When `ExecuteWorkflow` is processed, the processor creates a workflow runtime and runs the script. The key question is how `run()` calls interact with the command/event system.

**Design: synchronous within the processor goroutine.**

The workflow script runs on the processor's goroutine. When the script calls `run("coder")`:

1. Runtime checks projection for completed execution at this call_index
   - If found: return cached signal immediately (replay path)
2. If not found: runtime invokes `ProcessManager.StartAgent()`
3. Runtime blocks waiting for the agent process to exit
4. After exit: processor drains pending commands for this run
   - This processes the `ReportSignal` command submitted by MCP, emitting `SignalReceived`
5. Processor reads signal from updated projection, emits `AgentCompleted`, returns signal to script

The MCP server submits a `ReportSignal` command to the `commands` table. Since the processor is blocked on the agent process, this command sits as `pending`. After the process exits, the processor drains it. No special staging tables — commands are the universal inbox for all state mutations, including signals from external processes.

### Parallel execution (future)

When the script language supports parallel `run()` calls:

```javascript
const [a, b] = await Promise.all([
    run("coder"),
    run("reviewer")
]);
```

The runtime submits both `StartAgent` calls, then waits on both done channels. Each completion appends its own `AgentCompleted` event (with incrementing versions). The runtime returns both results to the script when both are done.

The call_index scheme needs to handle this. Options:
- Assign call_index at submission time (so parallel calls get sequential indices)
- Use a tree-structured index (e.g., "3.1", "3.2" for parallel branches)

For now, sequential indices assigned at submission time are fine. Replay sees two `AgentCompleted` events at indices 3 and 4, returns both.

## MCP Server Changes

The MCP server becomes simpler. All mutations go through commands.

### `report_signal` tool
- Submits a `ReportSignal` command to the `commands` table
- Params: `--db`, `--run-id`, `--call-index`

### `get_context` tool
- Reads from `events` table directly
- Filters for `AgentCompleted` events for this run, excluding current call_index
- Builds context markdown from signal payloads

### `get_run_info` tool
- Projects run state from events
- Returns: run ID, workflow name, status, initial prompt, current agent

## TUI Changes

The TUI subscribes to events (as today, via a channel). But now the event channel carries the typed events from the `events` table, not a separate in-memory event type.

### Run list view
- Query all non-deleted runs: `SELECT DISTINCT run_id FROM events` + project each
- Or maintain a lightweight index: the `runs` table with just `id, created_at, version`
- Filter active: project and check status

### Run detail view
- Load all events for run, project to `RunState`
- Display executions, status, logs from projection

### Live updates
- Processor emits events to an in-process channel after appending to DB
- TUI receives and re-projects (or incrementally updates)

## CLI Changes

CLI commands submit commands and (for synchronous operations) wait for completion:

### `shop run`
1. Insert run row
2. Submit `StartRun` command
3. If `--no-exec`: return run ID
4. Otherwise: start processor, wait for terminal event, print result

### `shop resume`
1. Submit `ResumeRun` command
2. Start processor (or signal existing), wait for terminal event

### `shop continue`
1. Read projection to get session ID + workspace
2. Open Claude session (interactive — not a command)
3. After human exits: submit `ProvideHumanInput` command

### `shop kill / stop / delete`
1. Submit corresponding command
2. Wait for processed

### `shop status`
1. Project run state from events
2. Print

### `shop list`
1. For each run: project state, filter, display

## File Structure (proposed)

```
internal/
  commands/
    types.go          -- Command type definitions
    processor.go      -- Command processor (per-run goroutine)
    handlers.go       -- Handler for each command type
  events/
    types.go          -- Event type definitions
    store.go          -- Append, query, optimistic locking
    projection.go     -- ProjectRun function
  process/
    manager.go        -- Claude CLI process management
  mcp/
    server.go         -- MCP tools (simplified)
  workflow/
    runtime.go        -- Script VM (Lua or JS), run/pause/stuck API
  workspace/
    workspace.go      -- Git worktree, directories
  tui/
    app.go            -- Bubbletea app
    views.go          -- Rendering
    styles.go         -- Lipgloss
  config/
    config.go         -- Paths
```

Removed: `models/` (types move into `commands/`, `events/`), `storage/` (replaced by `events/store.go`), `orchestrator/` (replaced by `commands/processor.go`), `lua/` (replaced by `workflow/`).

## Migration Path

Since this is greenfield enough to rewrite freely:

1. **Phase 1: Event store + projection** — New `events` and `commands` tables. `ProjectRun` function. Drop `executions` and `workflow_events` tables. Simplify `runs` table to index-only.

2. **Phase 2: Command processor** — Replace orchestrator with command processor. Each command type gets a handler. ProcessManager wraps Claude invocation.

3. **Phase 3: Workflow runtime** — Extract from current Lua code. `run()` interacts with processor via submitted commands (or direct calls within the processor goroutine). Keep Lua for now.

4. **Phase 4: MCP server** — Simplify to submit commands, read from `events`.

5. **Phase 5: TUI** — Rewrite to project from events. Subscribe to event channel.

6. **Phase 6: CLI** — Update to submit commands.

Each phase can be implemented independently and tested. Phases 1-3 are the core. 4-6 are integration.

## Open Questions

1. **Script language**: Lua vs JavaScript. Deferred — the command/event boundary is language-agnostic. The runtime is swappable.

2. **Daemon mode / process split**: The processor is a standalone loop that reads commands from SQLite and writes events to SQLite. It does not require the TUI. This enables a natural split:
   - `shop -d` (or `shop daemon`) — runs the command processor only, logs events to stdout
   - `shop` (TUI) — renders UI from event projections, submits commands
   - When running together (default today), the TUI starts an embedded processor and gets live updates via in-process channel. When running separately, the TUI polls the events table.
   - Detection: the processor could write a PID file or use an advisory lock so the TUI knows whether to start its own processor or connect to an existing one.
   - Not implementing now, but the design must not assume processor and TUI are in the same process. All communication goes through SQLite.

3. **Event compaction**: Over time, event streams grow. For local use this is unlikely to matter. If it does, snapshot + compact is the standard answer. Not needed now.

4. **Command queue ordering**: Within a run, commands are processed sequentially. Across runs, processors are independent. No global ordering needed.

5. **Parallel execution semantics**: How call_index works with parallel `run()` calls. Sequential assignment at submission is the simplest approach. Revisit when implementing parallel support in the script runtime.

## Design Principles

1. **Commands are the only way to mutate state.** No direct DB writes outside the command processor. The MCP server submits commands like any other actor.

2. **Events are immutable facts.** Never update or delete events. The `events` table is append-only.

3. **Projection is always rebuildable.** If the in-memory state is wrong, replay events. The projection function is the single definition of "current state."

4. **Optimistic locking via aggregate version.** The `UNIQUE(run_id, version)` constraint is the concurrency control. No distributed locks, no mutexes on DB writes.

5. **Side effects happen exactly once.** The command processor ensures side effects (starting agents, creating workspaces) happen during command processing. On retry (version conflict), it re-checks whether the effect already happened.

6. **The processor owns the run.** One goroutine per active run. No concurrent processing of commands for the same run. External actors (MCP, CLI, TUI) submit commands; the processor is the sole consumer.
