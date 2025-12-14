package orchestrator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	shopLua "github.com/mpataki/shop/internal/lua"
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

// StartRun creates a new run for a Lua workflow
func (o *Orchestrator) StartRun(specPath, specName, prompt, sourceRepo string) (*models.Run, error) {
	// Create run record
	run := &models.Run{
		InitialPrompt: prompt,
		SpecName:      specName,
		SpecPath:      specPath,
		Status:        models.RunStatusPending,
	}

	runID, err := o.storage.CreateRun(run)
	if err != nil {
		return nil, fmt.Errorf("failed to create run: %w", err)
	}
	run.ID = runID

	// Create workspace
	ws, err := workspace.Create(o.workspaceDir, runID, sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	run.WorkspacePath = ws.Path
	if err := o.storage.UpdateRun(run); err != nil {
		return nil, fmt.Errorf("failed to update run with workspace path: %w", err)
	}

	// Initialize context file
	if err := ws.InitContext(specName, prompt); err != nil {
		return nil, fmt.Errorf("failed to initialize context: %w", err)
	}

	return run, nil
}

// Execute runs a Lua workflow
func (o *Orchestrator) Execute(run *models.Run) error {
	ws, err := workspace.Open(o.workspaceDir, run.ID)
	if err != nil {
		return err
	}

	// Update run status to running
	run.Status = models.RunStatusRunning
	if err := o.storage.UpdateRun(run); err != nil {
		return err
	}

	// Create and execute the Lua runtime
	runtime := shopLua.NewRuntime(o.storage, run, ws)
	err = runtime.Execute(run.SpecPath, run.InitialPrompt)

	// Log any messages from the workflow
	for _, log := range runtime.GetLogs() {
		fmt.Printf("[lua] %s\n", log)
	}

	if err != nil {
		// Mark run as failed if not already stuck
		if run.Status != models.RunStatusStuck {
			now := time.Now()
			run.Status = models.RunStatusFailed
			run.CompletedAt = &now
			run.Error = err.Error()
			o.storage.UpdateRun(run)
		}
		return err
	}

	return nil
}

// Resume resumes a Lua workflow from where it left off
func (o *Orchestrator) Resume(runID int64) error {
	run, err := o.storage.GetRun(runID)
	if err != nil {
		return fmt.Errorf("failed to get run: %w", err)
	}

	if run.SpecPath == "" {
		return fmt.Errorf("run %d is not a Lua workflow", runID)
	}

	// Reset status to running
	run.Status = models.RunStatusRunning
	run.CompletedAt = nil
	if err := o.storage.UpdateRun(run); err != nil {
		return err
	}

	return o.Execute(run)
}

// ResumeSession opens a Claude session in interactive mode
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
