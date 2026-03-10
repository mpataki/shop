-- Example Lua workflow spec for shop
-- This demonstrates the basic workflow API

function workflow(prompt)
  log("Starting example workflow")

  -- Run the architect agent with the initial prompt
  local arch = run("architect", {prompt = prompt, model = "sonnet"})
  if arch.status == "BLOCKED" then
    return stuck(arch.reason or "architect blocked")
  end

  log("Architect complete, starting coder")

  -- Review loop: code until approved or max iterations
  local ctx = context()
  for i = 1, 5 do
    log("Iteration " .. i .. " of review loop")

    local code = run("coder", {model = "sonnet"})
    if code.status == "BLOCKED" then
      return stuck(code.reason or "coder blocked")
    end

    local review = run("reviewer", {model = "sonnet"})
    if review.status == "APPROVED" then
      log("Changes approved!")
      return -- success
    elseif review.status == "BLOCKED" then
      return stuck(review.reason or "reviewer blocked")
    end

    -- CHANGES_REQUESTED - continue loop
    log("Changes requested, continuing...")
  end

  stuck("max iterations exceeded")
end
