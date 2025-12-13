# Shop: Claude Agent Orchestration System

## Overview

Shop is a lightweight orchestration system for coordinating multiple Claude Code agents through defined workflows. It provides a TUI for submitting tasks, visualizing execution, and inspecting results—while delegating the actual agent interaction to Claude Code itself.

## Design Principles

1. **Claude Code is the runtime** — Shop doesn't capture or replay agent output. It stores session references and shells out to `claude --resume` for inspection.

2. **Workspace is the communication channel** — Agents share context through the filesystem, not message passing infrastructure.

3. **Mechanical transitions** — No LLM-as-judge. Agents produce structured output, orchestrator matches it deterministically.

4. **Sequential execution (v1)** — One agent runs at a time. Parallelism is a future enhancement.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                         TUI (bubbletea)                     │
│  ┌─────────────────┐  ┌──────────────────────────────────┐  │
│  │  Prompt Input   │  │  Execution Graph                 │  │
│  │                 │  │  START → coder → reviewer → END  │  │
│  │  [spec: ...]    │  │            ✓        ●            │  │
│  └─────────────────┘  └──────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  Output Panel (selected node info)                   │   │
│  │  Session: abc123 | Duration: 2m13s | Exit: 0         │   │
│  │  [Enter] open in claude code  [r] rerun              │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
           │                              ▲
           │ spawns                       │ reads
           ▼                              │
┌─────────────────┐              ┌────────────────┐
│  Claude Code    │──artifacts──▶│   Workspace    │
│  (agent run)    │              │  .agents/      │
└─────────────────┘              └────────────────┘
           │                              
           │ writes                       
           ▼                              
┌─────────────────┐
│     SQLite      │
│  (run metadata) │
└─────────────────┘
```

---

## Data Model

### SQLite Schema

```sql
CREATE TABLE runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP,
    initial_prompt TEXT NOT NULL,
    spec_name TEXT NOT NULL,
    workspace_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',  -- pending, running, complete, failed, stuck
    current_agent TEXT
);

CREATE TABLE executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    agent_name TEXT NOT NULL,
    claude_session_id TEXT,  -- reference to claude code session
    status TEXT NOT NULL DEFAULT 'pending',  -- pending, running, complete, failed
    exit_code INTEGER,
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    output_signal TEXT,  -- JSON blob from .agents/signals/{agent}.json
    sequence_num INTEGER NOT NULL,  -- order within run
    
    UNIQUE(run_id, sequence_num)
);

CREATE INDEX idx_runs_status ON runs(status);
CREATE INDEX idx_executions_run ON executions(run_id);
```

### Go Types

```go
type Run struct {
    ID            int64
    CreatedAt     time.Time
    CompletedAt   *time.Time
    InitialPrompt string
    SpecName      string
    WorkspacePath string
    Status        RunStatus  // pending, running, complete, failed, stuck
    CurrentAgent  string
}

type Execution struct {
    ID              int64
    RunID           int64
    AgentName       string
    ClaudeSessionID string
    Status          ExecStatus
    ExitCode        *int
    StartedAt       *time.Time
    CompletedAt     *time.Time
    OutputSignal    map[string]any  // parsed JSON
    SequenceNum     int
}

type RunStatus string
const (
    RunPending  RunStatus = "pending"
    RunRunning  RunStatus = "running"
    RunComplete RunStatus = "complete"
    RunFailed   RunStatus = "failed"
    RunStuck    RunStatus = "stuck"  // needs human intervention
)
```

---

## Orchestration Spec Format

Specs are YAML files defining agents and transitions.

### Example: Code Review Loop

```yaml
name: code-review-loop
description: Iterative coding with review feedback

# Agent that receives the initial prompt
start: coder

# Agent definitions
agents:
  coder:
    definition: ./agents/coder.md      # path to agent markdown
    model: sonnet                       # optional, defaults to inherit
    output_schema:                      # expected structured output
      status:
        type: enum
        values: [DONE, BLOCKED]
      summary:
        type: string

  reviewer:
    definition: ./agents/reviewer.md
    model: opus
    output_schema:
      status:
        type: enum
        values: [APPROVED, CHANGES_REQUESTED, BLOCKED]
      issues:
        type: array
        optional: true

# Transition rules (evaluated in order, first match wins)
transitions:
  - from: coder
    to: reviewer
    # no 'when' clause = always transition after completion

  - from: reviewer
    to: END
    when:
      status: APPROVED

  - from: reviewer
    to: STUCK
    when:
      status: BLOCKED

  - from: reviewer
    to: coder
    # fallback: if no other transition matches

# Global settings
settings:
  max_iterations: 10           # prevent infinite loops
  workspace_template: git      # git | empty
```

### Spec Location

```
~/.shop/specs/           # user-level specs
.shop/specs/             # project-level specs (takes precedence)
```

---

## Workspace Structure

Each run gets an isolated workspace:

```
~/.shop/workspaces/run-{id}/
├── repo/                      # working directory (git worktree or clone)
│   └── ... (project files)
├── .agents/
│   ├── messages/              # sequential inter-agent notes
│   │   ├── 001-coder.md
│   │   ├── 002-reviewer.md
│   │   └── 003-coder.md
│   ├── signals/               # structured output for orchestrator
│   │   ├── coder.json
│   │   └── reviewer.json
│   └── scratchpad/            # private agent workspace
│       ├── coder/
│       └── reviewer/
└── .shop/
    └── run.json               # run metadata for agents to read
```

### run.json

```json
{
  "run_id": 42,
  "spec_name": "code-review-loop",
  "initial_prompt": "Refactor auth module to use JWT",
  "current_agent": "reviewer",
  "iteration": 2,
  "previous_agents": ["coder", "reviewer", "coder"]
}
```

---

## Agent Workspace Skill

A skill that teaches agents the workspace protocol. Installed in the workspace or user's skills directory.

### `.agents/SKILL.md`

```markdown
---
name: shop-protocol
description: Protocol for multi-agent orchestrated workflows. Use when .agents/ directory exists.
---

# Shop Workspace Protocol

You are one agent in a coordinated workflow. Other agents work on this 
codebase before and after you.

## Reading Context

1. Check `.shop/run.json` for run metadata and your role
2. Read `.agents/messages/*.md` in order for notes from previous agents
3. Your predecessors may have left context about decisions or blockers

## Leaving Context for Next Agent

Write to `.agents/messages/{NNN}-{your-role}.md`:
- Increment the number from the last message
- Be concise—what does the next agent need to know?
- Don't duplicate what's obvious from code or commits

## Signaling Completion

**IMPORTANT:** When your work is complete, write your decision to:
`.agents/signals/{your-role}.json`

Use the schema defined for your role. Example:
```json
{"status": "APPROVED", "summary": "Code looks good, no issues found"}
```

Valid status values depend on your role—check the orchestration spec.

## Private Workspace

Use `.agents/scratchpad/{your-role}/` for drafts, notes, or intermediate work.
No guarantee anyone reads this.

## Git Commits

Make atomic commits with clear messages. The commit history is part of
the communication trail. Don't squash—preserve the narrative.
```

---

## Shoptor Execution Loop

```go
func (o *Shoptor) Run(run *Run, spec *Spec) error {
    currentAgent := spec.Start
    iteration := 0

    for {
        iteration++
        if iteration > spec.Settings.MaxIterations {
            return o.transitionTo(run, "STUCK", "max iterations exceeded")
        }

        // Create execution record
        exec := o.createExecution(run, currentAgent, iteration)

        // Build prompt with context
        prompt := o.buildPrompt(spec, currentAgent, run)

        // Run agent
        sessionID, exitCode, err := o.runClaudeAgent(run.WorkspacePath, prompt)
        if err != nil {
            return o.failExecution(exec, err)
        }

        // Update execution record
        exec.ClaudeSessionID = sessionID
        exec.ExitCode = exitCode

        // Read structured output
        signal, err := o.readSignal(run.WorkspacePath, currentAgent)
        if err != nil {
            return o.transitionTo(run, "STUCK", "failed to read agent signal")
        }
        exec.OutputSignal = signal
        o.completeExecution(exec)

        // Evaluate transitions
        nextAgent := o.evaluateTransitions(spec, currentAgent, signal)

        if nextAgent == "END" {
            return o.completeRun(run)
        }
        if nextAgent == "STUCK" {
            return o.stuckRun(run, "agent signaled blocked")
        }

        currentAgent = nextAgent
    }
}

func (o *Shoptor) evaluateTransitions(spec *Spec, from string, signal map[string]any) string {
    for _, t := range spec.Transitions {
        if t.From != from {
            continue
        }
        if t.When == nil {
            // No condition = default transition
            return t.To
        }
        if o.matchesCondition(t.When, signal) {
            return t.To
        }
    }
    return "STUCK"  // No matching transition
}

func (o *Shoptor) matchesCondition(when map[string]any, signal map[string]any) bool {
    for key, expected := range when {
        actual, ok := signal[key]
        if !ok || actual != expected {
            return false
        }
    }
    return true
}
```

### Running Claude Agent

```go
func (o *Shoptor) runClaudeAgent(workspacePath, prompt string) (sessionID string, exitCode int, err error) {
    cmd := exec.Command("claude",
        "-p", prompt,
        "--output-format", "json",
        "--dangerously-skip-permissions",  // for unattended execution
    )
    cmd.Dir = filepath.Join(workspacePath, "repo")

    output, err := cmd.Output()
    exitCode = cmd.ProcessState.ExitCode()

    // Parse session ID from JSON output
    var result struct {
        SessionID string `json:"session_id"`
    }
    json.Unmarshal(output, &result)

    return result.SessionID, exitCode, err
}
```

---

## TUI Interface

Built with bubbletea. Three main views:

### 1. Run List View (default)

```
┌─ Shop ──────────────────────────────────────────────────┐
│                                                              │
│  Recent Runs                                                 │
│  ──────────                                                  │
│  ▶ #42 code-review-loop    ● running   coder (iter 2)       │
│    #41 code-review-loop    ✓ complete  3m ago               │
│    #40 simple-task         ✗ failed    1h ago               │
│    #39 code-review-loop    ✓ complete  2h ago               │
│                                                              │
│  [n] new run  [enter] view  [d] delete  [q] quit            │
└──────────────────────────────────────────────────────────────┘
```

### 2. New Run View

```
┌─ New Run ────────────────────────────────────────────────────┐
│                                                              │
│  Spec: [code-review-loop ▼]                                  │
│                                                              │
│  Prompt:                                                     │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ Refactor the authentication module to use JWT tokens   │  │
│  │ instead of session cookies. Ensure backward compat...  │  │
│  │                                                        │  │
│  └────────────────────────────────────────────────────────┘  │
│                                                              │
│  Repo: ~/projects/myapp  (branch: main)                      │
│                                                              │
│  [ctrl+enter] start  [esc] cancel                            │
└──────────────────────────────────────────────────────────────┘
```

### 3. Run Detail View

```
┌─ Run #42: code-review-loop ──────────────────────────────────┐
│                                                              │
│  Refactor authentication module to use JWT tokens            │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ START ──▶ coder ──▶ reviewer ──▶ coder ──▶ reviewer    │  │
│  │           ✓ 2m     ✗ CHANGES   ✓ 1m      ● running     │  │
│  │                       ▲                                │  │
│  │                    [selected]                          │  │
│  └────────────────────────────────────────────────────────┘  │
│                                                              │
│  reviewer (iteration 1)                                      │
│  ────────────────────────                                    │
│  Session: abc123-def456                                      │
│  Duration: 45s                                               │
│  Signal: {"status": "CHANGES_REQUESTED", "issues": [...]}    │
│                                                              │
│  [enter] open in claude  [m] messages  [s] signal  [q] back  │
└──────────────────────────────────────────────────────────────┘
```

### Key Bindings

| Key | Action |
|-----|--------|
| `↑/↓` | Navigate list / graph |
| `enter` | Select / open in Claude Code |
| `n` | New run |
| `q/esc` | Back / quit |

---

## CLI Interface

```bash
# Start TUI
shop

# Quick commands (no TUI)
shop run <spec> "<prompt>"     # start a run, print run ID
shop status <run-id>           # show run status
shop list                      # list recent runs
```

---

## Directory Structure

```
~/.shop/
├── shop.db             # SQLite database
├── specs/                   # orchestration specs
│   └── code-review-loop.yaml
└── workspaces/              # run workspaces
    ├── run-42/
    └── run-43/
```

---

## Implementation Checklist

- [ ] SQLite schema and Go models
- [ ] Spec parser (YAML → structs)
- [ ] Workspace creation (git worktree)
- [ ] Agent execution (shell out to `claude`)
- [ ] Transition evaluation
- [ ] Shop-workspace skill
- [ ] TUI: run list view
- [ ] TUI: new run view
- [ ] TUI: run detail view with execution graph
- [ ] TUI: open session in Claude Code (`--resume`)
