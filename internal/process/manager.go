package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"syscall"

	"github.com/google/uuid"
)

// AgentOpts configures a Claude agent invocation.
type AgentOpts struct {
	ClaudeAgent   string // --agent flag (agent .md file name, empty for no agent mode)
	SignalAgent   string // name used for MCP signal identification
	Prompt        string
	Model         string
	WorkDir       string // working directory for the process
	MCPConfigPath string // path to mcp.json
}

// ProcessResult holds the outcome of a completed agent process.
type ProcessResult struct {
	SessionID   string
	PID         int
	ExitCode    int
	Stderr      string
	ErrorResult string // extracted from Claude's JSON output when is_error is true
}

// Manager abstracts starting and killing agent processes.
type Manager interface {
	StartAgent(ctx context.Context, opts AgentOpts) (sessionID string, pid int, done <-chan ProcessResult, err error)
	Kill(pid int) error
}

// CLIManager implements Manager by invoking the Claude CLI.
type CLIManager struct{}

func NewCLIManager() *CLIManager {
	return &CLIManager{}
}

func (m *CLIManager) StartAgent(ctx context.Context, opts AgentOpts) (string, int, <-chan ProcessResult, error) {
	sessionID := uuid.New().String()

	args := []string{
		"-p", opts.Prompt,
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--max-turns", "10",
		"--session-id", sessionID,
	}

	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	if opts.ClaudeAgent != "" {
		args = append([]string{"--agent", opts.ClaudeAgent}, args...)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = opts.WorkDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Set process group so we can kill children
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return "", 0, nil, fmt.Errorf("start claude: %w", err)
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	done := make(chan ProcessResult, 1)
	go func() {
		result := ProcessResult{SessionID: sessionID, PID: pid}
		err := cmd.Wait()
		if cmd.ProcessState != nil {
			result.ExitCode = cmd.ProcessState.ExitCode()
		}
		result.Stderr = stderr.String()

		// Parse JSON output for errors
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
			if _, ok := err.(*exec.ExitError); !ok {
				result.ErrorResult = err.Error()
			}
		}

		done <- result
		close(done)
	}()

	return sessionID, pid, done, nil
}

func (m *CLIManager) Kill(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
