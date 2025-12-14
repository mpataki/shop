# Shop Lua Workflow Specification

## Overview

Shop workflows are defined as Lua scripts that orchestrate Claude Code agents. The orchestrator executes the script, providing a `run()` function that invokes agents and returns their signals. Execution state is persisted to SQLite, enabling resume after crashes or interruptions.

## Goals

1. **Simple authoring**: Workflows read as straightforward imperative code
2. **Crash resilience**: Any workflow can resume from where it left off
3. **Minimal runtime**: Lua VM holds only local variables; Claude does real work
4. **Deterministic replay**: Same script + same signals = same execution path

## Lua Runtime

### Environment

Each workflow executes in a fresh Lua 5.1 VM (via gopher-lua). The environment is sandboxed:

**Available:**
- Basic Lua: `pairs`, `ipairs`, `type`, `tostring`, `tonumber`, `error`
- Tables: `table.insert`, `table.remove`, `table.concat`
- Strings: `string` library
- Math: `math` library (except `math.random`, `math.randomseed`)

**Not available:**
- `os` (no time, env, execute)
- `io` (no file access)
- `debug` (no introspection)
- `loadfile`, `dofile`, `load` (no dynamic code)
- `math.random` (breaks determinism)

### API

#### `run(agent: string, prompt?: string) -> signal`

Executes a Claude Code agent and returns its signal.

**Parameters:**
- `agent`: Name of agent (must exist in `.claude/agents/{agent}.md`)
- `prompt`: Optional initial prompt for the agent (typically only for first agent)

**Returns:**
- `signal`: Table containing fields from `.agents/signals/{agent}.json`
  - Always includes `status` (string)
  - May include additional fields like `reason`, `feedback`, etc.
  - Also includes `_session_id` (string) for debugging

**Behavior:**
1. Increments internal call index
2. Checks SQLite for completed execution at this index
3. If found and completed: returns cached signal (no agent invocation)
4. If found and running: checks for signal file, recovers or re-runs
5. Otherwise: creates execution record, runs agent, persists result

**Example:**
```lua
local review = run("reviewer")
if review.status == "APPROVED" then
  return
end
```

#### `stuck(reason?: string)`

Terminates workflow in a stuck/blocked state.

**Parameters:**
- `reason`: Optional explanation for why workflow cannot proceed

**Behavior:**
- Marks run as `stuck` in SQLite
- Stores reason for display in TUI
- Ends Lua execution

**Example:**
```lua
local code = run("coder")
if code.status == "BLOCKED" then
  return stuck(code.reason)
end
```

#### `context() -> table`

Returns information about the current run.

**Returns:**
- `run_id`: Integer run ID
- `repo`: Absolute path to workspace repository
- `iteration`: Current call index (number of `run()` calls so far)
- `prompt`: Original prompt passed to workflow

**Example:**
```lua
local ctx = context()
if ctx.iteration > 20 then
  return stuck("too many iterations")
end
```

#### `log(message: string)`

Writes a message to the run's log (visible in TUI).

**Parameters:**
- `message`: Human-readable status message

**Example:**
```lua
log("Starting security review phase")
run("security-reviewer")
```

### Workflow Function

Every spec must define a `workflow` function:

```lua
function workflow(prompt)
  -- orchestration logic here
end
```

**Parameters:**
- `prompt`: The user's initial prompt/task description

**Return behavior:**
- Normal return (no value): Run marked as `completed`
- Call `stuck(reason)`: Run marked as `stuck`
- Lua error: Run marked as `failed`

## SQLite Schema

### executions table

```sql
CREATE TABLE executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    call_index INTEGER NOT NULL,      -- position in script execution
    agent TEXT NOT NULL,
    prompt TEXT,                       -- prompt passed to this run() call
    status TEXT NOT NULL DEFAULT 'pending',  -- pending, running, completed, failed
    signal TEXT,                       -- JSON signal from agent
    session_id TEXT,                   -- Claude session ID
    pid INTEGER,                       -- process ID while running
    started_at DATETIME,
    completed_at DATETIME,
    
    UNIQUE(run_id, call_index)
);
```

### runs table (additions)

```sql
ALTER TABLE runs ADD COLUMN spec_path TEXT;      -- path to .lua file
ALTER TABLE runs ADD COLUMN initial_prompt TEXT; -- prompt passed to workflow()
```

## Resume Semantics

### On `shop run`

1. Create new run record
2. Create Lua VM with fresh call index (0)
3. Execute workflow function
4. Each `run()` creates new execution record, invokes agent

### On `shop resume <run_id>`

1. Load existing run record
2. Create Lua VM with fresh call index (0)
3. Execute workflow function
4. Each `run()`:
   - Increments call index
   - Queries `SELECT * FROM executions WHERE run_id = ? AND call_index = ?`
   - If completed: return cached signal, skip agent
   - If running: attempt recovery (check signal file), then re-run or return
   - If not found: create record, run agent

### On process death

Shop can die at any point. Run stays in `running` status with an execution in `running` status.

On resume:
- If signal file exists: Claude finished, we just missed it. Mark completed, return signal.
- If no signal file: Claude was killed too. Re-run from this execution.

Claude Code is stateless from shop's perspective - we don't resume Claude sessions, we re-run the agent if needed.

### Recovery pseudocode

```
for each run() call:
    index++
    exec = db.get(run_id, index)
    
    if exec is null:
        # Never started - run fresh
        exec = db.create(run_id, index, agent, "running")
        signal = invoke_claude(agent)
        db.complete(exec, signal)
        return signal
    
    if exec.status == "completed":
        # Already done - return cached
        return exec.signal
    
    if exec.status == "running":
        # Was in progress - check if Claude finished
        signal = try_read_signal(agent)
        if signal exists:
            db.complete(exec, signal)
            return signal
        else:
            # Re-run from scratch
            signal = invoke_claude(agent)
            db.complete(exec, signal)
            return signal
    
    if exec.status == "failed":
        # Previous attempt failed - retry
        db.update(exec, "running")
        signal = invoke_claude(agent)
        db.complete(exec, signal)
        return signal
```

## Determinism Constraint

**Rule: Workflow scripts must be deterministic.**

Given the same initial prompt and sequence of agent signals, the script must make the same sequence of `run()` calls.

**Valid patterns:**

```lua
-- Branching on agent output (deterministic on replay)
if review.status == "APPROVED" then
  run("deployer")
else
  run("coder")
end

-- Loops with agent-controlled exit
while true do
  local result = run("worker")
  if result.status == "DONE" then break end
end

-- Iteration counters (call_index handles this)
for i = 1, 10 do
  run("coder")
  run("reviewer")
end
```

**Invalid patterns:**

```lua
-- BROKEN: Random choice
if math.random() > 0.5 then
  run("fast-reviewer")
end

-- BROKEN: Time-based logic
if os.time() % 2 == 0 then
  run("morning-agent")
end

-- BROKEN: External state
local f = io.open("/tmp/flag")
if f then
  run("special-agent")
end
```

The sandbox prevents most non-determinism by removing `os`, `io`, and `math.random`.

**What happens on non-determinism:**

If replay diverges (e.g., script calls `run("coder")` but cached execution at that index was `reviewer`), the orchestrator:
1. Logs a warning
2. Invalidates remaining cached executions for this run
3. Continues with fresh execution

This is recovery, not normal operation.

## Error Handling

### Agent fails to produce signal

If Claude exits without writing a signal file:
- Execution marked as `failed`
- `run()` returns `{ status = "ERROR", reason = "no signal produced" }`
- Script can handle or propagate

### Lua error

If script calls `error()` or has a runtime error:
- Run marked as `failed`
- Error message stored in `runs.error`
- Displayed in TUI

### Agent timeout

Future: configurable timeout per agent. On timeout:
- Kill Claude process
- Mark execution `failed`
- Return error signal

## Examples

### Simple linear

```lua
function workflow(prompt)
  run("architect", prompt)
  run("coder")
  run("reviewer")
end
```

### Review loop

```lua
function workflow(prompt)
  run("architect", prompt)
  
  for i = 1, 10 do
    local code = run("coder")
    if code.status == "BLOCKED" then
      return stuck(code.reason)
    end
    
    local review = run("reviewer")
    if review.status == "APPROVED" then
      return  -- success
    elseif review.status == "BLOCKED" then
      return stuck(review.reason)
    end
    -- CHANGES_REQUESTED continues loop
  end
  
  stuck("max iterations exceeded")
end
```

### Parallel-ish review (sequential but independent)

```lua
function workflow(prompt)
  run("coder", prompt)
  
  local security = run("security-reviewer")
  local perf = run("perf-reviewer")
  local style = run("style-reviewer")
  
  local issues = {}
  if security.status ~= "APPROVED" then
    table.insert(issues, "security: " .. security.reason)
  end
  if perf.status ~= "APPROVED" then
    table.insert(issues, "performance: " .. perf.reason)
  end
  if style.status ~= "APPROVED" then
    table.insert(issues, "style: " .. style.reason)
  end
  
  if #issues > 0 then
    return stuck(table.concat(issues, "\n"))
  end
end
```

### Human checkpoint (future)

```lua
function workflow(prompt)
  run("architect", prompt)
  run("coder")
  
  local review = run("reviewer")
  if review.status == "NEEDS_HUMAN" then
    checkpoint("Please review the changes and approve")
    -- Workflow pauses here until human continues
  end
  
  run("deployer")
end
```

## File Structure

```
project/
├── .claude/
│   └── agents/
│       ├── architect.md
│       ├── coder.md
│       └── reviewer.md
├── .shop/
│   └── specs/
│       ├── feature.lua
│       └── bugfix.lua
└── src/
    └── ...
```

## CLI Integration

```bash
# Run a workflow
shop run feature "Add user authentication"

# Resume interrupted run
shop resume 42

# List runs
shop list

# Show run details including execution history
shop status 42
```

## TUI Integration

Run detail view shows:
- Each execution with call_index, agent, status, duration
- Cached vs fresh indicator
- Resume button if run is resumable (stuck in `running`)

## Future Considerations

### Parallel execution

```lua
-- Potential future syntax
local results = parallel(
  function() return run("security-reviewer") end,
  function() return run("perf-reviewer") end
)
```

Requires tracking parallel branches in execution log.

### Workflow composition

```lua
-- specs/review.lua
function review_loop()
  for i = 1, 10 do
    run("coder")
    if run("reviewer").status == "APPROVED" then
      return true
    end
  end
  return false
end

-- specs/feature.lua
local review = require("review")

function workflow(prompt)
  run("architect", prompt)
  if not review.review_loop() then
    return stuck("review failed")
  end
  run("deployer")
end
```

Requires careful call_index management across modules.

### Checkpoints

```lua
checkpoint("Review the architecture before proceeding")
```

Pauses workflow, notifies human, waits for explicit continue.
