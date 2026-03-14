package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mpataki/shop/internal/config"
	"github.com/mpataki/shop/internal/models"
	"github.com/mpataki/shop/internal/orchestrator"
)

type View int

const (
	ViewRunList View = iota
	ViewRunDetail
	ViewNewRun
	ViewOutput
)

type App struct {
	orchestrator *orchestrator.Orchestrator
	config       *config.Config

	view            View
	runs            []*models.Run
	selectedIdx     int
	selectedRun     *models.Run
	executions      []*models.Execution
	selectedExecIdx int
	outputContent   string

	workflows           []config.WorkflowInfo
	selectedWorkflowIdx int
	promptInput         textarea.Model
	focusOnPrompt       bool

	spinner spinner.Model
	logs    []string // recent activity log (ring buffer)
	width   int
	height  int
	err     error
}

const maxLogs = 6

func NewApp(orch *orchestrator.Orchestrator, cfg *config.Config) *App {
	ti := textarea.New()
	ti.Placeholder = "what should the agents do?"
	ti.CharLimit = 2000
	ti.MaxHeight = 0 // no max, grows freely
	ti.ShowLineNumbers = false
	ti.Prompt = "  "
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	ti.FocusedStyle.Base = lipgloss.NewStyle()
	ti.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ti.BlurredStyle.Prompt = lipgloss.NewStyle()
	ti.BlurredStyle.Base = lipgloss.NewStyle()
	ti.SetHeight(1)

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = statusRunningStyle

	return &App{
		orchestrator: orch,
		config:       cfg,
		view:         ViewRunList,
		promptInput:  ti,
		spinner:      sp,
	}
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.loadRuns, a.waitForEvent(), a.spinner.Tick)
}

// waitForEvent blocks on the orchestrator event channel and delivers events as tea messages.
func (a *App) waitForEvent() tea.Cmd {
	ch := a.orchestrator.Subscribe()
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return nil
		}
		return orchestratorEventMsg{event}
	}
}

type orchestratorEventMsg struct{ event *models.WorkflowEvent }

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		a.spinner, cmd = a.spinner.Update(msg)
		return a, cmd

	case tea.KeyMsg:
		return a.handleKey(msg)

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.promptInput.SetWidth(a.promptBoxInnerWidth())
		return a, nil

	case orchestratorEventMsg:
		e := msg.event
		var cmds []tea.Cmd
		cmds = append(cmds, a.waitForEvent())

		if line := a.formatEventLog(e); line != "" {
			a.appendLog(line)
		}

		switch e.Type {
		case models.WFEventLogMessage:
			// Log-only — no data reload needed.
		default:
			switch a.view {
			case ViewRunList:
				cmds = append(cmds, a.loadRuns)
			case ViewRunDetail:
				if a.selectedRun != nil && e.RunID == a.selectedRun.ID {
					cmds = append(cmds, a.loadRunDetail(a.selectedRun.ID))
				}
			}
		}
		return a, tea.Batch(cmds...)

	case runsLoadedMsg:
		a.runs = msg.runs
		a.err = msg.err
		return a, nil

	case runDetailMsg:
		a.selectedRun = msg.run
		a.executions = msg.executions
		a.err = msg.err
		if a.err == nil {
			a.view = ViewRunDetail
		}
		return a, nil

	case runKilledMsg:
		a.err = msg.err
		return a, a.loadRuns

	case sessionResumedMsg:
		if msg.err != nil {
			a.err = msg.err
		}
		a.view = ViewRunList
		cmds := []tea.Cmd{a.loadRuns}
		if msg.runID > 0 {
			cmds = append(cmds, a.tryResumeAfterHuman(msg.runID))
		}
		return a, tea.Batch(cmds...)

	case runDeletedMsg:
		a.err = msg.err
		if a.selectedIdx >= len(a.runs)-1 && a.selectedIdx > 0 {
			a.selectedIdx--
		}
		return a, a.loadRuns

	case runStoppedMsg:
		a.err = msg.err
		if a.view == ViewRunDetail && a.selectedRun != nil {
			return a, a.loadRunDetail(a.selectedRun.ID)
		}
		return a, a.loadRuns

	case outputLoadedMsg:
		if msg.err != nil {
			a.err = msg.err
		} else {
			a.outputContent = msg.content
			a.view = ViewOutput
		}
		return a, nil

	case workflowsLoadedMsg:
		a.workflows = msg.workflows
		a.err = msg.err
		if msg.err == nil {
			a.view = ViewNewRun
			a.selectedWorkflowIdx = 0
			a.focusOnPrompt = false
			a.promptInput.Reset()
			a.promptInput.SetHeight(1)
			a.promptInput.SetWidth(a.promptBoxInnerWidth())
			a.promptInput.Blur()
		}
		return a, nil

	case runStartedMsg:
		if msg.err != nil {
			a.err = msg.err
		} else {
			a.view = ViewRunList
		}
		return a, a.loadRuns
	}

	return a, nil
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch a.view {
	case ViewRunList:
		return a.handleRunListKey(msg)
	case ViewOutput:
		return a.handleOutputKey(msg)
	case ViewRunDetail:
		return a.handleRunDetailKey(msg)
	case ViewNewRun:
		return a.handleNewRunKey(msg)
	}
	return a, nil
}

func (a *App) handleRunListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return a, tea.Quit
	case "up", "k":
		if a.selectedIdx > 0 {
			a.selectedIdx--
		}
	case "down", "j":
		if a.selectedIdx < len(a.runs)-1 {
			a.selectedIdx++
		}
	case "enter", "l":
		if len(a.runs) > 0 && a.selectedIdx < len(a.runs) {
			return a, a.loadRunDetail(a.runs[a.selectedIdx].ID)
		}
	case "G":
		if len(a.runs) > 0 {
			a.selectedIdx = len(a.runs) - 1
		}
	case "g":
		a.selectedIdx = 0
	case "n":
		return a, a.enterNewRunView()
	case "x":
		if len(a.runs) > 0 && a.selectedIdx < len(a.runs) {
			return a, a.killRun(a.runs[a.selectedIdx].ID)
		}
	case "d":
		if len(a.runs) > 0 && a.selectedIdx < len(a.runs) {
			return a, a.deleteRun(a.runs[a.selectedIdx].ID)
		}
	case "c":
		if len(a.runs) > 0 && a.selectedIdx < len(a.runs) {
			run := a.runs[a.selectedIdx]
			if run.Status == models.RunStatusWaitingHuman && run.WaitingSessionID != "" {
				return a, a.continueSession(run.ID, run.WaitingSessionID, run.WorkspacePath+"/repo")
			}
		}
	}
	return a, nil
}

func (a *App) handleRunDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return a, tea.Quit
	case "esc", "h":
		a.view = ViewRunList
		a.selectedRun = nil
		a.executions = nil
		a.selectedExecIdx = 0
	case "up", "k":
		if a.selectedExecIdx > 0 {
			a.selectedExecIdx--
		}
	case "down", "j":
		if a.selectedExecIdx < len(a.executions)-1 {
			a.selectedExecIdx++
		}
	case "G":
		if len(a.executions) > 0 {
			a.selectedExecIdx = len(a.executions) - 1
		}
	case "g":
		a.selectedExecIdx = 0
	case "enter", "l":
		if len(a.executions) > 0 && a.selectedExecIdx < len(a.executions) {
			exec := a.executions[a.selectedExecIdx]
			if exec.ClaudeSessionID != "" && a.selectedRun != nil {
				workDir := filepath.Join(a.selectedRun.WorkspacePath, "repo")
				return a, a.resumeSession(exec.ClaudeSessionID, workDir)
			}
		}
	case "o":
		if len(a.executions) > 0 && a.selectedExecIdx < len(a.executions) {
			exec := a.executions[a.selectedExecIdx]
			if exec.ClaudeSessionID != "" && a.selectedRun != nil {
				return a, a.loadOutput(exec.ClaudeSessionID, a.selectedRun.WorkspacePath)
			}
		}
	case "c":
		if a.selectedRun != nil && a.selectedRun.Status == models.RunStatusWaitingHuman {
			if a.selectedRun.WaitingSessionID != "" {
				workDir := a.selectedRun.WorkspacePath + "/repo"
				return a, a.continueSession(a.selectedRun.ID, a.selectedRun.WaitingSessionID, workDir)
			}
		}
	case "s":
		if a.selectedRun != nil && a.selectedRun.Status == models.RunStatusWaitingHuman {
			return a, a.stopRun(a.selectedRun.ID)
		}
	}
	return a, nil
}

func (a *App) handleOutputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return a, tea.Quit
	case "esc", "h":
		a.view = ViewRunDetail
		a.outputContent = ""
	}
	return a, nil
}

func (a *App) handleNewRunKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.focusOnPrompt {
		switch msg.String() {
		case "ctrl+c":
			return a, tea.Quit
		case "esc":
			a.focusOnPrompt = false
			a.promptInput.Blur()
			return a, nil
		case "enter":
			if a.promptInput.Value() != "" && len(a.workflows) > 0 {
				return a, a.startNewRun()
			}
			return a, nil
		case "alt+enter":
			// Insert newline
			a.promptInput.InsertString("\n")
			a.resizePromptInput()
			return a, nil
		default:
			var cmd tea.Cmd
			a.promptInput, cmd = a.promptInput.Update(msg)
			a.resizePromptInput()
			return a, cmd
		}
	}

	switch msg.String() {
	case "ctrl+c":
		return a, tea.Quit
	case "esc":
		a.view = ViewRunList
	case "up", "k":
		if a.selectedWorkflowIdx > 0 {
			a.selectedWorkflowIdx--
		}
	case "down", "j":
		if a.selectedWorkflowIdx < len(a.workflows)-1 {
			a.selectedWorkflowIdx++
		}
	case "enter", "tab":
		if len(a.workflows) > 0 {
			a.focusOnPrompt = true
			return a, a.promptInput.Focus()
		}
	}
	return a, nil
}

// resizePromptInput adjusts the textarea height to fit its content (min 1 line).
func (a *App) resizePromptInput() {
	lines := strings.Count(a.promptInput.Value(), "\n") + 1
	if lines < 1 {
		lines = 1
	}
	a.promptInput.SetHeight(lines)
}

// ── Activity Log ──────────────────────────────────────────────────────────────

func (a *App) appendLog(line string) {
	a.logs = append(a.logs, line)
	if len(a.logs) > maxLogs {
		a.logs = a.logs[len(a.logs)-maxLogs:]
	}
}

func (a *App) formatEventLog(e *models.WorkflowEvent) string {
	switch e.Type {
	case models.WFEventAgentStarted:
		return fmt.Sprintf("#%d %s started", e.RunID, e.AgentName)
	case models.WFEventAgentCompleted:
		return fmt.Sprintf("#%d %s completed", e.RunID, e.AgentName)
	case models.WFEventAgentFailed:
		return fmt.Sprintf("#%d %s failed", e.RunID, e.AgentName)
	case models.WFEventRunCompleted:
		return fmt.Sprintf("#%d completed", e.RunID)
	case models.WFEventRunStuck:
		if p, ok := e.Payload.(models.RunStuckPayload); ok && p.Reason != "" {
			return fmt.Sprintf("#%d stuck: %s", e.RunID, p.Reason)
		}
		return fmt.Sprintf("#%d stuck", e.RunID)
	case models.WFEventRunFailed:
		if p, ok := e.Payload.(models.RunFailedPayload); ok && p.Error != "" {
			return fmt.Sprintf("#%d failed: %s", e.RunID, p.Error)
		}
		return fmt.Sprintf("#%d failed", e.RunID)
	case models.WFEventLogMessage:
		if p, ok := e.Payload.(models.LogMessagePayload); ok {
			return fmt.Sprintf("#%d %s", e.RunID, p.Message)
		}
	case models.WFEventCheckpointStarted:
		if p, ok := e.Payload.(models.CheckpointStartedPayload); ok {
			return fmt.Sprintf("#%d checkpoint: %s", e.RunID, p.Message)
		}
	case models.WFEventCheckpointResumed:
		return fmt.Sprintf("#%d checkpoint resumed", e.RunID)
	}
	return ""
}

// ── Messages ──────────────────────────────────────────────────────────────────

type runsLoadedMsg struct {
	runs []*models.Run
	err  error
}

type runDetailMsg struct {
	run        *models.Run
	executions []*models.Execution
	err        error
}

type runKilledMsg struct {
	runID int64
	err   error
}

type sessionResumedMsg struct {
	sessionID string
	runID     int64 // non-zero if this was a continue session (triggers auto-resume)
	err       error
}

type runDeletedMsg struct {
	runID int64
	err   error
}

type runStoppedMsg struct {
	runID int64
	err   error
}

type outputLoadedMsg struct {
	content string
	err     error
}

type workflowsLoadedMsg struct {
	workflows []config.WorkflowInfo
	err       error
}

type runStartedMsg struct {
	runID int64
	err   error
}

// ── Commands ──────────────────────────────────────────────────────────────────

func (a *App) loadRuns() tea.Msg {
	runs, err := a.orchestrator.ListRuns(20)
	return runsLoadedMsg{runs: runs, err: err}
}

func (a *App) loadRunDetail(id int64) tea.Cmd {
	return func() tea.Msg {
		run, err := a.orchestrator.GetRun(id)
		if err != nil {
			return runDetailMsg{err: err}
		}
		execs, err := a.orchestrator.GetExecutionsForRun(id)
		return runDetailMsg{run: run, executions: execs, err: err}
	}
}

func (a *App) killRun(id int64) tea.Cmd {
	return func() tea.Msg {
		if err := a.orchestrator.KillRun(id); err != nil {
			return runKilledMsg{err: err}
		}
		return runKilledMsg{runID: id}
	}
}

func (a *App) deleteRun(id int64) tea.Cmd {
	return func() tea.Msg {
		if err := a.orchestrator.DeleteRun(id); err != nil {
			return runDeletedMsg{err: err}
		}
		return runDeletedMsg{runID: id}
	}
}

func (a *App) stopRun(id int64) tea.Cmd {
	return func() tea.Msg {
		if err := a.orchestrator.StopRun(id, "Stopped from TUI"); err != nil {
			return runStoppedMsg{err: err}
		}
		return runStoppedMsg{runID: id}
	}
}

func (a *App) resumeSession(sessionID string, workDir string) tea.Cmd {
	cmd := exec.Command("claude", "--resume", sessionID)
	cmd.Dir = workDir
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return sessionResumedMsg{sessionID: sessionID, err: err}
	})
}

func (a *App) continueSession(runID int64, sessionID string, workDir string) tea.Cmd {
	cmd := exec.Command("claude", "--resume", sessionID)
	cmd.Dir = workDir
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return sessionResumedMsg{sessionID: sessionID, runID: runID, err: err}
	})
}

func (a *App) tryResumeAfterHuman(runID int64) tea.Cmd {
	return func() tea.Msg {
		a.orchestrator.TryResumeAfterHuman(runID)
		return nil // events drive TUI updates
	}
}

func (a *App) loadOutput(sessionID string, workspacePath string) tea.Cmd {
	return func() tea.Msg {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return outputLoadedMsg{err: err}
		}

		repoPath := filepath.Join(workspacePath, "repo")
		encodedPath := url.PathEscape(repoPath)
		sessionFile := filepath.Join(homeDir, ".claude", "projects", encodedPath, sessionID+".jsonl")

		file, err := os.Open(sessionFile)
		if err != nil {
			return outputLoadedMsg{err: fmt.Errorf("session file not found: %w", err)}
		}
		defer file.Close()

		var lastContent string
		scanner := bufio.NewScanner(file)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			var entry map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue
			}
			if entry["type"] == "assistant" {
				if msg, ok := entry["message"].(map[string]any); ok {
					if content, ok := msg["content"].([]any); ok {
						var text string
						for _, block := range content {
							if b, ok := block.(map[string]any); ok {
								if b["type"] == "text" {
									if t, ok := b["text"].(string); ok {
										text += t
									}
								}
							}
						}
						if text != "" {
							lastContent = text
						}
					}
				}
			}
			if entry["type"] == "summary" {
				if summary, ok := entry["summary"].(string); ok && lastContent == "" {
					lastContent = summary
				}
			}
		}

		if lastContent == "" {
			return outputLoadedMsg{content: "(no output found)"}
		}
		return outputLoadedMsg{content: lastContent}
	}
}

func (a *App) enterNewRunView() tea.Cmd {
	return func() tea.Msg {
		workflows, err := a.config.ListWorkflows()
		return workflowsLoadedMsg{workflows: workflows, err: err}
	}
}

func (a *App) startNewRun() tea.Cmd {
	return func() tea.Msg {
		if a.selectedWorkflowIdx >= len(a.workflows) {
			return runStartedMsg{err: fmt.Errorf("no workflow selected")}
		}
		wf := a.workflows[a.selectedWorkflowIdx]
		prompt := a.promptInput.Value()

		cwd, err := os.Getwd()
		if err != nil {
			return runStartedMsg{err: fmt.Errorf("failed to get working directory: %w", err)}
		}

		run, err := a.orchestrator.StartRun(wf.Path, wf.Name, prompt, cwd)
		if err != nil {
			return runStartedMsg{err: err}
		}

		go a.orchestrator.Execute(run)
		return runStartedMsg{runID: run.ID}
	}
}
