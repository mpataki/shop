package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	specs        map[string]*models.Spec

	view            View
	runs            []*models.Run
	selectedIdx     int
	selectedRun     *models.Run
	executions      []*models.Execution
	selectedExecIdx int
	outputContent   string

	width  int
	height int
	err    error
}

func NewApp(orch *orchestrator.Orchestrator, specs map[string]*models.Spec) *App {
	return &App{
		orchestrator: orch,
		specs:        specs,
		view:         ViewRunList,
	}
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.loadRuns, a.tickCmd())
}

func (a *App) tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (a *App) hasRunningRuns() bool {
	for _, run := range a.runs {
		if run.Status == models.RunStatusRunning {
			return true
		}
	}
	return false
}

type tickMsg time.Time

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return a.handleKey(msg)

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		return a, nil

	case runsLoadedMsg:
		a.runs = msg.runs
		a.err = msg.err
		// Continue ticking if there are running runs
		if a.hasRunningRuns() {
			return a, a.tickCmd()
		}
		return a, nil

	case tickMsg:
		// Only refresh if we're on the run list view and have running runs
		if a.view == ViewRunList && a.hasRunningRuns() {
			return a, tea.Batch(a.loadRuns, a.tickCmd())
		}
		// Keep ticking to detect new running runs
		return a, a.tickCmd()

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
		// Reload runs list to show updated status
		return a, a.loadRuns

	case sessionResumedMsg:
		if msg.err != nil {
			a.err = msg.err
		}
		// Session ended, return to run list
		a.view = ViewRunList
		return a, a.loadRuns

	case runDeletedMsg:
		a.err = msg.err
		// Adjust selection if needed
		if a.selectedIdx >= len(a.runs)-1 && a.selectedIdx > 0 {
			a.selectedIdx--
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

	case "enter":
		if len(a.runs) > 0 && a.selectedIdx < len(a.runs) {
			return a, a.loadRunDetail(a.runs[a.selectedIdx].ID)
		}

	case "n":
		a.view = ViewNewRun

	case "r":
		return a, a.loadRuns

	case "x":
		if len(a.runs) > 0 && a.selectedIdx < len(a.runs) {
			return a, a.killRun(a.runs[a.selectedIdx].ID)
		}

	case "d":
		if len(a.runs) > 0 && a.selectedIdx < len(a.runs) {
			return a, a.deleteRun(a.runs[a.selectedIdx].ID)
		}
	}

	return a, nil
}

func (a *App) handleRunDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		a.view = ViewRunList
		a.selectedRun = nil
		a.executions = nil
		a.selectedExecIdx = 0

	case "ctrl+c":
		return a, tea.Quit

	case "up", "k":
		if a.selectedExecIdx > 0 {
			a.selectedExecIdx--
		}

	case "down", "j":
		if a.selectedExecIdx < len(a.executions)-1 {
			a.selectedExecIdx++
		}

	case "enter":
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
	}

	return a, nil
}

func (a *App) handleOutputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		a.view = ViewRunDetail
		a.outputContent = ""

	case "ctrl+c":
		return a, tea.Quit
	}

	return a, nil
}

func (a *App) handleNewRunKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.view = ViewRunList

	case "ctrl+c":
		return a, tea.Quit
	}

	return a, nil
}

func (a *App) View() string {
	switch a.view {
	case ViewRunList:
		return a.viewRunList()
	case ViewRunDetail:
		return a.viewRunDetail()
	case ViewNewRun:
		return a.viewNewRun()
	case ViewOutput:
		return a.viewOutput()
	}
	return ""
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("57"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	statusRunning  = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	statusComplete = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	statusFailed   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	statusStuck    = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	statusPending  = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))

	// Signal status colors
	signalApproved = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))  // green
	signalDone     = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))  // green
	signalChanges  = lipgloss.NewStyle().Foreground(lipgloss.Color("220")) // yellow
	signalBlocked  = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // red

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))
)

func (a *App) viewRunList() string {
	s := titleStyle.Render("Shop") + "\n\n"

	if a.err != nil {
		s += fmt.Sprintf("Error: %v\n", a.err)
	}

	if len(a.runs) == 0 {
		s += "No runs yet. Press 'n' to create one.\n"
	} else {
		s += "Recent Runs\n"
		s += "───────────\n"

		for i, run := range a.runs {
			line := a.formatRunLine(run)
			isSelected := i == a.selectedIdx
			isRunning := run.Status == models.RunStatusRunning

			if isSelected {
				line = selectedStyle.Render("▶ " + line)
			} else if !isRunning && run.Status != models.RunStatusStuck {
				// Dim completed/failed runs
				line = "  " + dimStyle.Render(line)
			} else {
				line = "  " + line
			}
			s += line + "\n"
		}
	}

	s += "\n" + helpStyle.Render("[enter] view  [x] kill  [d] delete  [r] refresh  [q] quit")

	return s
}

func (a *App) formatRunLine(run *models.Run) string {
	status := a.formatStatus(run.Status)
	age := a.formatAge(run.CreatedAt)
	prompt := truncate(run.InitialPrompt, 35)
	return fmt.Sprintf("#%-3d %-18s %s  %-6s  %s", run.ID, run.SpecName, status, age, prompt)
}

func (a *App) formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd", days)
	}
}

func (a *App) formatStatus(status models.RunStatus) string {
	switch status {
	case models.RunStatusRunning:
		return statusRunning.Render("● running")
	case models.RunStatusComplete:
		return statusComplete.Render("✓ complete")
	case models.RunStatusFailed:
		return statusFailed.Render("✗ failed")
	case models.RunStatusStuck:
		return statusStuck.Render("⚠ stuck")
	default:
		return string(status)
	}
}

func (a *App) viewRunDetail() string {
	if a.selectedRun == nil {
		return "No run selected"
	}

	run := a.selectedRun

	// Header with status badge
	header := fmt.Sprintf("Run #%d: %s", run.ID, run.SpecName)
	s := titleStyle.Render(header) + "  " + a.formatStatus(run.Status) + "\n\n"

	// Full prompt
	s += run.InitialPrompt + "\n\n"

	// Workspace path
	s += labelStyle.Render("Workspace: ") + dimStyle.Render(run.WorkspacePath) + "\n\n"

	s += "Executions\n"
	s += "──────────\n"

	if len(a.executions) == 0 {
		s += "(no executions yet)\n"
	} else {
		for i, exec := range a.executions {
			status := "○"
			switch exec.Status {
			case models.ExecStatusComplete:
				status = statusComplete.Render("✓")
			case models.ExecStatusRunning:
				status = statusRunning.Render("●")
			case models.ExecStatusFailed:
				status = statusFailed.Render("✗")
			}

			// Exit code
			exitCode := ""
			if exec.ExitCode != nil {
				if *exec.ExitCode == 0 {
					exitCode = dimStyle.Render("exit:0")
				} else {
					exitCode = statusFailed.Render(fmt.Sprintf("exit:%d", *exec.ExitCode))
				}
			}

			// Duration
			duration := ""
			if exec.StartedAt != nil && exec.CompletedAt != nil {
				d := exec.CompletedAt.Sub(*exec.StartedAt)
				duration = dimStyle.Render(formatDuration(d))
			} else if exec.StartedAt != nil && exec.Status == models.ExecStatusRunning {
				d := time.Since(*exec.StartedAt)
				duration = statusRunning.Render(formatDuration(d) + "...")
			}

			// Signal status with color
			signalStatus := ""
			if exec.OutputSignal != nil {
				if sig, ok := exec.OutputSignal["status"].(string); ok {
					signalStatus = a.formatSignalStatus(sig)
				}
			}

			// Build line: "1. coder      ✓  exit:0  32s   DONE"
			line := fmt.Sprintf("%d. %-10s %s", exec.SequenceNum, exec.AgentName, status)
			if exitCode != "" {
				line += "  " + exitCode
			}
			if duration != "" {
				line += "  " + fmt.Sprintf("%6s", duration)
			}
			if signalStatus != "" {
				line += "   " + signalStatus
			}

			if i == a.selectedExecIdx {
				line = selectedStyle.Render("▶ " + line)
			} else {
				line = "  " + line
			}
			s += line + "\n"
		}
	}

	s += "\n" + helpStyle.Render("[↑/↓] select  [enter] resume  [o] output  [esc] back  [q] quit")

	return s
}

func (a *App) formatSignalStatus(status string) string {
	switch status {
	case "APPROVED", "DONE":
		return signalApproved.Render(status)
	case "CHANGES_REQUESTED":
		return signalChanges.Render(status)
	case "BLOCKED":
		return signalBlocked.Render(status)
	default:
		return status
	}
}

func (a *App) viewNewRun() string {
	s := titleStyle.Render("New Run") + "\n\n"

	s += "Available specs:\n"
	for name := range a.specs {
		s += fmt.Sprintf("  • %s\n", name)
	}

	if len(a.specs) == 0 {
		s += "  (no specs found)\n"
	}

	s += "\n" + helpStyle.Render("[esc] cancel")

	return s
}

func (a *App) viewOutput() string {
	s := titleStyle.Render("Output") + "\n\n"

	if a.outputContent == "" {
		s += "(no output)\n"
	} else {
		s += a.outputContent + "\n"
	}

	s += "\n" + helpStyle.Render("[esc] back  [q] quit")

	return s
}

// Messages

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
	err       error
}

type runDeletedMsg struct {
	runID int64
	err   error
}

type outputLoadedMsg struct {
	content string
	err     error
}

// Commands

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

func (a *App) resumeSession(sessionID string, workDir string) tea.Cmd {
	cmd := exec.Command("claude", "--resume", sessionID)
	cmd.Dir = workDir
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return sessionResumedMsg{sessionID: sessionID, err: err}
	})
}

func (a *App) loadOutput(sessionID string, workspacePath string) tea.Cmd {
	return func() tea.Msg {
		// Claude stores sessions in ~/.claude/projects/{encoded-path}/{sessionID}.jsonl
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

		// Read JSONL and find the last assistant message
		var lastContent string
		scanner := bufio.NewScanner(file)
		// Increase buffer size for large lines
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			var entry map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue
			}

			// Check if this is an assistant message
			if entry["type"] == "assistant" {
				if msg, ok := entry["message"].(map[string]any); ok {
					if content, ok := msg["content"].([]any); ok {
						// Build text from content blocks
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

			// Also check for summary type as fallback
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", h, m)
}
