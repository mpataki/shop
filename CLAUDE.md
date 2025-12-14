# Shop - Claude Agent Orchestration System

## What This Is

Shop orchestrates multiple Claude Code agents through Lua workflow scripts. It runs agents in sequence, handles their outputs (signals), and supports crash recovery via SQLite persistence.

## Architecture Overview

```
cmd/shop/main.go          CLI entry point (run, resume, status, list, kill, delete)
internal/
  config/config.go        Paths: ~/.shop/shop.db, .shop/specs/, ~/.shop/specs/
  storage/sqlite.go       SQLite schema and CRUD for runs/executions
  models/run.go           Run struct (ID, Status, SpecPath, Error, etc.)
  models/execution.go     Execution struct (AgentName, CallIndex, OutputSignal, etc.)
  orchestrator/           StartRun, Execute, Resume, KillRun, DeleteRun
  lua/runtime.go          Sandboxed Lua VM with run(), stuck(), context(), log()
  workspace/workspace.go  Git worktree creation, signal file reading
  tui/app.go              Bubbletea TUI for viewing runs
```

## Key Concepts

### Lua Workflows
Specs are Lua scripts in `.shop/specs/` or `~/.shop/specs/` that define a `workflow(prompt)` function:

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
      return  -- normal return = success
    end
  end

  stuck("max iterations")
end
```

### Crash Recovery
Each `run()` call is assigned a `call_index`. Before executing an agent, the runtime checks SQLite for a cached result at that index. On resume, completed executions return their cached signals without re-running.

### Workspace Structure
Each run gets a git worktree at `~/.shop/workspaces/run-{id}/repo/` with:
- `.agents/signals/{agent}.json` - Agent output signals
- `.agents/context.md` - Accumulated context for agents
- `.agents/scratchpad/{agent}/` - Per-agent scratch space

### Agent Invocation
Agents are invoked via: `claude --agent {name} -p {prompt} --output-format json --dangerously-skip-permissions`

Agents must exist as `.claude/agents/{name}.md` in the workspace and must write their signal to `.agents/signals/{name}.json`.

## Database Schema

```sql
runs (id, status, spec_path, initial_prompt, workspace_path, current_agent, error, ...)
executions (id, run_id, call_index, agent_name, status, output_signal, session_id, pid, ...)
```

Run statuses: `pending`, `running`, `complete`, `failed`, `stuck`

## CLI Commands

```bash
shop run <spec> <prompt>   # Start workflow
shop resume <run-id>       # Resume from last successful call_index
shop status <run-id>       # Show run details
shop list                  # List recent runs
shop kill <run-id>         # Kill running process
shop delete <run-id>       # Remove run and workspace
shop                       # Launch TUI
```

## Lua API (available in workflow scripts)

- `run(agent, prompt?)` → signal table with `status`, `_session_id`, etc.
- `stuck(reason?)` → terminate workflow as stuck
- `context()` → `{run_id, repo, iteration, prompt}`
- `log(message)` → write to run log

Sandbox removes: `os`, `io`, `debug`, `math.random`, `load*` functions

## File Locations

- Database: `~/.shop/shop.db`
- User specs: `~/.shop/specs/*.lua`
- Project specs: `.shop/specs/*.lua` (takes precedence)
- Workspaces: `~/.shop/workspaces/run-{id}/`

## Dependencies

- `github.com/yuin/gopher-lua` - Lua 5.1 VM
- `modernc.org/sqlite` - Pure Go SQLite
- `github.com/spf13/cobra` - CLI
- `github.com/charmbracelet/bubbletea` - TUI
- `github.com/charmbracelet/lipgloss` - TUI styling

## Spec Documentation

See `specs/lua.md` for the complete Lua workflow specification including resume semantics, determinism requirements, and error handling.
