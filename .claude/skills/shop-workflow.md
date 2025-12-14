---
name: shop-workflow
description: Create Lua workflow scripts for Shop agent orchestration
---

# Shop Workflow Authoring

You are creating a Lua workflow script for Shop, a Claude agent orchestration system. Workflows define how multiple Claude Code agents work together to accomplish a task.

## Workflow Structure

Every workflow is a `.lua` file that defines a `workflow(prompt)` function:

```lua
function workflow(prompt)
  -- Your orchestration logic here
end
```

Place workflow files in `.shop/specs/` (project) or `~/.shop/specs/` (user).

## Available API

### `run(agent, prompt?) -> signal`

Execute a Claude Code agent and get its result.

- `agent`: Name of agent (must exist as `.claude/agents/{agent}.md`)
- `prompt`: Optional prompt (typically only for first agent; others read context)
- Returns: Table with `status` field plus any additional fields the agent wrote

```lua
local result = run("coder", prompt)
if result.status == "DONE" then
  -- success
end
```

### `pause(message) -> result`

Pause workflow for human approval (explicit checkpoint).

- `message`: Description of what needs approval
- Returns: `{continue: bool, reason: string, message: string}`

```lua
local ok = pause("Approve deployment to production?")
if not ok.continue then
  return stuck(ok.reason)
end
```

### `stuck(reason?)`

End workflow in a blocked state.

- `reason`: Optional explanation for why workflow cannot proceed

```lua
if result.status == "BLOCKED" then
  return stuck(result.reason)
end
```

### `context() -> table`

Get information about the current run.

- Returns: `{run_id, repo, iteration, prompt}`

```lua
local ctx = context()
log("Iteration " .. ctx.iteration .. " of run " .. ctx.run_id)
```

### `log(message)`

Write to run log (visible in TUI).

```lua
log("Starting review phase")
```

## Signal Handling

Agents write JSON signals to `.agents/signals/{agent}.json`. Common statuses:

| Status | Meaning |
|--------|---------|
| `DONE` | Task completed successfully |
| `APPROVED` | Review passed |
| `CHANGES_REQUESTED` | Review found issues, continue iterating |
| `BLOCKED` | Cannot proceed, needs intervention |
| `NEEDS_HUMAN` | Agent needs human input (workflow pauses) |

### Handling NEEDS_HUMAN

When an agent returns `NEEDS_HUMAN`, the workflow automatically pauses:

```lua
local result = run("coder")
-- If coder returns {status: "NEEDS_HUMAN", reason: "..."},
-- workflow pauses until human helps via 'shop continue'
```

After human interaction, use `shop resume` to continue the workflow.

## Workflow Patterns

### Simple Linear

```lua
function workflow(prompt)
  run("architect", prompt)
  run("coder")
  run("reviewer")
end
```

### Review Loop

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

### With Human Approval Gate

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

### Multiple Review Phases

```lua
function workflow(prompt)
  run("architect", prompt)

  -- Architecture review
  local arch_ok = pause("Approve architecture?")
  if not arch_ok.continue then
    return stuck("Architecture rejected: " .. arch_ok.reason)
  end

  -- Implementation loop
  for i = 1, 10 do
    run("coder")
    local review = run("reviewer")
    if review.status == "APPROVED" then
      break
    end
  end

  -- Deployment approval
  local deploy_ok = pause("Approve deployment?")
  if not deploy_ok.continue then
    return stuck("Deployment rejected: " .. deploy_ok.reason)
  end

  run("deployer")
end
```

### Multiple Independent Reviews

```lua
function workflow(prompt)
  run("coder", prompt)

  local security = run("security-reviewer")
  local perf = run("perf-reviewer")
  local style = run("style-reviewer")

  local issues = {}
  if security.status ~= "APPROVED" then
    table.insert(issues, "security: " .. (security.reason or "failed"))
  end
  if perf.status ~= "APPROVED" then
    table.insert(issues, "performance: " .. (perf.reason or "failed"))
  end
  if style.status ~= "APPROVED" then
    table.insert(issues, "style: " .. (style.reason or "failed"))
  end

  if #issues > 0 then
    return stuck(table.concat(issues, "\n"))
  end
end
```

## Important Constraints

### Determinism Required

Workflows must be deterministic - same inputs produce same `run()` call sequence.

**Valid:**
- Branching on agent signal status
- Fixed iteration counts
- Loops with agent-controlled exit

**Invalid (sandbox prevents these):**
- `math.random()` - removed from environment
- `os.time()` - `os` library not available
- `io.open()` - `io` library not available

### Sandbox Environment

Available:
- `pairs`, `ipairs`, `type`, `tostring`, `tonumber`, `error`
- `table.insert`, `table.remove`, `table.concat`
- `string` library
- `math` library (except `random`, `randomseed`)

Not available:
- `os`, `io`, `debug` libraries
- `loadfile`, `dofile`, `load`, `loadstring`
- `print` (use `log()` instead)

## Agent Requirements

Agents must:
1. Exist as `.claude/agents/{name}.md` in the repository
2. Write their signal to `.agents/signals/{name}.json`
3. Include a `status` field in their signal

Example agent signal:
```json
{
  "status": "DONE",
  "summary": "Implemented the feature successfully"
}
```

For human interaction, agent returns:
```json
{
  "status": "NEEDS_HUMAN",
  "reason": "Need clarification on authentication approach"
}
```

## CLI Usage

```bash
# Run workflow
shop run <spec-name> "task description"

# Continue waiting workflow (human interaction)
shop continue <run-id>

# Resume after human helped
shop resume <run-id>

# Stop waiting workflow
shop stop <run-id> --reason "Changed approach"

# View status
shop status <run-id>
shop list --active
```

## Best Practices

1. **Use descriptive agent names** - `security-reviewer` not `agent1`
2. **Handle all terminal statuses** - BLOCKED, APPROVED, etc.
3. **Set iteration limits** - Prevent infinite loops
4. **Use pause() for risky operations** - Deployments, data migrations
5. **Log important transitions** - Helps debugging
6. **Keep workflows simple** - Complex logic belongs in agents
