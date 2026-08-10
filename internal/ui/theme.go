package ui

import "github.com/charmbracelet/lipgloss"

// Palette.
var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#8250df", Dark: "#a78bfa"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#6b7280"}
	colorErr    = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#f87171"}
	colorOK     = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#4ade80"}
	colorBorder = lipgloss.AdaptiveColor{Light: "#d0d7de", Dark: "#3f3f46"}
)

// Styles.
var (
	styleLogo = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleHint = lipgloss.NewStyle().Foreground(colorMuted)
	styleKey  = lipgloss.NewStyle().Foreground(colorAccent)
	styleErr  = lipgloss.NewStyle().Foreground(colorErr)
	styleOK   = lipgloss.NewStyle().Foreground(colorOK)
	styleDim  = lipgloss.NewStyle().Foreground(colorMuted)

	styleBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder)

	styleBoxFocus = styleBox.BorderForeground(colorAccent)

	styleChip = lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(colorMuted)

	styleChipActive = lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(lipgloss.Color("#ffffff")).
			Background(colorAccent).
			Bold(true)

	styleTitle    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleSelected = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleLabel    = lipgloss.NewStyle().Foreground(colorMuted).Width(10)
)

// key renders a "<key> <description>" help pair.
func key(k, desc string) string {
	return styleKey.Render(k) + " " + styleHint.Render(desc)
}
