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
	"github.com/mpataki/shop/internal/commands"
	"github.com/mpataki/shop/internal/config"
	"github.com/mpataki/shop/internal/events"
)

type View int

const (
	ViewRunList View = iota
	ViewRunDetail
	ViewNewRun
	ViewOutput
)

type App struct {
	processor *commands.Processor
	store     *events.Store
	config    *config.Config

	view            View
	runs            []*events.RunState
	selectedIdx     int
	selectedRun     *events.RunState
	selectedExecIdx int
	outputContent   string

	workflows           []config.WorkflowInfo
	selectedWorkflowIdx int
	promptInput         textarea.Model
	focusOnPrompt       bool

	spinner spinner.Model
	eventCh <-chan events.Event // single subscription, reused
	logs    []string            // recent activity log (ring buffer)
	width   int
	height  int
	err     error
}

const maxLogs = 6

func NewApp(proc *commands.Processor, store *events.Store, cfg *config.Config) *App {
	ti := textarea.New()
	ti.Placeholder = "what should the agents do?"
	ti.CharLimit = 2000
	ti.MaxHeight = 0
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
		processor:   proc,
		store:       store,
		config:      cfg,
		view:        ViewRunList,
		eventCh:     proc.Subscribe(),
		promptInput: ti,
		spinner:     sp,
	}
}

func (a *App) Init() tea.Cmd {
	a.reloadRuns()
	return tea.Batch(a.waitForEvent(), a.spinner.Tick)
}

func (a *App) waitForEvent() tea.Cmd {
	ch := a.eventCh
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return nil
		}
		return processorEventMsg{event}
	}
}

type processorEventMsg struct{ event events.Event }

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

	case processorEventMsg:
		e := msg.event

		if line := a.formatEventLog(e); line != "" {
			a.appendLog(line)
		}

		switch e.EventType {
		case events.EventLogMessage:
			// Log-only — no data reload needed.
		default:
			a.reloadRuns()
			if a.view == ViewRunDetail && a.selectedRun != nil && e.RunID == a.selectedRun.ID {
				a.selectedRun, _ = a.store.ProjectRunFromDB(a.selectedRun.ID)
			}
		}
		return a, a.waitForEvent()

	case runDetailMsg:
		a.selectedRun = msg.state
		a.err = msg.err
		if a.err == nil {
			a.view = ViewRunDetail
		}
		return a, nil

	case runKilledMsg:
		a.err = msg.err
		a.reloadRuns()
		return a, nil

	case sessionResumedMsg:
		if msg.err != nil {
			a.err = msg.err
		}
		a.view = ViewRunList
		a.reloadRuns()
		if msg.runID > 0 {
			return a, a.tryResumeAfterHuman(msg.runID)
		}
		return a, nil

	case runDeletedMsg:
		a.err = msg.err
		if a.selectedIdx >= len(a.runs)-1 && a.selectedIdx > 0 {
			a.selectedIdx--
		}
		a.reloadRuns()
		return a, nil

	case runStoppedMsg:
		a.err = msg.err
		if a.view == ViewRunDetail && a.selectedRun != nil {
			a.selectedRun, _ = a.store.ProjectRunFromDB(a.selectedRun.ID)
		}
		a.reloadRuns()
		return a, nil

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
		a.reloadRuns()
		return a, nil
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
			if run.Status == events.RunStatusWaitingHuman && run.WaitingSessionID != "" {
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
		a.selectedExecIdx = 0
		a.reloadRuns()
	case "up", "k":
		if a.selectedExecIdx > 0 {
			a.selectedExecIdx--
		}
	case "down", "j":
		if a.selectedRun != nil && a.selectedExecIdx < len(a.selectedRun.Executions)-1 {
			a.selectedExecIdx++
		}
	case "G":
		if a.selectedRun != nil && len(a.selectedRun.Executions) > 0 {
			a.selectedExecIdx = len(a.selectedRun.Executions) - 1
		}
	case "g":
		a.selectedExecIdx = 0
	case "enter", "l":
		if a.selectedRun != nil && len(a.selectedRun.Executions) > 0 && a.selectedExecIdx < len(a.selectedRun.Executions) {
			exec := a.selectedRun.Executions[a.selectedExecIdx]
			if exec.SessionID != "" {
				workDir := filepath.Join(a.selectedRun.WorkspacePath, "repo")
				return a, a.resumeSession(exec.SessionID, workDir)
			}
		}
	case "o":
		if a.selectedRun != nil && len(a.selectedRun.Executions) > 0 && a.selectedExecIdx < len(a.selectedRun.Executions) {
			exec := a.selectedRun.Executions[a.selectedExecIdx]
			if exec.SessionID != "" {
				return a, a.loadOutput(exec.SessionID, a.selectedRun.WorkspacePath)
			}
		}
	case "c":
		if a.selectedRun != nil && a.selectedRun.Status == events.RunStatusWaitingHuman {
			if a.selectedRun.WaitingSessionID != "" {
				workDir := a.selectedRun.WorkspacePath + "/repo"
				return a, a.continueSession(a.selectedRun.ID, a.selectedRun.WaitingSessionID, workDir)
			}
		}
	case "s":
		if a.selectedRun != nil && a.selectedRun.Status == events.RunStatusWaitingHuman {
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

func (a *App) formatEventLog(e events.Event) string {
	switch e.EventType {
	case events.EventAgentStarted:
		p, _ := events.DecodePayload[events.AgentStartedPayload](e)
		return fmt.Sprintf("#%d %s started", e.RunID, p.AgentName)
	case events.EventAgentCompleted:
		p, _ := events.DecodePayload[events.AgentCompletedPayload](e)
		return fmt.Sprintf("#%d %s completed", e.RunID, p.AgentName)
	case events.EventAgentFailed:
		p, _ := events.DecodePayload[events.AgentFailedPayload](e)
		return fmt.Sprintf("#%d %s failed", e.RunID, p.AgentName)
	case events.EventRunCompleted:
		return fmt.Sprintf("#%d completed", e.RunID)
	case events.EventRunStuck:
		p, _ := events.DecodePayload[events.RunStuckPayload](e)
		if p.Reason != "" {
			return fmt.Sprintf("#%d stuck: %s", e.RunID, p.Reason)
		}
		return fmt.Sprintf("#%d stuck", e.RunID)
	case events.EventRunFailed:
		p, _ := events.DecodePayload[events.RunFailedPayload](e)
		if p.Error != "" {
			return fmt.Sprintf("#%d failed: %s", e.RunID, p.Error)
		}
		return fmt.Sprintf("#%d failed", e.RunID)
	case events.EventLogMessage:
		p, _ := events.DecodePayload[events.LogMessagePayload](e)
		return fmt.Sprintf("#%d %s", e.RunID, p.Message)
	case events.EventCheckpointStarted:
		p, _ := events.DecodePayload[events.CheckpointStartedPayload](e)
		return fmt.Sprintf("#%d checkpoint: %s", e.RunID, p.Message)
	case events.EventCheckpointCompleted:
		return fmt.Sprintf("#%d checkpoint resumed", e.RunID)
	}
	return ""
}

// ── Messages ──────────────────────────────────────────────────────────────────

type runDetailMsg struct {
	state *events.RunState
	err   error
}

type runKilledMsg struct {
	runID int64
	err   error
}

type sessionResumedMsg struct {
	sessionID string
	runID     int64
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

func (a *App) reloadRuns() {
	infos, err := a.store.ListRunIDs(20)
	if err != nil {
		a.err = err
		return
	}

	var runs []*events.RunState
	for _, info := range infos {
		evts, err := a.store.GetEvents(info.ID)
		if err != nil {
			continue
		}
		state := events.ProjectRun(info.ID, info.CreatedAt, evts)
		if state.Status != events.RunStatusDeleted {
			runs = append(runs, state)
		}
	}

	a.runs = runs
}

func (a *App) loadRunDetail(id int64) tea.Cmd {
	return func() tea.Msg {
		state, err := a.store.ProjectRunFromDB(id)
		if err != nil {
			return runDetailMsg{err: err}
		}
		return runDetailMsg{state: state}
	}
}

func (a *App) killRun(id int64) tea.Cmd {
	return func() tea.Msg {
		cmd, err := commands.NewCommand(id, commands.CmdKillRun, commands.KillRunPayload{})
		if err != nil {
			return runKilledMsg{err: err}
		}
		a.processor.SubmitCommand(cmd)
		done := a.processor.ProcessRunSync(id)
		<-done
		return runKilledMsg{runID: id}
	}
}

func (a *App) deleteRun(id int64) tea.Cmd {
	return func() tea.Msg {
		cmd, err := commands.NewCommand(id, commands.CmdDeleteRun, commands.DeleteRunPayload{})
		if err != nil {
			return runDeletedMsg{err: err}
		}
		a.processor.SubmitCommand(cmd)
		done := a.processor.ProcessRunSync(id)
		<-done
		return runDeletedMsg{runID: id}
	}
}

func (a *App) stopRun(id int64) tea.Cmd {
	return func() tea.Msg {
		cmd, err := commands.NewCommand(id, commands.CmdStopRun, commands.StopRunPayload{Reason: "Stopped from TUI"})
		if err != nil {
			return runStoppedMsg{err: err}
		}
		a.processor.SubmitCommand(cmd)
		done := a.processor.ProcessRunSync(id)
		<-done
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
		a.processor.TryResumeAfterHuman(runID)
		return nil
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

		runID, err := a.store.CreateRun()
		if err != nil {
			return runStartedMsg{err: err}
		}

		cmd, err := commands.NewCommand(runID, commands.CmdStartRun, commands.StartRunPayload{
			WorkflowPath:  wf.Path,
			WorkflowName:  wf.Name,
			InitialPrompt: prompt,
			SourceRepo:    cwd,
		})
		if err != nil {
			return runStartedMsg{err: err}
		}

		a.processor.SubmitCommand(cmd)
		a.processor.ProcessRunSync(runID) // starts the goroutine

		return runStartedMsg{runID: runID}
	}
}
