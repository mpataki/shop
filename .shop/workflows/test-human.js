// Test workflow for human interaction features
// Tests both pause() and STUCK signal handling

function workflow(prompt) {
  log("Starting human interaction test");

  // First, run a simple agent that completes
  log("Running test-worker agent...");
  const result = run("test-worker", { prompt, model: "sonnet" });
  log("test-worker returned: " + result.status);

  // Now test pause() - explicit checkpoint
  log("Testing pause()...");
  const ok = pause("Test checkpoint - approve to continue?");

  if (!ok.continue) {
    return stuck("User stopped at checkpoint: " + (ok.reason || "no reason"));
  }

  log("Checkpoint approved, continuing...");

  // Run another agent
  log("Running test-worker again...");
  const result2 = run("test-worker", { model: "sonnet" });
  log("test-worker returned: " + result2.status);

  log("Test workflow complete!");
}
