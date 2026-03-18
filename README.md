# Shop

Claude Agent Orchestration System. Coordinates multiple Claude Code agents through JavaScript workflow scripts with human-in-the-loop support.

## Prerequisites

Shop invokes the Claude CLI in headless mode (`-p`). This counts against your Claude subscription's usage limits (Pro or Max) just like interactive sessions do — usage is shared across Claude and Claude Code. If you hit your plan's limit mid-workflow, Claude Code will offer to continue on API credits, but that requires explicit consent.

Workflows that run multiple agents or long review loops can consume your limit quickly. Start simple.

You'll need:
- The [Claude CLI](https://github.com/anthropics/claude-code) installed and authenticated
- A Claude Pro or Max subscription

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
# Launch TUI
shop

# Run a workflow (creates git worktree from current repo)
shop run code-review-loop "Add a fibonacci function"

# View status
shop status <run-id>
shop list
shop list --active

# Kill a running workflow
shop kill <run-id>

# Continue a paused workflow (human interaction)
shop continue <run-id>

# Stop a paused workflow
shop stop <run-id>

# Resume after crash/stop
shop resume <run-id>

# Delete a run and its workspace
shop delete <run-id>
```

## Workflows

Workflow scripts are JavaScript files that define a `workflow(prompt)` function. Place them in `.shop/workflows/` (project) or `~/.shop/workflows/` (user):

```js
// code-review-loop.js
function workflow(prompt) {
  run("architect", { prompt, model: "sonnet" });

  for (let i = 1; i <= 5; i++) {
    run("coder");

    const review = run("reviewer", { statuses: ["APPROVED", "CHANGES_REQUESTED"] });
    if (review.status === "APPROVED") break;
  }

  const ok = pause("Approve for production?");
  if (!ok.continue) return stuck(ok.reason);

  run("deployer");
}
```

Agents must exist as `.claude/agents/{name}.md` in your repository.

### Workflow API

- `run(agent, prompt?)` or `run(agent, { prompt?, model?, statuses? })` — invoke a Claude Code agent, returns its signal
- `pause(message)` — pause for human input, returns `{ continue, reason }`
- `stuck(reason?)` — terminate workflow as stuck
- `context()` — returns `{ run_id, repo, iteration, prompt }`
- `log(message)` — write to the run log

## How It Works

1. `shop run` creates a git worktree from your repo at `~/.shop/workspaces/run-{id}/repo/`
2. The JavaScript workflow executes, calling `run()` for each agent
3. Each agent runs as `claude -p {prompt} --mcp-config mcp.json`
4. A short-lived MCP server provides `report_signal`, `get_context`, and `get_run_info` tools to the agent
5. Agent calls `report_signal(status, summary)` when done — this is returned to the workflow as the signal
6. Workflow script inspects the signal and decides what to do next
7. If an agent returns `STUCK` or the script calls `pause()`, the workflow suspends for human input
8. Human uses `shop continue` to open an interactive Claude session; the agent reports a new signal when ready
9. Loop continues until the script returns or calls `stuck()`

All state is event-sourced: commands → events → projected state. Crash recovery works by replaying events and skipping already-completed `run()` calls by their index.

## Workspace Structure

```
~/.shop/workspaces/run-{id}/
├── repo/          # git worktree (isolated branch)
├── scratchpad/    # per-agent working directories
│   └── {agent}/
└── mcp.json       # MCP server config (regenerated per agent call)
```

## TUI

```
 shop

╭──────────────────────────────────────────────────────╮
│ ❯ #5  code-review-loop  ⠦ coder    2m  Add fibonacci  │
│   #4  simple            ✓ done    5m  Fix the bug     │
│   #3  code-review-loop  ⏸ waiting 1h  Auth approach   │
╰──────────────────────────────────────────────────────╯
```

| Key | Action |
|-----|--------|
| `j`/`k` | Navigate up/down |
| `l`/`enter` | View run details |
| `h`/`esc` | Back |
| `g`/`G` | Jump to top/bottom |
| `n` | New run |
| `c` | Continue waiting run |
| `s` | Stop waiting run (detail view) |
| `x` | Kill run |
| `d` | Delete run |
| `o` | View agent output (detail view) |
| `q` | Quit |

## Human Interaction

Workflows can pause for human input in two ways:

**Agent escalation** — agent calls `report_signal` with `STUCK`:
```
report_signal(status="STUCK", summary="Need clarification on the auth approach")
```

**Explicit checkpoint** — script calls `pause()`:
```js
const ok = pause("Approve deployment to production?");
if (!ok.continue) return stuck(ok.reason);
```

When paused:
```bash
shop continue <run-id>   # opens an interactive Claude session
# interact with the agent — it will report a new signal when ready
# workflow auto-resumes after the session ends
```

## Data

- Database: `~/.shop/shop.db`
- Workspaces: `~/.shop/workspaces/`
- Workflows: `.shop/workflows/` (project) or `~/.shop/workflows/` (user)
