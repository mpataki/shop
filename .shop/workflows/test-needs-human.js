// Test workflow for STUCK signal
// Tests agent-initiated escalation

function workflow(prompt) {
  log("Starting STUCK test");

  // Run an agent that will return STUCK
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
