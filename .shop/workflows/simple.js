// Simple linear workflow
// Runs three agents in sequence

function workflow(prompt) {
  log("Starting simple workflow");

  run("architect", { prompt, model: "sonnet" });
  run("coder", { model: "sonnet" });
  run("reviewer", { model: "sonnet" });

  log("Workflow complete");
}
