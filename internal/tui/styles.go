package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Title / branding
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	// Selection: accent cursor, no background
	cursorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	selectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("255"))

	// Run / exec status
	statusRunningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	statusCompleteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("84"))
	statusFailedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	statusStuckStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	statusPendingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	statusWaitingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))

	// Signal status
	signalApprovedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("84"))
	signalChangesStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	signalBlockedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	signalNeedsHumanStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))

	// Chrome
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	helpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	sepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)
