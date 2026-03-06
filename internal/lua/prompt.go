package lua

import "fmt"

// buildAgentPrompt constructs the prompt for a regular agent invocation.
func (r *Runtime) buildAgentPrompt(agent, prompt string) string {
	result := prompt
	if result == "" {
		result = r.run.InitialPrompt
	}

	// Direct agent to read context file for history
	if r.callIndex > 1 {
		result += "\n\n---\n"
		result += "IMPORTANT: Read `.agents/context.md` for context from previous agents before starting work."
	}

	result += fmt.Sprintf("\n\nYou are the '%s' agent in the '%s' workflow.", agent, r.run.WorkflowName)

	// Add signal file instructions
	result += "\n\n---\n"
	result += "IMPORTANT: When you have completed your task, you MUST write a JSON signal file.\n\n"
	result += "Write to: .agents/signals/" + agent + ".json\n\n"
	result += "Example:\n```json\n{\"status\": \"DONE\", \"summary\": \"Completed the task.\"}\n```\n"

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

When ready, write your decision to .agents/signals/_checkpoint.json:

To continue:
`+"```json\n{\"status\": \"CONTINUE\", \"message\": \"Your optional note here\"}\n```"+`

To stop:
`+"```json\n{\"status\": \"STOP\", \"reason\": \"Reason for stopping\"}\n```"+`

Wait for the human to provide guidance before writing your decision.`, message)
}
