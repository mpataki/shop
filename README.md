# Shop

Claude Agent Orchestration System. Coordinates multiple Claude Code agents through Lua workflow scripts with human-in-the-loop support.

## Install

```bash
go install ./cmd/shop
```

Requires `~/go/bin` on your `PATH`:

```bash
export PATH="$HOME/go/bin:$PATH"  # add to ~/.zshrc
```

## Quick Start

```bash
# Run a workflow (creates git worktree from current repo)
shop run code-review-loop "Add a fibonacci function"

# Run against a different repo
shop run simple-task "Fix the bug" -r /path/to/repo

# View status
shop status <run-id>
shop list
shop list --active  # Only active runs

# Kill a running workflow
shop kill <run-id>

# Continue a waiting workflow (human interaction)
shop continue <run-id>

# Stop a waiting workflow
shop stop <run-id> --reason "Changed approach"

# Resume workflow after human interaction
shop resume <run-id>

# Launch TUI
shop
```

## Workflows

Workflow scripts are Lua files that define a `workflow(prompt)` function. Place them in `.shop/workflows/` (project) or `~/.shop/workflows/` (user):

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
4. A Shop MCP server provides the `report_signal` tool to the agent
5. Agent calls `report_signal(status, summary)` — schema-enforced via tool calling
6. Signal is returned to Lua script which decides next action
7. If agent returns `NEEDS_HUMAN` or script calls `pause()`, workflow pauses
8. Human uses `shop continue` to interact with agent, then `shop resume`
9. Loop continues until script returns or calls `stuck()`

## Workspace Structure

Each run gets an isolated worktree:

```
~/.shop/workspaces/run-42/
└── repo/                    # git worktree (detached HEAD)
    ├── (your repo files)
    ├── .mcp.json            # MCP config (auto-generated per agent)
    ├── .agents/
    │   ├── SKILL.md         # protocol for agents
    │   ├── signals/         # structured output (JSON)
    │   ├── context.md       # accumulated context across agents
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

[j/k] navigate  [l/enter] view  [c] continue  [x] kill  [d] delete  [r] refresh  [q] quit
```

| Key | Action |
|-----|--------|
| `j`/`k` | Navigate up/down |
| `l`/`enter` | Select / enter |
| `h`/`esc` | Back |
| `g`/`G` | Jump to top/bottom |
| `n` | New run |
| `r` | Refresh |
| `c` | Continue waiting run |
| `s` | Stop waiting run |
| `x` | Kill run |
| `d` | Delete run |
| `o` | View output (in detail view) |
| `q` | Quit |

## Human Interaction

Workflows can pause for human input:

**Agent escalation** — agent calls `report_signal` with `NEEDS_HUMAN`:
```
report_signal(status="NEEDS_HUMAN", summary="...", reason="Need clarification on auth approach")
```

**Explicit checkpoint** — script calls `pause()`:
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
- Workflows: `.shop/workflows/` (project) or `~/.shop/workflows/` (user)
