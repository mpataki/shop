package lua

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// runClaude executes the Claude CLI and returns the session ID and exit code.
// claudeAgent is the --agent flag (agent .md file name, empty for no agent mode).
// signalAgent is the name used for the MCP signal file.
func (r *Runtime) runClaude(claudeAgent, signalAgent, prompt string, execID int64) (sessionID string, exitCode int, err error) {
	// Set up MCP config so the agent can call report_signal
	if err := r.writeMCPConfig(signalAgent); err != nil {
		return "", 0, fmt.Errorf("failed to write MCP config: %w", err)
	}

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--max-turns", "10",
	}

	if claudeAgent != "" {
		args = append([]string{"--agent", claudeAgent}, args...)
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

// writeMCPConfig writes .mcp.json to the workspace so Claude discovers the Shop MCP server.
func (r *Runtime) writeMCPConfig(signalAgent string) error {
	shopBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find shop binary: %w", err)
	}
	shopBin, err = filepath.EvalSymlinks(shopBin)
	if err != nil {
		return fmt.Errorf("failed to resolve shop binary path: %w", err)
	}

	signalDir := filepath.Join(r.ws.RepoPath, ".agents", "signals")

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"shop": map[string]any{
				"command": shopBin,
				"args":    []string{"mcp-server", "--agent", signalAgent, "--signal-dir", signalDir},
			},
		},
	}

	data, err := json.MarshalIndent(mcpConfig, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(r.ws.RepoPath, ".mcp.json"), data, 0644)
}
