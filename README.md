# Shop

Claude Agent Orchestration System. Coordinates multiple Claude Code agents through YAML-defined workflows.

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

# Kill a running workflow
./shop kill <run-id>

# Launch TUI
./shop
```

## Specs

Workflow specs define agents and transitions. Place them in `~/.shop/specs/` or `.shop/specs/`:

```yaml
name: code-review-loop
description: Iterative coding with review feedback
start: coder

agents:
  coder:
    output_schema:
      status:
        type: enum
        values: [DONE, BLOCKED]
      summary:
        type: string

  reviewer:
    output_schema:
      status:
        type: enum
        values: [APPROVED, CHANGES_REQUESTED, BLOCKED]
      issues:
        type: array
        optional: true

transitions:
  - from: coder
    to: reviewer

  - from: reviewer
    to: END
    when:
      status: APPROVED

  - from: reviewer
    to: coder
    # fallback: loop back for changes

settings:
  max_iterations: 10
```

## How It Works

1. `shop run` creates a detached git worktree from your repo
2. First agent receives your prompt and runs via `claude -p`
3. Agent writes structured output to `.agents/signals/{agent}.json`
4. Shop evaluates transitions to determine next agent
5. Next agent receives previous agent's feedback
6. Loop continues until `END` or `STUCK`

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
▶ #42 code-review-loop    ● running   Refactor auth module...
  #41 simple-task         ✓ complete  Add fibonacci function...
  #40 code-review-loop    ✗ failed    Fix the bug in...

[n] new run  [enter] view  [x] kill  [r] refresh  [q] quit
```

| View | Key | Action |
|------|-----|--------|
| Run List | `enter` | View run details |
| Run List | `x` | Kill run |
| Run List | `r` | Refresh |
| Run Detail | `↑/↓` | Select execution |
| Run Detail | `enter` | Resume session in Claude |
| All | `q` / `esc` | Quit / Back |

## Data

- Database: `~/.shop/shop.db`
- Workspaces: `~/.shop/workspaces/`
- Specs: `~/.shop/specs/` or `.shop/specs/`
