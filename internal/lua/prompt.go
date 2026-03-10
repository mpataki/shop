package lua

import (
	"fmt"
	"strings"

	"github.com/mpataki/shop/internal/models"
)

// buildAgentPrompt constructs the prompt for a regular agent invocation.
func (r *Runtime) buildAgentPrompt(agent, prompt string) string {
	result := prompt
	if result == "" {
		result = r.run.InitialPrompt
	}

	// Direct agent to fetch context from previous agents via MCP tool
	if r.callIndex > 1 {
		result += "\n\n---\n"
		result += "IMPORTANT: Call the `get_context` tool to retrieve context and summaries from previous agents before starting work."
	}

	result += fmt.Sprintf("\n\nYou are the '%s' agent in the '%s' workflow.", agent, r.run.WorkflowName)
	result += fmt.Sprintf("\nUse `%s` for drafts or intermediate work.", r.ws.ScratchpadPath(agent))

	result += "\n\n---\n"
	result += "IMPORTANT: When you have completed your task, you MUST call the `report_signal` tool to report your status.\n"
	result += "Valid statuses: " + strings.Join(models.ValidAgentStatusStrings(), ", ") + "\n"

	return result
}

// buildCheckpointPrompt constructs the prompt for a pause() checkpoint agent.
func (r *Runtime) buildCheckpointPrompt(message string) string {
	return fmt.Sprintf(`The workflow has paused for human input.

**Checkpoint:** %s

**What to do:**
1. Review the workspace state
2. Check recent changes and test results
3. Decide whether to continue or stop

When ready, call the report_signal tool with your decision:
- To continue: report_signal(status="%s", summary="your note")
- To stop: report_signal(status="%s", summary="reason for stopping")

Wait for the human to provide guidance before reporting your decision.`,
		message, models.SignalContinue, models.SignalStop)
}
