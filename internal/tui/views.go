package tui

import (
	"fmt"
	"strings"
	"time"

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

	b.WriteString(titleStyle.Render("shop") + "\n")
	b.WriteString(sepStyle.Render(sep(a.width)) + "\n\n")

	if a.err != nil {
		b.WriteString(errorStyle.Render("  error: "+a.err.Error()) + "\n\n")
	}

	if len(a.runs) == 0 {
		b.WriteString(dimStyle.Render("  no runs yet — press n to start one") + "\n")
	} else {
		for i, run := range a.runs {
			b.WriteString(a.renderRunRow(i, run) + "\n")
		}
	}

	b.WriteString("\n")
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
	b.WriteString(titleStyle.Render(fmt.Sprintf("run #%d", run.ID)) + "  " +
		dimStyle.Render(run.WorkflowName) + "  " +
		a.formatStatus(run) + "\n")
	b.WriteString(sepStyle.Render(sep(a.width)) + "\n\n")

	// Prompt
	b.WriteString("  " + run.InitialPrompt + "\n\n")

	// Metadata
	b.WriteString("  " + labelStyle.Render("workspace  ") + dimStyle.Render(run.WorkspacePath) + "\n")

	if run.Status == models.RunStatusWaitingHuman {
		b.WriteString("\n")
		if run.WaitingReason != "" {
			b.WriteString("  " + statusWaitingStyle.Render("⏸ "+run.WaitingReason) + "\n")
		}
	}

	if run.Status == models.RunStatusFailed && run.Error != "" {
		b.WriteString("\n  " + errorStyle.Render("✗ "+run.Error) + "\n")
	}

	b.WriteString("\n")
	b.WriteString("  " + dimStyle.Render("executions") + "\n")
	b.WriteString("  " + sepStyle.Render(sep(a.width-4)) + "\n")

	if len(a.executions) == 0 {
		b.WriteString("  " + dimStyle.Render("(none yet)") + "\n")
	} else {
		for i, exec := range a.executions {
			b.WriteString(a.renderExecRow(i, exec) + "\n")
		}
	}

	b.WriteString("\n")
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
		return cursorStyle.Render("❯ ") + "  " +
			selectedRowStyle.Render(num) + "  " +
			selectedRowStyle.Render(agent) + "  " +
			status + "  " +
			selectedRowStyle.Render(padRight(duration, 8)) + "  " +
			signal
	}

	return "    " + num + "  " + agent + "  " + status + "  " + padRight(duration, 8) + "  " + signal
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

func (a *App) viewNewRun() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("new run") + "\n")
	b.WriteString(sepStyle.Render(sep(a.width)) + "\n\n")

	if a.err != nil {
		b.WriteString(errorStyle.Render("  error: "+a.err.Error()) + "\n\n")
	}

	b.WriteString("  " + labelStyle.Render("workflow") + "\n\n")

	if len(a.workflows) == 0 {
		b.WriteString("  " + dimStyle.Render("no workflows found in .shop/workflows/ or ~/.shop/workflows/") + "\n")
	} else {
		for i, wf := range a.workflows {
			name := wf.Name
			if wf.Source == "user" {
				name += dimStyle.Render(" ~")
			}
			if i == a.selectedWorkflowIdx {
				if !a.focusOnPrompt {
					b.WriteString(cursorStyle.Render("  ❯ ") + selectedRowStyle.Render(name) + "\n")
				} else {
					b.WriteString("  ❯ " + name + "\n")
				}
			} else {
				b.WriteString("    " + dimStyle.Render(name) + "\n")
			}
		}
	}

	b.WriteString("\n  " + labelStyle.Render("prompt") + "\n\n")

	if a.focusOnPrompt {
		b.WriteString("  " + a.promptInput.View() + "\n")
	} else if a.promptInput.Value() == "" {
		b.WriteString("  " + dimStyle.Render("press ↵ to enter prompt…") + "\n")
	} else {
		b.WriteString("  " + a.promptInput.Value() + "\n")
	}

	b.WriteString("\n")

	if a.focusOnPrompt {
		b.WriteString(helpStyle.Render("  ↵ start  esc back  ctrl+c quit"))
	} else {
		b.WriteString(helpStyle.Render("  j/k ↕  ↵/tab enter prompt  esc cancel  ctrl+c quit"))
	}

	return b.String()
}

func (a *App) viewOutput() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("output") + "\n")
	b.WriteString(sepStyle.Render(sep(a.width)) + "\n\n")

	if a.outputContent == "" {
		b.WriteString(dimStyle.Render("  (no output)") + "\n")
	} else {
		b.WriteString(a.outputContent + "\n")
	}

	b.WriteString("\n" + helpStyle.Render("  esc/h back  q quit"))

	return b.String()
}

// helpers

func sep(width int) string {
	if width <= 0 {
		width = 60
	}
	return strings.Repeat("─", width)
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
