// Example workflow spec for shop
// Demonstrates the basic workflow API

function workflow(prompt) {
  log("Starting example workflow");

  run("architect", { prompt, model: "sonnet" });

  log("Architect complete, starting coder");

  // Review loop: code until approved or max iterations
  for (let i = 1; i <= 5; i++) {
    log(`Iteration ${i} of review loop`);

    run("coder", { model: "sonnet" });

    const review = run("reviewer", { model: "sonnet", statuses: ["APPROVED", "CHANGES_REQUESTED"] });
    if (review.status === "APPROVED") {
      log("Changes approved!");
      return;
    }

    // CHANGES_REQUESTED - continue loop
    log("Changes requested, continuing...");
  }

  stuck("max iterations exceeded");
}
