package lua

import (
	"bytes"
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
// runClaudeResult holds the output of a Claude CLI invocation.
type runClaudeResult struct {
	SessionID string
	ExitCode  int
	Stderr    string
	// ErrorResult is extracted from claude's JSON output when is_error is true
	ErrorResult string
}

func (r *Runtime) runClaude(claudeAgent, signalAgent, prompt, model string, execID int64) (*runClaudeResult, error) {
	// Set up MCP config so the agent can call report_signal
	if err := r.writeMCPConfig(signalAgent); err != nil {
		return nil, fmt.Errorf("failed to write MCP config: %w", err)
	}

	// Pre-generate session ID so we can resume the session while it's still running
	sessionID := uuid.New().String()

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--max-turns", "10",
		"--session-id", sessionID,
		"--mcp-config", r.ws.MCPConfigPath(),
	}

	if model != "" {
		args = append(args, "--model", model)
	}

	if claudeAgent != "" {
		args = append([]string{"--agent", claudeAgent}, args...)
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = r.ws.RepoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Store PID and session ID immediately so TUI can resume live sessions
	if cmd.Process != nil {
		r.storage.UpdateExecutionPID(execID, cmd.Process.Pid)
	}
	r.storage.UpdateExecutionSessionID(execID, sessionID)

	// Wait for completion
	result := &runClaudeResult{SessionID: sessionID}
	err := cmd.Wait()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	result.Stderr = stderr.String()

	// Parse JSON output to extract error messages
	if stdout.Len() > 0 {
		var output struct {
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}
		if json.Unmarshal(stdout.Bytes(), &output) == nil && output.IsError {
			result.ErrorResult = output.Result
		}
	}

	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			// Non-zero exit — not a Go-level error, caller inspects ExitCode + Stderr
			return result, nil
		}
		return nil, err
	}

	return result, nil
}

// writeMCPConfig writes mcp.json to the workspace root (outside the repo worktree).
func (r *Runtime) writeMCPConfig(signalAgent string) error {
	shopBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find shop binary: %w", err)
	}
	shopBin, err = filepath.EvalSymlinks(shopBin)
	if err != nil {
		return fmt.Errorf("failed to resolve shop binary path: %w", err)
	}

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"shop": map[string]any{
				"command": shopBin,
				"args":    []string{"mcp-server", "--agent", signalAgent, "--signal-dir", r.ws.SignalDir()},
			},
		},
	}

	data, err := json.MarshalIndent(mcpConfig, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(r.ws.MCPConfigPath(), data, 0644)
}
