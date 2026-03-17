// Example workflow spec for shop
// Demonstrates the basic workflow API

function workflow(prompt) {
  log("Starting example workflow");

  // Run the architect agent with the initial prompt
  const arch = run("architect", { prompt, model: "sonnet" });
  if (arch.status === "BLOCKED") {
    return stuck(arch.reason || "architect blocked");
  }

  log("Architect complete, starting coder");

  // Review loop: code until approved or max iterations
  const ctx = context();
  for (let i = 1; i <= 5; i++) {
    log(`Iteration ${i} of review loop`);

    const code = run("coder", { model: "sonnet" });
    if (code.status === "BLOCKED") {
      return stuck(code.reason || "coder blocked");
    }

    const review = run("reviewer", { model: "sonnet" });
    if (review.status === "APPROVED") {
      log("Changes approved!");
      return;
    }
    if (review.status === "BLOCKED") {
      return stuck(review.reason || "reviewer blocked");
    }

    // CHANGES_REQUESTED - continue loop
    log("Changes requested, continuing...");
  }

  stuck("max iterations exceeded");
}
