package lua

import (
	"encoding/json"
	"io"
	"os/exec"
)

// runClaude executes the Claude CLI and returns the session ID and exit code.
func (r *Runtime) runClaude(agent, prompt string, execID int64) (sessionID string, exitCode int, err error) {
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--max-turns", "10",
	}

	if agent != "" {
		args = append([]string{"--agent", agent}, args...)
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = r.ws.RepoPath

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", 0, err
	}

	if err := cmd.Start(); err != nil {
		return "", 0, err
	}

	// Store PID
	if cmd.Process != nil {
		r.storage.UpdateExecutionPID(execID, cmd.Process.Pid)
	}

	// Read output
	output, _ := io.ReadAll(stdout)

	// Wait for completion
	err = cmd.Wait()
	exitCode = 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", 0, err
		}
	}

	// Parse session ID from JSON output
	var result struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(output, &result); err == nil {
		sessionID = result.SessionID
	}

	return sessionID, exitCode, nil
}
