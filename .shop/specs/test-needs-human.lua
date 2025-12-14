-- Test workflow for NEEDS_HUMAN signal
-- This tests agent-initiated escalation

function workflow(prompt)
  log("Starting NEEDS_HUMAN test")

  -- Run an agent that will return NEEDS_HUMAN
  log("Running needs-help agent...")
  local result = run("needs-help", prompt)

  -- This should only execute after human helps
  log("needs-help returned: " .. result.status)

  if result.status == "DONE" then
    log("Agent completed after human assistance")
  else
    log("Unexpected status: " .. result.status)
  end

  log("Test complete!")
end
