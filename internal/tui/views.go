package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mpataki/shop/internal/models"
)

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

func (a *App) viewRunList() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" shop") + "\n\n")

	if a.err != nil {
		b.WriteString(errorStyle.Render("  error: "+a.err.Error()) + "\n\n")
	}

	// Runs section
	var runsContent strings.Builder
	if len(a.runs) == 0 {
		runsContent.WriteString(dimStyle.Render("no runs yet — press n to start one"))
	} else {
		for i, run := range a.runs {
			if i > 0 {
				runsContent.WriteString("\n")
			}
			runsContent.WriteString(a.renderRunRow(i, run))
		}
	}

	runsBox := boxStyle.Width(a.contentWidth()).Render(runsContent.String())
	b.WriteString(runsBox + "\n")

	// Activity log
	b.WriteString(a.renderLogPanel())

	// Help
	b.WriteString(helpStyle.Render("  j/k ↕  l/↵ view  n new  c continue  x kill  d delete  q quit"))

	return b.String()
}

func (a *App) renderRunRow(i int, run *models.Run) string {
	selected := i == a.selectedIdx

	id := fmt.Sprintf("#%-3d", run.ID)
	workflow := padRight(truncate(run.WorkflowName, 20), 21)
	status := a.formatStatus(run)
	age := fmt.Sprintf("%-4s", a.formatAge(run.CreatedAt))

	isInactive := run.Status == models.RunStatusComplete ||
		run.Status == models.RunStatusFailed ||
		run.Status == models.RunStatusPending

	if selected {
		return cursorStyle.Render("❯ ") +
			selectedRowStyle.Render(id) + "  " +
			selectedRowStyle.Render(workflow) + "  " +
			status + "  " +
			selectedRowStyle.Render(age) + "  " +
			dimStyle.Render(truncate(run.InitialPrompt, 36))
	} else if isInactive {
		return "  " + dimStyle.Render(id) + "  " +
			dimStyle.Render(workflow) + "  " +
			status + "  " +
			dimStyle.Render(age) + "  " +
			dimStyle.Render(truncate(run.InitialPrompt, 36))
	}

	return "  " + id + "  " + workflow + "  " + status + "  " + age + "  " +
		dimStyle.Render(truncate(run.InitialPrompt, 36))
}

func (a *App) viewRunDetail() string {
	if a.selectedRun == nil {
		return "no run selected"
	}

	run := a.selectedRun
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(fmt.Sprintf(" run #%d", run.ID)) + "  " +
		dimStyle.Render(run.WorkflowName) + "  " +
		a.formatStatus(run) + "\n\n")

	// Info section
	var infoContent strings.Builder
	infoContent.WriteString(run.InitialPrompt + "\n\n")
	infoContent.WriteString(labelStyle.Render("workspace  ") + dimStyle.Render(run.WorkspacePath))

	if run.Status == models.RunStatusWaitingHuman && run.WaitingReason != "" {
		infoContent.WriteString("\n\n" + statusWaitingStyle.Render("⏸ "+run.WaitingReason))
	}

	if run.Status == models.RunStatusFailed && run.Error != "" {
		infoContent.WriteString("\n\n" + errorStyle.Render("✗ "+run.Error))
	}

	infoBox := boxStyle.Width(a.contentWidth()).Render(infoContent.String())
	b.WriteString(infoBox + "\n")

	// Executions section
	var execContent strings.Builder
	if len(a.executions) == 0 {
		execContent.WriteString(dimStyle.Render("(none yet)"))
	} else {
		for i, exec := range a.executions {
			if i > 0 {
				execContent.WriteString("\n")
			}
			execContent.WriteString(a.renderExecRow(i, exec))
		}
	}

	execBox := boxStyle.Width(a.contentWidth()).Render(
		labelStyle.Render("executions") + "\n" + execContent.String())
	b.WriteString(execBox + "\n")

	// Activity log
	b.WriteString(a.renderLogPanel())

	// Help
	if run.Status == models.RunStatusWaitingHuman {
		b.WriteString(helpStyle.Render("  j/k ↕  c continue  s stop  o output  h/← back  q quit"))
	} else {
		b.WriteString(helpStyle.Render("  j/k ↕  l/↵ resume session  o output  h/← back  q quit"))
	}

	return b.String()
}

func (a *App) renderExecRow(i int, exec *models.Execution) string {
	selected := i == a.selectedExecIdx

	num := fmt.Sprintf("%d.", exec.SequenceNum)
	agent := padRight(exec.AgentName, 12)
	status := a.formatExecStatus(exec)
	duration := a.formatExecDuration(exec)
	signal := a.formatSignalStatus(exec)

	if selected {
		return cursorStyle.Render("❯ ") +
			selectedRowStyle.Render(num) + "  " +
			selectedRowStyle.Render(agent) + "  " +
			status + "  " +
			selectedRowStyle.Render(padRight(duration, 8)) + "  " +
			signal
	}

	return "  " + num + "  " + agent + "  " + status + "  " + padRight(duration, 8) + "  " + signal
}

func (a *App) formatExecStatus(exec *models.Execution) string {
	switch exec.Status {
	case models.ExecStatusComplete:
		exitStr := ""
		if exec.ExitCode != nil && *exec.ExitCode != 0 {
			exitStr = statusFailedStyle.Render(fmt.Sprintf(" exit:%d", *exec.ExitCode))
		}
		return statusCompleteStyle.Render("✓") + exitStr
	case models.ExecStatusRunning:
		return statusRunningStyle.Render(a.spinner.View())
	case models.ExecStatusFailed:
		exitStr := ""
		if exec.ExitCode != nil {
			exitStr = statusFailedStyle.Render(fmt.Sprintf(" exit:%d", *exec.ExitCode))
		}
		return statusFailedStyle.Render("✗") + exitStr
	case models.ExecStatusWaitingHuman:
		return statusWaitingStyle.Render("⏸")
	default:
		return dimStyle.Render("○")
	}
}

func (a *App) formatExecDuration(exec *models.Execution) string {
	if exec.StartedAt != nil && exec.CompletedAt != nil {
		return dimStyle.Render(formatDuration(exec.CompletedAt.Sub(*exec.StartedAt)))
	}
	if exec.StartedAt != nil && exec.Status == models.ExecStatusRunning {
		return statusRunningStyle.Render(formatDuration(time.Since(*exec.StartedAt)))
	}
	return ""
}

func (a *App) formatSignalStatus(exec *models.Execution) string {
	if exec.OutputSignal == nil {
		return ""
	}
	sig, ok := exec.OutputSignal["status"].(string)
	if !ok {
		return ""
	}
	switch models.SignalStatus(sig) {
	case models.SignalApproved, models.SignalDone, models.SignalContinue:
		return signalApprovedStyle.Render(sig)
	case models.SignalChangesRequested:
		return signalChangesStyle.Render(sig)
	case models.SignalBlocked, models.SignalStop:
		return signalBlockedStyle.Render(sig)
	case models.SignalNeedsHuman:
		return signalNeedsHumanStyle.Render(sig)
	default:
		return dimStyle.Render(sig)
	}
}

func (a *App) formatStatus(run *models.Run) string {
	switch run.Status {
	case models.RunStatusRunning:
		agent := run.CurrentAgent
		if agent == "" {
			agent = "running"
		}
		return statusRunningStyle.Render(a.spinner.View() + " " + agent)
	case models.RunStatusComplete:
		return statusCompleteStyle.Render("✓ done")
	case models.RunStatusFailed:
		return statusFailedStyle.Render("✗ failed")
	case models.RunStatusStuck:
		return statusStuckStyle.Render("⚠ stuck")
	case models.RunStatusWaitingHuman:
		return statusWaitingStyle.Render("⏸ waiting")
	case models.RunStatusPending:
		return statusPendingStyle.Render("○ pending")
	default:
		return dimStyle.Render(string(run.Status))
	}
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
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func (a *App) renderLogPanel() string {
	if len(a.logs) == 0 {
		return "\n"
	}

	var content strings.Builder
	for i, line := range a.logs {
		if i > 0 {
			content.WriteString("\n")
		}
		content.WriteString(logEntryStyle.Render("  " + line))
	}

	logBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("235")).
		Padding(0, 1).
		Width(a.contentWidth()).
		Render(labelStyle.Render("activity") + "\n" + content.String())

	return logBox + "\n"
}

func (a *App) viewNewRun() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" new run") + "\n\n")

	if a.err != nil {
		b.WriteString(errorStyle.Render("  error: "+a.err.Error()) + "\n\n")
	}

	// Workflow selection
	var wfContent strings.Builder
	if len(a.workflows) == 0 {
		wfContent.WriteString(dimStyle.Render("no workflows found in .shop/workflows/ or ~/.shop/workflows/"))
	} else {
		for i, wf := range a.workflows {
			name := wf.Name
			if wf.Source == "user" {
				name += dimStyle.Render(" ~")
			}
			if i > 0 {
				wfContent.WriteString("\n")
			}
			if i == a.selectedWorkflowIdx {
				if !a.focusOnPrompt {
					wfContent.WriteString(cursorStyle.Render("❯ ") + selectedRowStyle.Render(name))
				} else {
					wfContent.WriteString("❯ " + name)
				}
			} else {
				wfContent.WriteString("  " + dimStyle.Render(name))
			}
		}
	}

	wfBox := boxStyle.Width(a.contentWidth()).Render(
		labelStyle.Render("workflow") + "\n" + wfContent.String())
	b.WriteString(wfBox + "\n")

	// Prompt input
	var promptContent string
	if a.focusOnPrompt {
		promptContent = a.promptInput.View()
	} else if a.promptInput.Value() == "" {
		promptContent = dimStyle.Render("press ↵ to enter prompt…")
	} else {
		promptContent = a.promptInput.Value()
	}

	promptBox := boxStyle.Width(a.contentWidth()).Render(
		labelStyle.Render("prompt") + "\n" + promptContent)
	b.WriteString(promptBox + "\n\n")

	// Help
	if a.focusOnPrompt {
		b.WriteString(helpStyle.Render("  ↵ start  esc back  ctrl+c quit"))
	} else {
		b.WriteString(helpStyle.Render("  j/k ↕  ↵/tab enter prompt  esc cancel  ctrl+c quit"))
	}

	return b.String()
}

func (a *App) viewOutput() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" output") + "\n\n")

	var content string
	if a.outputContent == "" {
		content = dimStyle.Render("(no output)")
	} else {
		content = a.outputContent
	}

	outBox := boxStyle.Width(a.contentWidth()).Render(content)
	b.WriteString(outBox + "\n\n")

	b.WriteString(helpStyle.Render("  esc/h back  q quit"))

	return b.String()
}

// helpers

func (a *App) contentWidth() int {
	w := a.width - 2
	if w < 40 {
		w = 60
	}
	return w
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
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
