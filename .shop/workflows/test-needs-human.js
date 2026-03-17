// Test workflow for NEEDS_HUMAN signal
// Tests agent-initiated escalation

function workflow(prompt) {
  log("Starting NEEDS_HUMAN test");

  // Run an agent that will return NEEDS_HUMAN
  log("Running needs-help agent...");
  const result = run("needs-help", { prompt, model: "sonnet" });

  // This should only execute after human helps
  log("needs-help returned: " + result.status);

  if (result.status === "DONE") {
    log("Agent completed after human assistance");
  } else {
    log("Unexpected status: " + result.status);
  }

  log("Test complete!");
}
