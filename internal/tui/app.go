package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mpataki/shop/internal/models"
	"github.com/mpataki/shop/internal/storage"
)

type View int

const (
	ViewRunList View = iota
	ViewRunDetail
	ViewNewRun
)

type App struct {
	storage      *storage.Storage
	specs        map[string]*models.Spec
	workspaceDir string

	view            View
	runs            []*models.Run
	selectedIdx     int
	selectedRun     *models.Run
	executions      []*models.Execution
	selectedExecIdx int

	width  int
	height int
	err    error
}

func NewApp(store *storage.Storage, specs map[string]*models.Spec, workspaceDir string) *App {
	return &App{
		storage:      store,
		specs:        specs,
		workspaceDir: workspaceDir,
		view:         ViewRunList,
	}
}

func (a *App) Init() tea.Cmd {
	return a.loadRuns
}

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
		// Reload runs list to show updated status
		return a, a.loadRuns

	case sessionResumedMsg:
		if msg.err != nil {
			a.err = msg.err
		}
		// Session ended, return to run list
		a.view = ViewRunList
		return a, a.loadRuns
	}

	return a, nil
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch a.view {
	case ViewRunList:
		return a.handleRunListKey(msg)
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

	statusRunning  = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	statusComplete = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	statusFailed   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	statusStuck    = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))
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
			if i == a.selectedIdx {
				line = selectedStyle.Render("▶ " + line)
			} else {
				line = "  " + line
			}
			s += line + "\n"
		}
	}

	s += "\n" + helpStyle.Render("[n] new run  [enter] view  [x] kill  [r] refresh  [q] quit")

	return s
}

func (a *App) formatRunLine(run *models.Run) string {
	status := a.formatStatus(run.Status)
	prompt := truncate(run.InitialPrompt, 40)
	return fmt.Sprintf("#%-3d %-20s %s  %s", run.ID, run.SpecName, status, prompt)
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
	s := titleStyle.Render(fmt.Sprintf("Run #%d: %s", run.ID, run.SpecName)) + "\n\n"
	s += run.InitialPrompt + "\n\n"

	s += "Executions\n"
	s += "──────────\n"

	if len(a.executions) == 0 {
		s += "(no executions yet)\n"
	} else {
		for i, exec := range a.executions {
			status := "○"
			switch exec.Status {
			case models.ExecStatusComplete:
				status = "✓"
			case models.ExecStatusRunning:
				status = "●"
			case models.ExecStatusFailed:
				status = "✗"
			}

			sessionInfo := ""
			if exec.ClaudeSessionID != "" {
				sessionInfo = fmt.Sprintf(" [%s]", truncate(exec.ClaudeSessionID, 12))
			}

			line := fmt.Sprintf("%d. %s %s%s", exec.SequenceNum, exec.AgentName, status, sessionInfo)
			if i == a.selectedExecIdx {
				line = selectedStyle.Render("▶ " + line)
			} else {
				line = "  " + line
			}
			s += line + "\n"
		}
	}

	s += "\n" + helpStyle.Render("[↑/↓] select  [enter] open in claude  [esc] back  [q] quit")

	return s
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

// Commands

func (a *App) loadRuns() tea.Msg {
	runs, err := a.storage.ListRuns(20)
	return runsLoadedMsg{runs: runs, err: err}
}

func (a *App) loadRunDetail(id int64) tea.Cmd {
	return func() tea.Msg {
		run, err := a.storage.GetRun(id)
		if err != nil {
			return runDetailMsg{err: err}
		}

		execs, err := a.storage.GetExecutionsForRun(id)
		return runDetailMsg{run: run, executions: execs, err: err}
	}
}

func (a *App) killRun(id int64) tea.Cmd {
	return func() tea.Msg {
		run, err := a.storage.GetRun(id)
		if err != nil {
			return runKilledMsg{err: err}
		}

		// Find and kill running execution
		runningExec, _ := a.storage.GetRunningExecutionForRun(id)
		if runningExec != nil && runningExec.PID != nil {
			syscall.Kill(-*runningExec.PID, syscall.SIGKILL)
			runningExec.Status = models.ExecStatusFailed
			a.storage.UpdateExecution(runningExec)
		}

		// Update run status
		run.Status = models.RunStatusFailed
		a.storage.UpdateRun(run)

		return runKilledMsg{runID: id}
	}
}

func (a *App) resumeSession(sessionID string, workDir string) tea.Cmd {
	cmd := exec.Command("claude", "--resume", sessionID)
	cmd.Dir = workDir
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return sessionResumedMsg{sessionID: sessionID, err: err}
	})
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
