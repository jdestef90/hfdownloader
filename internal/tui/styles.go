package tui

import "github.com/charmbracelet/lipgloss"

var (
	styleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	styleBadge = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("237")).
			Padding(0, 1)

	styleSection = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("81"))

	styleSelected = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212"))

	styleMuted = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	styleWarning = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	styleError = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("196"))

	styleSuccess = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("42"))

	styleBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)
)
