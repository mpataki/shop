package lua

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/google/uuid"
)

// runClaude executes the Claude CLI and returns the session ID and exit code.
// claudeAgent is the --agent flag (agent .md file name, empty for no agent mode).
// signalAgent is the name used for the MCP signal file.
func (r *Runtime) runClaude(claudeAgent, signalAgent, prompt string, execID int64) (sessionID string, exitCode int, err error) {
	// Set up MCP config so the agent can call report_signal
	if err := r.writeMCPConfig(signalAgent); err != nil {
		return "", 0, fmt.Errorf("failed to write MCP config: %w", err)
	}

	// Pre-generate session ID so we can resume the session while it's still running
	sessionID = uuid.New().String()

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--max-turns", "10",
		"--session-id", sessionID,
	}

	if claudeAgent != "" {
		args = append([]string{"--agent", claudeAgent}, args...)
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = r.ws.RepoPath

	if err := cmd.Start(); err != nil {
		return "", 0, err
	}

	// Store PID and session ID immediately so TUI can resume live sessions
	if cmd.Process != nil {
		r.storage.UpdateExecutionPID(execID, cmd.Process.Pid)
	}
	r.storage.UpdateExecutionSessionID(execID, sessionID)

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
