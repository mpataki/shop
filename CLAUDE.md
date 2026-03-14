# Shop - Claude Agent Orchestration System

## What This Is

Shop orchestrates multiple Claude Code agents through Lua workflow scripts. It uses an event-sourced architecture: all state mutations flow through commands, all state derives from events. This gives strong concurrency semantics, full audit trail, crash recovery by design, and a foundation for parallel agent execution.

## Architecture Overview

```
cmd/shop/main.go          CLI entry point (run, resume, status, list, kill, delete, continue, stop)
internal/
  events/
    types.go              Event types (18), payload structs, NewEvent/DecodePayload helpers
    signal.go             SignalStatus type, validation, valid agent statuses
    store.go              SQLite event store, optimistic locking, command CRUD
    projection.go         RunState/ExecutionState, ProjectRun() fold function
  commands/
    types.go              Command types (10), payload structs
    processor.go          Per-run command processing goroutine, optimistic locking + retry
    handlers.go           Handler per command type (StartRun, ExecuteWorkflow, ReportSignal, etc.)
    mcp_config.go         MCP config generation with --call-index
  process/
    manager.go            ProcessManager interface, CLIManager (Claude CLI invocation)
  workflow/
    runtime.go            Sandboxed Lua VM with run(), stuck(), pause(), context(), log()
  mcp/
    server.go             MCP server (report_signal submits commands, get_context/get_run_info project from events)
  workspace/
    workspace.go          Git worktree creation
  config/
    config.go             Paths: ~/.shop/shop.db, .shop/workflows/, ~/.shop/workflows/
  tui/
    app.go                Bubbletea TUI
    views.go              Rendering from RunState/ExecutionState projections
    styles.go             Lipgloss styles
```

## Key Concepts

### Event Sourcing

All state mutations flow through commands → events:
1. External actors (CLI, TUI, MCP server) submit **commands** to the `commands` table
2. The **command processor** (one goroutine per active run) processes commands, executes side effects, and appends **events**
3. Current state is derived by **projecting** events via `ProjectRun()`
4. **Optimistic locking** via `UNIQUE(run_id, version)` on the events table

### Lua Workflows
Workflows are Lua scripts in `.shop/workflows/` or `~/.shop/workflows/` that define a `workflow(prompt)` function:

```lua
function workflow(prompt)
  run("architect", prompt)  -- run() invokes Claude Code agent

  for i = 1, 5 do
    local code = run("coder")
    if code.status == "BLOCKED" then
      return stuck(code.reason)  -- stuck() marks run as blocked
    end

    local review = run("reviewer")
    if review.status == "APPROVED" then
      break
    end
  end

  local ok = pause("Approve deployment?")
  if not ok.continue then
    return stuck(ok.reason)
  end

  run("deployer")
end
```

### Crash Recovery
Each `run()` call is assigned a `call_index`. On resume, the projection is rebuilt from events — completed executions at each call_index are returned from cache without re-running.

### Workspace Structure
Each run gets a workspace at `~/.shop/workspaces/run-{id}/` with:
- `repo/` - Git worktree (kept clean of orchestration files)
- `scratchpad/{agent}/` - Per-agent scratch space
- `mcp.json` - MCP server config (passed via `--mcp-config` flag)

### Agent Invocation
Agents are invoked via: `claude --agent {name} -p {prompt} --output-format json --dangerously-skip-permissions`

Agents must exist as `.claude/agents/{name}.md` in the repo worktree. Signals are reported via the MCP `report_signal` tool, which submits a `ReportSignal` command to the commands table.

## Database Schema

```sql
runs (id, created_at, version)           -- Minimal aggregate root
events (id, run_id, event_type, payload, version, created_at)  -- Append-only event log
commands (id, run_id, command_type, payload, status, error, created_at, processed_at)
```

Run statuses (from projection): `pending`, `running`, `complete`, `failed`, `stuck`, `waiting_human`, `killed`, `deleted`

## Command Types

`StartRun`, `ExecuteWorkflow`, `ExecuteAgent`, `ReportSignal`, `PauseForHuman`, `ProvideHumanInput`, `ResumeRun`, `KillRun`, `StopRun`, `DeleteRun`

## Event Types

Run lifecycle: `RunStarted`, `RunResumed`, `RunCompleted`, `RunFailed`, `RunStuck`, `RunWaitingHuman`, `RunKilled`, `RunStopped`, `RunDeleted`
Agent lifecycle: `AgentStarted`, `AgentCompleted`, `AgentFailed`, `SignalReceived`
Checkpoint: `CheckpointStarted`, `CheckpointCompleted`, `HumanInputReceived`
Runtime: `ReplayInvalidated`, `LogMessage`

## CLI Commands

```bash
shop run <workflow> <prompt>   # Start workflow
shop resume <run-id>           # Resume from last successful call_index
shop status <run-id>           # Show run details (projected from events)
shop list                      # List recent runs
shop list --active             # List only active runs
shop kill <run-id>             # Kill running process
shop delete <run-id>           # Remove run and workspace
shop continue <run-id>         # Open Claude session for waiting run
shop stop <run-id>             # Stop a waiting run
shop                           # Launch TUI
```

## Lua API (available in workflow scripts)

- `run(agent, prompt?)` or `run(agent, {prompt?, model?})` → signal table with `status`, `_session_id`, etc.
- `pause(message)` → pause for human approval, returns `{continue: bool, reason: string, message: string}`
- `stuck(reason?)` → terminate workflow as stuck
- `context()` → `{run_id, repo, iteration, prompt}`
- `log(message)` → write to run log

Sandbox removes: `os`, `io`, `debug`, `math.random`, `load*` functions

## Human Interaction

Workflows can pause for human input in two ways:

1. **Agent escalation**: Agent returns `{status: "NEEDS_HUMAN", reason: "..."}` signal
2. **Explicit checkpoint**: Script calls `pause("message")`

When paused:
- Run status becomes `waiting_human` (via `RunWaitingHuman` event)
- Human uses `shop continue <id>` to open Claude session
- Human interacts, agent writes new signal via MCP
- After exit, `ProvideHumanInput` command triggers `ResumeRun`

## File Locations

- Database: `~/.shop/shop.db`
- User workflows: `~/.shop/workflows/*.lua`
- Project workflows: `.shop/workflows/*.lua` (takes precedence)
- Workspaces: `~/.shop/workspaces/run-{id}/`

## Dependencies

- `github.com/yuin/gopher-lua` - Lua 5.1 VM
- `modernc.org/sqlite` - Pure Go SQLite
- `github.com/google/uuid` - Session ID generation
- `github.com/spf13/cobra` - CLI
- `github.com/charmbracelet/bubbletea` - TUI
- `github.com/charmbracelet/lipgloss` - TUI styling

## Specifications

- `specs/event-sourcing.md` - Full event-sourcing architecture spec
- `specs/lua.md` - Lua workflow specification
