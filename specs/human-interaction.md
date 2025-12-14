# Shop Human Interaction Specification

## Overview

Shop workflows can pause for human input. When this happens, humans interact directly through the Claude Code session - the same interface agents use. This keeps the interaction model simple: Claude sessions are the universal interface for both agents and humans.

## Design Principles

1. **Claude sessions are the interface**: No separate messaging system. Humans resume the agent's session.
2. **Context is built-in**: Session history shows what the agent tried, why it's stuck, and what it needs.
3. **Agents mediate**: The agent handles the human conversation and decides when it has enough to proceed.
4. **Minimal new concepts**: Same `run()` mechanics, just with human in the loop.

## Agent-Initiated Escalation

### Signal Convention

Agents can return `NEEDS_HUMAN` status when stuck:

```json
// .agents/signals/coder.json
{
  "status": "NEEDS_HUMAN",
  "reason": "Need clarification on authentication approach"
}
```

### Runtime Behavior

When `run(agent)` receives `NEEDS_HUMAN`:

```
run("coder")
  ↓
agent returns NEEDS_HUMAN
  ↓
execution marked: waiting_human
run marked: waiting_human
session_id stored
  ↓
Lua execution suspends
  ↓
[Human does: shop continue 42]
  ↓
claude --resume {session_id}
  ↓
Human chats with agent in Claude Code TUI
  - reviews context
  - provides guidance
  - asks questions
  ↓
Agent writes new signal (DONE, NEEDS_REVIEW, etc.)
  ↓
Human exits / agent signals complete
  ↓
Shop detects signal change
  ↓
Lua execution resumes
run("coder") returns final signal
```

### What the Human Sees

```bash
$ shop continue 42

Opening Claude session for: coder
Reason: Need clarification on authentication approach

# Claude Code TUI opens with full session history
```

The human is now in the agent's conversation. They can:
- Ask "what did you try?"
- Review files the agent modified
- Provide the missing information
- Watch the agent continue working
- Exit when done

### Signal Completion

The agent writes a new signal when it has what it needs:

```json
{
  "status": "DONE",
  "summary": "Implemented OAuth2 with PKCE per human guidance"
}
```

Shop watches for signal changes. When status is no longer `NEEDS_HUMAN`, the workflow continues.

### Automatic Handling in Lua

From the script's perspective, `run()` just takes longer:

```lua
function workflow(prompt)
  run("architect", prompt)
  run("coder")        -- might pause for human, script doesn't know or care
  run("reviewer")
end
```

The `run()` call blocks until the agent produces a terminal signal, regardless of how many human interactions happen in between.

### Configuration

```lua
config({
  human_escalation = true,    -- enable NEEDS_HUMAN handling (default: true)
  human_timeout = 86400,      -- seconds before marking stuck (default: 24h)
})
```

Disable for specific agents:

```lua
local result = run("autonomous-agent", { human = false })
if result.status == "NEEDS_HUMAN" then
  return stuck("Agent needed human but wasn't allowed: " .. result.reason)
end
```

## Explicit Checkpoints

### The `pause()` Function

For deliberate pause points (approval gates, phase reviews):

```lua
function workflow(prompt)
  run("architect", prompt)
  run("coder")
  
  local approval = pause("Approve deployment to production?")
  if not approval.continue then
    return stuck(approval.reason)
  end
  
  run("deployer")
end
```

### Implementation

`pause()` runs a built-in checkpoint agent:

```markdown
<!-- Built into shop, not a user-defined agent -->

The workflow has paused for human input.

**Checkpoint:** {{message}}

**What to do:**
1. Review the workspace state
2. Check recent changes and test results
3. Decide whether to continue or stop

When ready, write your decision:
- To continue: signal with status "CONTINUE"
- To stop: signal with status "STOP" and provide a reason
```

The human opens the session, reviews context, tells the agent their decision.

### Return Value

```lua
local approval = pause("Approve deployment?")
-- approval.continue = true/false
-- approval.reason = "..." (if stopped)
-- approval.message = "..." (optional note from human)
```

## Run States

```
running
  │
  ├──► waiting_human ──► running (signal changed)
  │         │
  │         └──────────► stuck (timeout or human stops)
  │
  ├──► completed
  ├──► stuck  
  └──► failed
```

## Execution Records

### Agent Escalation

When coder returns NEEDS_HUMAN and human helps:

| call_index | agent | status | notes |
|------------|-------|--------|-------|
| 3 | coder | completed | Final signal after human interaction |

Single execution record. The human interaction happens within the same Claude session - it's not a separate execution, just a longer one.

Session history contains the full agent + human conversation.

### Explicit Checkpoint

| call_index | agent | status |
|------------|-------|--------|
| 4 | _checkpoint | completed |

The `_checkpoint` agent handles the human interaction.

## CLI Commands

### List waiting runs

```bash
$ shop list
ID   SPEC      STATUS          AGENT    WAITING FOR
42   feature   waiting_human   coder    Need clarification on auth approach
46   deploy    waiting_human   -        Approve deployment to production?

$ shop list --active  # exclude completed
```

### Continue a run (open Claude session)

```bash
$ shop continue 42
Opening Claude session for: coder
Reason: Need clarification on authentication approach

# Launches: claude --resume {session_id}
```

### Stop a waiting run

```bash
$ shop stop 42 --reason "Decided to use different approach"
Run 42 marked as stuck: Decided to use different approach
```

### Check if run can continue

```bash
$ shop status 42
Run 42: waiting_human
Agent: coder
Session: abc-123-def
Reason: Need clarification on authentication approach
Waiting since: 10 minutes ago

Use 'shop continue 42' to open the Claude session.
```

## TUI Integration

### Run List

```
┌─ Shop Runs ──────────────────────────────────────────────┐
│                                                          │
│  ID   SPEC        STATUS          WAITING FOR            │
│  47   feature     ● running       -                      │
│  42   feature     ⏸ waiting       auth clarification     │
│  46   deploy      ⏸ waiting       deployment approval    │
│  45   bugfix      ✓ completed     -                      │
│                                                          │
│  [↑↓] Navigate  [Enter] Details  [c] Continue  [q] Quit  │
└──────────────────────────────────────────────────────────┘
```

### Run Detail (Waiting)

```
┌─ Run 42: feature ────────────────────────────────────────┐
│                                                          │
│  Status: ⏸ Waiting for human                             │
│  Agent: coder                                            │
│  Reason: Need clarification on authentication approach   │
│  Waiting: 10 minutes                                     │
│                                                          │
│  [c] Continue (open Claude)  [s] Stop  [o] Open editor   │
│                                                          │
│  ─────────────────────────────────────────────────────── │
│  Executions:                                             │
│  #1  architect   ✓  2m 15s                               │
│  #2  coder       ⏸  waiting (12m)                        │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

### Continue Action

Pressing `c` on a waiting run:

```
┌─ Continue Run 42 ────────────────────────────────────────┐
│                                                          │
│  Opening Claude session...                               │
│                                                          │
│  Agent: coder                                            │
│  Reason: Need clarification on authentication approach   │
│                                                          │
│  The Claude Code TUI will open in this terminal.         │
│  When done, exit Claude (Ctrl+C) to return to Shop.      │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

Then Claude Code takes over the terminal. On exit, back to Shop TUI.

## Resume Semantics

### Process Death During Wait

Shop can die while waiting. On restart:

1. `shop list` shows run as waiting_human
2. `shop continue 42` resumes the Claude session
3. Agent continues from where it was

The Claude session persists independently of shop.

### Lua Replay

On `shop resume 42`:

1. Start Lua VM
2. Replay `run()` calls using cached signals
3. Hit the waiting execution
4. Check if signal has changed:
   - Still NEEDS_HUMAN: restore waiting state
   - New signal: return it, continue workflow

```go
func (o *Orchestrator) runWithHumanHandling(agent string) Signal {
    for {
        exec := o.getOrCreateExecution(agent)
        
        if exec.Status == "completed" {
            return exec.Signal  // cached from previous run
        }
        
        if exec.Status == "waiting_human" {
            // Check if human has helped since we last looked
            signal := o.workspace.ReadSignal(agent)
            if signal.Status != "NEEDS_HUMAN" {
                o.completeExecution(exec, signal)
                return signal
            }
            // Still waiting - suspend Lua
            o.suspendForHuman(exec)
            return  // won't reach here, Lua suspended
        }
        
        // Run the agent
        sessionID := o.invokeAgent(agent)
        signal := o.workspace.ReadSignal(agent)
        
        if signal.Status == "NEEDS_HUMAN" {
            o.markWaitingHuman(exec, sessionID, signal.Reason)
            o.suspendForHuman(exec)
            return  // Lua suspended
        }
        
        o.completeExecution(exec, signal)
        return signal
    }
}
```

## Examples

### Simple workflow (escalation handled automatically)

```lua
function workflow(prompt)
  run("architect", prompt)
  run("coder")
  run("reviewer")
end
```

If coder returns NEEDS_HUMAN, workflow pauses. Human does `shop continue`, helps the agent, workflow resumes. Script doesn't need to know.

### Approval gate

```lua
function workflow(prompt)
  run("architect", prompt)
  run("coder")
  run("test-runner")
  
  local ok = pause("Approve for production deployment?")
  if not ok.continue then
    return stuck(ok.reason)
  end
  
  run("deployer")
end
```

### Disable escalation for autonomous agent

```lua
function workflow(prompt)
  local result = run("autonomous-agent", { human = false })
  if result.status == "NEEDS_HUMAN" then
    -- This agent shouldn't need help
    return stuck("Unexpected: " .. result.reason)
  end
end
```

### Multiple review phases

```lua
function workflow(prompt)
  run("architect", prompt)
  
  local arch_ok = pause("Approve architecture?")
  if not arch_ok.continue then
    return stuck("Architecture rejected: " .. arch_ok.reason)
  end
  
  for i = 1, 10 do
    run("coder")
    local review = run("reviewer")
    if review.status == "APPROVED" then
      break
    end
  end
  
  local deploy_ok = pause("Approve deployment?")
  if not deploy_ok.continue then
    return stuck("Deployment rejected: " .. deploy_ok.reason)
  end
  
  run("deployer")
end
```

## Future Enhancements

### Open workspace in editor

```bash
$ shop continue 42 --editor
# Opens VS Code / Cursor at workspace root
# Then opens Claude session
```

TUI shortcut: `o` to open editor, `c` to open Claude.

### Watch mode

```bash
$ shop watch 42
# Monitors signal file, auto-continues when human finishes
# Useful for running shop in background
```

### Notifications

```lua
config({
  notify_on_waiting = { "desktop", "slack" }
})
```

Push notification when workflow needs human attention.

### Timeout behavior

```lua
config({
  human_timeout = 3600,  -- 1 hour
  on_timeout = "stuck",  -- or "notify_again"
})
```

### Continue from CLI without TUI

```bash
# For scripts/CI - provide input without opening Claude
$ shop signal 42 --status CONTINUE --message "approved"
```

Writes signal directly, workflow continues. Useful for automated approvals.
