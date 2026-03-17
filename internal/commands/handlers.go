package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mpataki/shop/internal/events"
	"github.com/mpataki/shop/internal/process"
	"github.com/mpataki/shop/internal/workflow"
	"github.com/mpataki/shop/internal/workspace"
)

func (p *Processor) handleStartRun(runID int64, cmd events.CommandRow) error {
	var payload StartRunPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return err
	}

	// Create workspace
	ws, err := workspace.Create(p.workspacesDir, runID, payload.SourceRepo)
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}

	// Emit RunStarted
	evt, _ := events.NewEvent(runID, events.EventRunStarted, events.RunStartedPayload{
		WorkflowPath:  payload.WorkflowPath,
		WorkflowName:  payload.WorkflowName,
		InitialPrompt: payload.InitialPrompt,
		WorkspacePath: ws.Path,
	})
	if _, err := p.appendEvents(runID, []events.Event{evt}); err != nil {
		return err
	}

	// Submit ExecuteWorkflow
	return p.submitInternalCommand(runID, CmdExecuteWorkflow, ExecuteWorkflowPayload{})
}

func (p *Processor) handleExecuteWorkflow(runID int64, cmd events.CommandRow) error {
	// Load current projection
	state, err := p.store.ProjectRunFromDB(runID)
	if err != nil {
		return err
	}

	// Create workflow runtime with deps
	deps := workflow.RuntimeDeps{
		Store:          p.store,
		State:          state,
		ProcessManager: p.processManager,
		WorkspacePath:  state.WorkspacePath,
		RepoPath:       filepath.Join(state.WorkspacePath, "repo"),
		EmitEvents: func(evts []events.Event) ([]events.Event, error) {
			return p.appendEvents(runID, evts)
		},
		DrainCommands: func() error {
			return p.drainPendingCommands(runID)
		},
		WriteMCPConfig: func(callIndex int, statuses []string) error {
			return WriteMCPConfig(state.WorkspacePath, p.store.DBPath(), runID, callIndex, statuses)
		},
	}

	rt := workflow.NewRuntime(deps)
	err = rt.Execute(state.WorkflowPath, state.InitialPrompt)

	if err == workflow.ErrWaitingHuman {
		info := rt.GetWaitingInfo()
		if info != nil {
			evt, _ := events.NewEvent(runID, events.EventRunWaitingHuman, events.RunWaitingHumanPayload{
				Reason:    info.Reason,
				CallIndex: info.CallIndex,
				SessionID: info.SessionID,
			})
			p.appendEvents(runID, []events.Event{evt})
		}
		return nil
	}

	if err != nil {
		if rt.IsStuck() {
			evt, _ := events.NewEvent(runID, events.EventRunStuck, events.RunStuckPayload{
				Reason: rt.StuckReason(),
			})
			p.appendEvents(runID, []events.Event{evt})
			return nil
		}
		evt, _ := events.NewEvent(runID, events.EventRunFailed, events.RunFailedPayload{
			Error: err.Error(),
		})
		p.appendEvents(runID, []events.Event{evt})
		return nil
	}

	// Success
	evt, _ := events.NewEvent(runID, events.EventRunCompleted, events.RunCompletedPayload{})
	_, appendErr := p.appendEvents(runID, []events.Event{evt})
	return appendErr
}

func (p *Processor) handleReportSignal(runID int64, cmd events.CommandRow) error {
	var payload ReportSignalPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return err
	}

	signal := payload.Signal
	if signal == nil {
		signal = map[string]any{
			"status":  payload.Status,
			"summary": payload.Summary,
		}
		if payload.Reason != "" {
			signal["reason"] = payload.Reason
		}
	}

	evt, _ := events.NewEvent(runID, events.EventSignalReceived, events.SignalReceivedPayload{
		CallIndex: payload.CallIndex,
		Signal:    signal,
	})
	_, err := p.appendEvents(runID, []events.Event{evt})
	return err
}

func (p *Processor) handleResumeRun(runID int64, cmd events.CommandRow) error {
	evt, _ := events.NewEvent(runID, events.EventRunResumed, events.RunResumedPayload{})
	if _, err := p.appendEvents(runID, []events.Event{evt}); err != nil {
		return err
	}
	return p.submitInternalCommand(runID, CmdExecuteWorkflow, ExecuteWorkflowPayload{})
}

func (p *Processor) handleKillRun(runID int64, cmd events.CommandRow) error {
	state, err := p.store.ProjectRunFromDB(runID)
	if err != nil {
		return err
	}

	if pid := state.ActivePID(); pid > 0 {
		p.processManager.Kill(pid)
	}

	evt, _ := events.NewEvent(runID, events.EventRunKilled, events.RunKilledPayload{})
	_, err = p.appendEvents(runID, []events.Event{evt})
	return err
}

func (p *Processor) handleStopRun(runID int64, cmd events.CommandRow) error {
	var payload StopRunPayload
	json.Unmarshal(cmd.Payload, &payload)

	state, err := p.store.ProjectRunFromDB(runID)
	if err != nil {
		return err
	}
	if state.Status != events.RunStatusWaitingHuman {
		return fmt.Errorf("run %d is not waiting for human input (status: %s)", runID, state.Status)
	}

	reason := payload.Reason
	if reason == "" {
		reason = "Stopped by user"
	}

	evt, _ := events.NewEvent(runID, events.EventRunStopped, events.RunStoppedPayload{Reason: reason})
	_, err = p.appendEvents(runID, []events.Event{evt})
	return err
}

func (p *Processor) handleDeleteRun(runID int64, cmd events.CommandRow) error {
	state, err := p.store.ProjectRunFromDB(runID)
	if err != nil {
		return err
	}

	// Clean up workspace
	if state.WorkspacePath != "" {
		repoPath := state.WorkspacePath + "/repo"
		branchName := fmt.Sprintf("shop/run-%d", runID)

		sourceRepo := findSourceRepo(repoPath)
		if sourceRepo != "" {
			gitCmd := exec.Command("git", "worktree", "remove", "--force", repoPath)
			gitCmd.Dir = sourceRepo
			gitCmd.CombinedOutput()

			gitCmd = exec.Command("git", "branch", "-D", branchName)
			gitCmd.Dir = sourceRepo
			gitCmd.CombinedOutput()
		}

		trashCmd := exec.Command("trash", state.WorkspacePath)
		if err := trashCmd.Run(); err != nil {
			os.RemoveAll(state.WorkspacePath)
		}
	}

	evt, _ := events.NewEvent(runID, events.EventRunDeleted, events.RunDeletedPayload{})
	_, err = p.appendEvents(runID, []events.Event{evt})
	return err
}

func (p *Processor) handleProvideHumanInput(runID int64, cmd events.CommandRow) error {
	var payload ProvideHumanInputPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return err
	}

	state, err := p.store.ProjectRunFromDB(runID)
	if err != nil {
		return err
	}
	if state.Status != events.RunStatusWaitingHuman {
		return fmt.Errorf("run %d is not waiting for human input", runID)
	}

	evt, _ := events.NewEvent(runID, events.EventHumanInputReceived, events.HumanInputReceivedPayload{
		CallIndex: payload.CallIndex,
		Signal:    payload.Signal,
	})
	if _, err := p.appendEvents(runID, []events.Event{evt}); err != nil {
		return err
	}

	return p.submitInternalCommand(runID, CmdResumeRun, ResumeRunPayload{})
}

// ContinueRun returns session ID and work dir for a waiting run.
func (p *Processor) ContinueRun(runID int64) (sessionID string, workDir string, err error) {
	state, err := p.store.ProjectRunFromDB(runID)
	if err != nil {
		return "", "", err
	}
	if state.Status != events.RunStatusWaitingHuman {
		return "", "", fmt.Errorf("run %d is not waiting for human input (status: %s)", runID, state.Status)
	}
	if state.WaitingSessionID == "" {
		return "", "", fmt.Errorf("run %d has no session ID to resume", runID)
	}
	return state.WaitingSessionID, filepath.Join(state.WorkspacePath, "repo"), nil
}

// TryResumeAfterHuman checks if a waiting run's signal changed and auto-resumes.
func (p *Processor) TryResumeAfterHuman(runID int64) error {
	state, err := p.store.ProjectRunFromDB(runID)
	if err != nil || state.Status != events.RunStatusWaitingHuman {
		return nil
	}

	// Find the waiting execution
	for _, exec := range state.Executions {
		if exec.Status == events.ExecStatusWaitingHuman {
			if exec.Signal != nil {
				if status, _ := exec.Signal["status"].(string); status != string(events.SignalNeedsHuman) {
					// Signal changed — submit ProvideHumanInput
					cmd, err := NewCommand(runID, CmdProvideHumanInput, ProvideHumanInputPayload{
						CallIndex: exec.CallIndex,
						Signal:    exec.Signal,
					})
					if err != nil {
						return err
					}
					p.ensureRunGoroutine(runID)
					return p.SubmitCommand(cmd)
				}
			}
			break
		}
	}

	return nil
}

// ResumeSession opens a Claude session in interactive mode.
func ResumeSession(sessionID string) error {
	cmd := exec.Command("claude", "--resume", sessionID)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}

// findSourceRepo extracts the main repo path from a worktree's .git file.
func findSourceRepo(worktreePath string) string {
	gitFile := filepath.Join(worktreePath, ".git")
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	content := string(data)
	if !strings.HasPrefix(content, "gitdir: ") {
		return ""
	}
	gitDir := strings.TrimSpace(content[8:])
	idx := strings.LastIndex(gitDir, "/.git/")
	if idx == -1 {
		return ""
	}
	return gitDir[:idx]
}

// GetStore returns the underlying event store.
func (p *Processor) GetStore() *events.Store {
	return p.store
}

// ProcessManager returns the underlying process manager.
func (p *Processor) ProcessManager() process.Manager {
	return p.processManager
}
