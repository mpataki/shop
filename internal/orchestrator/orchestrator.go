package orchestrator

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mpataki/shop/internal/models"
	"github.com/mpataki/shop/internal/storage"
	"github.com/mpataki/shop/internal/workspace"
)

type Orchestrator struct {
	storage      *storage.Storage
	workspaceDir string
}

func New(store *storage.Storage, workspaceDir string) *Orchestrator {
	return &Orchestrator{
		storage:      store,
		workspaceDir: workspaceDir,
	}
}

type RunResult struct {
	Run    *models.Run
	Status models.RunStatus
	Error  error
}

func (o *Orchestrator) StartRun(spec *models.Spec, prompt string, sourceRepo string) (*models.Run, error) {
	// Create run record
	run := &models.Run{
		InitialPrompt: prompt,
		SpecName:      spec.Name,
		Status:        models.RunStatusPending,
		CurrentAgent:  spec.Start,
	}

	runID, err := o.storage.CreateRun(run)
	if err != nil {
		return nil, fmt.Errorf("failed to create run: %w", err)
	}
	run.ID = runID

	// Create workspace (with git worktree if sourceRepo provided)
	ws, err := workspace.Create(o.workspaceDir, runID, sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	run.WorkspacePath = ws.Path
	if err := o.storage.UpdateRun(run); err != nil {
		return nil, fmt.Errorf("failed to update run with workspace path: %w", err)
	}

	// Initialize context file
	if err := ws.InitContext(spec.Name, prompt); err != nil {
		return nil, fmt.Errorf("failed to initialize context: %w", err)
	}

	return run, nil
}

func (o *Orchestrator) Execute(run *models.Run, spec *models.Spec) error {
	ws, err := workspace.Open(o.workspaceDir, run.ID)
	if err != nil {
		return err
	}

	currentAgent := spec.Start
	iteration := 0
	var previousAgents []string

	// Update run status to running
	run.Status = models.RunStatusRunning
	if err := o.storage.UpdateRun(run); err != nil {
		return err
	}

	for {
		iteration++
		if iteration > spec.Settings.MaxIterations {
			return o.stuckRun(run, "max iterations exceeded")
		}

		// Update run metadata
		run.CurrentAgent = currentAgent
		if err := o.storage.UpdateRun(run); err != nil {
			return err
		}

		// Write run.json for the agent
		meta := &workspace.RunMetadata{
			RunID:          run.ID,
			SpecName:       spec.Name,
			InitialPrompt:  run.InitialPrompt,
			CurrentAgent:   currentAgent,
			Iteration:      iteration,
			PreviousAgents: previousAgents,
		}
		if err := ws.WriteRunMetadata(meta); err != nil {
			return err
		}

		// Create scratchpad for this agent
		if err := ws.CreateAgentScratchpad(currentAgent); err != nil {
			return err
		}

		// Create execution record
		exec := &models.Execution{
			RunID:       run.ID,
			AgentName:   currentAgent,
			Status:      models.ExecStatusPending,
			SequenceNum: iteration,
		}
		execID, err := o.storage.CreateExecution(exec)
		if err != nil {
			return err
		}
		exec.ID = execID

		// Build prompt
		agentDef := spec.Agents[currentAgent]
		agentPrompt := o.buildPrompt(spec, currentAgent, run, agentDef, iteration == 1)

		// Run the agent
		now := time.Now()
		exec.StartedAt = &now
		exec.Status = models.ExecStatusRunning
		if err := o.storage.UpdateExecution(exec); err != nil {
			return err
		}

		sessionID, exitCode, err := o.runClaudeAgent(ws.RepoPath, currentAgent, agentPrompt, exec.ID)
		if err != nil {
			exec.Status = models.ExecStatusFailed
			o.storage.UpdateExecution(exec)
			return o.failRun(run, fmt.Sprintf("agent execution failed: %v", err))
		}

		// Update execution with results
		completedAt := time.Now()
		exec.ClaudeSessionID = sessionID
		exec.ExitCode = &exitCode
		exec.CompletedAt = &completedAt

		// Read signal
		signal, err := ws.ReadSignal(currentAgent)
		if err != nil {
			exec.Status = models.ExecStatusFailed
			o.storage.UpdateExecution(exec)
			return o.stuckRun(run, fmt.Sprintf("failed to read agent signal: %v", err))
		}
		exec.OutputSignal = signal
		exec.Status = models.ExecStatusComplete
		if err := o.storage.UpdateExecution(exec); err != nil {
			return err
		}

		// Append agent's output to context for next agent
		if err := ws.AppendContext(currentAgent, signal); err != nil {
			return fmt.Errorf("failed to append context: %w", err)
		}

		// Evaluate transitions
		nextAgent := o.evaluateTransitions(spec, currentAgent, signal)

		if nextAgent == "END" {
			return o.completeRun(run)
		}
		if nextAgent == "STUCK" {
			return o.stuckRun(run, "agent signaled blocked")
		}

		previousAgents = append(previousAgents, currentAgent)
		currentAgent = nextAgent
	}
}

func (o *Orchestrator) buildPrompt(spec *models.Spec, agentName string, run *models.Run, agentDef *models.AgentDef, isFirstAgent bool) string {
	prompt := run.InitialPrompt

	// Direct agent to read context file for history
	if !isFirstAgent {
		prompt += "\n\n---\n"
		prompt += "IMPORTANT: Read `.agents/context.md` for context from previous agents before starting work."
	}

	prompt += fmt.Sprintf("\n\nYou are the '%s' agent in the '%s' workflow.", agentName, spec.Name)

	// Add output schema expectations with explicit instructions
	if len(agentDef.OutputSchema) > 0 {
		prompt += "\n\n---\n"
		prompt += "IMPORTANT: When you have completed your task, you MUST write a JSON signal file.\n\n"
		prompt += "Write to: .agents/signals/" + agentName + ".json\n\n"

		// Build example based on schema
		example := make(map[string]any)
		for field, def := range agentDef.OutputSchema {
			if def.Type == "enum" && len(def.Values) > 0 {
				example[field] = def.Values[0]
			} else if def.Type == "string" {
				example[field] = "your summary here"
			} else if def.Type == "array" {
				example[field] = []string{}
			}
		}
		exampleJSON, _ := json.MarshalIndent(example, "", "  ")
		prompt += "Example:\n```json\n" + string(exampleJSON) + "\n```\n"

		// List valid values for enums
		for field, def := range agentDef.OutputSchema {
			if def.Type == "enum" && len(def.Values) > 0 {
				prompt += fmt.Sprintf("\nValid values for '%s': %v", field, def.Values)
			}
		}
	}

	return prompt
}

func (o *Orchestrator) runClaudeAgent(workDir, agentName, prompt string, execID int64) (sessionID string, exitCode int, err error) {
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--max-turns", "10",
	}

	// Use Claude Code's agent definition if it exists
	if agentName != "" {
		args = append([]string{"--agent", agentName}, args...)
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = workDir

	// Start the process
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", 0, err
	}

	if err := cmd.Start(); err != nil {
		return "", 0, err
	}

	// Store PID immediately
	if cmd.Process != nil {
		o.storage.UpdateExecutionPID(execID, cmd.Process.Pid)
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

func (o *Orchestrator) evaluateTransitions(spec *models.Spec, from string, signal map[string]any) string {
	for _, t := range spec.Transitions {
		if t.From != from {
			continue
		}
		if t.When == nil {
			// No condition = default transition
			return t.To
		}
		if o.matchesCondition(t.When, signal) {
			return t.To
		}
	}
	return "STUCK" // No matching transition
}

func (o *Orchestrator) matchesCondition(when map[string]any, signal map[string]any) bool {
	for key, expected := range when {
		actual, ok := signal[key]
		if !ok || actual != expected {
			return false
		}
	}
	return true
}

func (o *Orchestrator) completeRun(run *models.Run) error {
	now := time.Now()
	run.Status = models.RunStatusComplete
	run.CompletedAt = &now
	return o.storage.UpdateRun(run)
}

func (o *Orchestrator) failRun(run *models.Run, reason string) error {
	now := time.Now()
	run.Status = models.RunStatusFailed
	run.CompletedAt = &now
	return o.storage.UpdateRun(run)
}

func (o *Orchestrator) stuckRun(run *models.Run, reason string) error {
	now := time.Now()
	run.Status = models.RunStatusStuck
	run.CompletedAt = &now
	return o.storage.UpdateRun(run)
}

func (o *Orchestrator) ResumeSession(sessionID string) error {
	cmd := exec.Command("claude", "--resume", sessionID)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}

// Read methods for TUI

func (o *Orchestrator) ListRuns(limit int) ([]*models.Run, error) {
	return o.storage.ListRuns(limit)
}

func (o *Orchestrator) GetRun(id int64) (*models.Run, error) {
	return o.storage.GetRun(id)
}

func (o *Orchestrator) GetExecutionsForRun(runID int64) ([]*models.Execution, error) {
	return o.storage.GetExecutionsForRun(runID)
}

func (o *Orchestrator) KillRun(runID int64) error {
	run, err := o.storage.GetRun(runID)
	if err != nil {
		return fmt.Errorf("failed to get run: %w", err)
	}

	// Find running execution and kill its process
	runningExec, err := o.storage.GetRunningExecutionForRun(runID)
	if err != nil {
		return fmt.Errorf("failed to get running execution: %w", err)
	}

	if runningExec != nil && runningExec.PID != nil {
		// Kill the process group to ensure child processes are also killed
		syscall.Kill(-*runningExec.PID, syscall.SIGKILL)

		// Update execution status
		now := time.Now()
		runningExec.Status = models.ExecStatusFailed
		runningExec.CompletedAt = &now
		o.storage.UpdateExecution(runningExec)
	}

	// Update run status
	now := time.Now()
	run.Status = models.RunStatusFailed
	run.CompletedAt = &now
	return o.storage.UpdateRun(run)
}

func (o *Orchestrator) DeleteRun(runID int64) error {
	run, err := o.storage.GetRun(runID)
	if err != nil {
		return fmt.Errorf("failed to get run: %w", err)
	}

	repoPath := run.WorkspacePath + "/repo"
	branchName := fmt.Sprintf("shop/run-%d", runID)

	// Find the source repo from the worktree's .git file
	sourceRepo := o.findSourceRepo(repoPath)

	// Remove git worktree and branch if source repo found
	if sourceRepo != "" {
		// Remove worktree
		cmd := exec.Command("git", "worktree", "remove", "--force", repoPath)
		cmd.Dir = sourceRepo
		cmd.CombinedOutput() // Ignore errors

		// Delete the branch
		cmd = exec.Command("git", "branch", "-D", branchName)
		cmd.Dir = sourceRepo
		cmd.CombinedOutput() // Ignore errors
	}

	// Remove workspace directory
	if run.WorkspacePath != "" {
		os.RemoveAll(run.WorkspacePath)
	}

	// Delete from database
	return o.storage.DeleteRun(runID)
}

// findSourceRepo extracts the main repo path from a worktree's .git file
func (o *Orchestrator) findSourceRepo(worktreePath string) string {
	gitFile := filepath.Join(worktreePath, ".git")
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}

	// .git file contains: "gitdir: /path/to/main/.git/worktrees/run-N"
	content := string(data)
	if !strings.HasPrefix(content, "gitdir: ") {
		return ""
	}

	gitDir := strings.TrimSpace(content[8:])
	// Navigate up from .git/worktrees/run-N to the main repo
	// gitDir looks like: /path/to/repo/.git/worktrees/run-N
	// Find .git in the path and return everything before it
	idx := strings.LastIndex(gitDir, "/.git/")
	if idx == -1 {
		return ""
	}
	return gitDir[:idx]
}
