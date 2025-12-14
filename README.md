# Shop

Claude Agent Orchestration System. Coordinates multiple Claude Code agents through Lua workflow scripts with human-in-the-loop support.

## Quick Start

```bash
# Build
go build -o shop ./cmd/shop

# Run a workflow (creates git worktree from current repo)
./shop run code-review-loop "Add a fibonacci function"

# Run against a different repo
./shop run simple-task "Fix the bug" -r /path/to/repo

# View status
./shop status <run-id>
./shop list
./shop list --active  # Only active runs

# Kill a running workflow
./shop kill <run-id>

# Continue a waiting workflow (human interaction)
./shop continue <run-id>

# Stop a waiting workflow
./shop stop <run-id> --reason "Changed approach"

# Resume workflow after human interaction
./shop resume <run-id>

# Launch TUI
./shop
```

## Specs

Workflow specs are Lua scripts that define a `workflow(prompt)` function. Place them in `~/.shop/specs/` or `.shop/specs/`:

```lua
-- code-review-loop.lua
function workflow(prompt)
  run("architect", prompt)

  for i = 1, 10 do
    local code = run("coder")
    if code.status == "BLOCKED" then
      return stuck(code.reason)
    end

    local review = run("reviewer")
    if review.status == "APPROVED" then
      break
    end
  end

  -- Optional: pause for human approval before deployment
  local ok = pause("Approve for production?")
  if not ok.continue then
    return stuck(ok.reason)
  end

  run("deployer")
end
```

Agents must exist as `.claude/agents/{name}.md` in your repository.

## How It Works

1. `shop run` creates a detached git worktree from your repo
2. Lua workflow script executes, calling `run()` for each agent
3. Each agent runs via `claude --agent {name} -p {prompt}`
4. Agent writes structured output to `.agents/signals/{agent}.json`
5. Signal is returned to Lua script which decides next action
6. If agent returns `NEEDS_HUMAN` or script calls `pause()`, workflow pauses
7. Human uses `shop continue` to interact with agent, then `shop resume`
8. Loop continues until script returns or calls `stuck()`

## Workspace Structure

Each run gets an isolated worktree:

```
~/.shop/workspaces/run-42/
└── repo/                    # git worktree (detached HEAD)
    ├── (your repo files)
    ├── .agents/
    │   ├── SKILL.md         # protocol for agents
    │   ├── signals/         # structured output (JSON)
    │   ├── messages/        # inter-agent notes (markdown)
    │   └── scratchpad/      # per-agent workspace
    └── .shop/
        └── run.json         # run metadata
```

## TUI

```
Shop

Recent Runs
───────────
▶ #42 code-review-loop    ⏸ waiting   Need auth clarification...
  #41 simple-task         ● running   Add fibonacci function...
  #40 code-review-loop    ✓ complete  Fix the bug in...

[enter] view  [c] continue  [x] kill  [d] delete  [r] refresh  [q] quit
```

| View | Key | Action |
|------|-----|--------|
| Run List | `enter` | View run details |
| Run List | `c` | Continue waiting run |
| Run List | `x` | Kill run |
| Run List | `d` | Delete run |
| Run List | `r` | Refresh |
| Run Detail | `↑/↓` | Select execution |
| Run Detail | `enter` | Resume session in Claude |
| Run Detail | `c` | Continue (if waiting) |
| Run Detail | `s` | Stop (if waiting) |
| Run Detail | `o` | View output |
| All | `q` / `esc` | Quit / Back |

## Human Interaction

Workflows can pause for human input:

**Agent escalation** - Agent returns `NEEDS_HUMAN` when stuck:
```json
{"status": "NEEDS_HUMAN", "reason": "Need clarification on auth approach"}
```

**Explicit checkpoint** - Script calls `pause()`:
```lua
local ok = pause("Approve deployment to production?")
if not ok.continue then
  return stuck(ok.reason)
end
```

When paused, use:
```bash
shop continue 42   # Open Claude session to help agent
# ... interact with agent ...
shop resume 42     # Continue workflow after agent is ready
```

## Data

- Database: `~/.shop/shop.db`
- Workspaces: `~/.shop/workspaces/`
- Specs: `~/.shop/specs/` or `.shop/specs/`
