-- Simple linear workflow
-- Runs three agents in sequence

function workflow(prompt)
  log("Starting simple workflow")

  run("architect", prompt)
  run("coder")
  run("reviewer")

  log("Workflow complete")
end
