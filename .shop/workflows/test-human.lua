-- Test workflow for human interaction features
-- Tests both pause() and NEEDS_HUMAN signal handling

function workflow(prompt)
  log("Starting human interaction test")

  -- First, run a simple agent that completes
  log("Running test-worker agent...")
  local result = run("test-worker", prompt)
  log("test-worker returned: " .. result.status)

  -- Now test pause() - explicit checkpoint
  log("Testing pause()...")
  local ok = pause("Test checkpoint - approve to continue?")

  if not ok.continue then
    return stuck("User stopped at checkpoint: " .. (ok.reason or "no reason"))
  end

  log("Checkpoint approved, continuing...")

  -- Run another agent
  log("Running test-worker again...")
  local result2 = run("test-worker")
  log("test-worker returned: " .. result2.status)

  log("Test workflow complete!")
end
