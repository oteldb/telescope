package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// screenPad is the horizontal breathing room kept around every view.
const screenPad = 2

// padScreen indents a rendered view away from the terminal edges.
func padScreen(s string) string {
	return lipgloss.NewStyle().PaddingLeft(screenPad).Render(s)
}

// Palette.
var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#8250df", Dark: "#a78bfa"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#6b7280"}
	colorText   = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#e5e7eb"}
	colorWhere  = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#38bdf8"}
	colorErr    = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#f87171"}
	colorOK     = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#4ade80"}
	colorWarn   = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#fbbf24"}
	colorMatch  = lipgloss.AdaptiveColor{Light: "#bf3989", Dark: "#f472b6"}
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

	// styleChipWhere labels where a stream comes from, next to what it reads.
	styleChipWhere = lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(lipgloss.Color("#ffffff")).
			Background(colorWhere).
			Bold(true)

	styleFilter = lipgloss.NewStyle().Foreground(colorText)

	// styleMatch marks the characters a query matched inside a suggestion.
	styleMatch = lipgloss.NewStyle().Foreground(colorMatch).Bold(true)

	styleTitle    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleSelected = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleLabel    = lipgloss.NewStyle().Foreground(colorMuted)
)

// tagColors tell the sources of a merge apart. Which color a source gets is
// only its position, so a stream keeps the same one for as long as it is open.
var tagColors = []lipgloss.AdaptiveColor{
	{Light: "#0969da", Dark: "#38bdf8"},
	{Light: "#1a7f37", Dark: "#4ade80"},
	{Light: "#9a6700", Dark: "#fbbf24"},
	{Light: "#bf3989", Dark: "#f472b6"},
	{Light: "#8250df", Dark: "#a78bfa"},
	{Light: "#0e7490", Dark: "#22d3ee"},
}

// tagStyle is how the i-th source of a merge is marked.
func tagStyle(i int) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(tagColors[i%len(tagColors)])
}

// stateStyles colors the lifecycle words collectors report. Only states that
// mean something is wrong, or is on its way somewhere, are colored; the
// resting states stay dim so a list of mostly-stopped units is not a wall of
// red. Anything unlisted falls back to dim.
var stateStyles = map[string]lipgloss.Style{
	// Up and serving.
	"running":   lipgloss.NewStyle().Foreground(colorOK),
	"active":    lipgloss.NewStyle().Foreground(colorOK),
	"listening": lipgloss.NewStyle().Foreground(colorOK),
	"mounted":   lipgloss.NewStyle().Foreground(colorOK),
	"plugged":   lipgloss.NewStyle().Foreground(colorOK),

	// Finished as intended.
	"succeeded": lipgloss.NewStyle().Foreground(colorWhere),
	"completed": lipgloss.NewStyle().Foreground(colorWhere),

	// In transition, or not yet settled.
	"pending":      lipgloss.NewStyle().Foreground(colorWarn),
	"activating":   lipgloss.NewStyle().Foreground(colorWarn),
	"deactivating": lipgloss.NewStyle().Foreground(colorWarn),
	"reloading":    lipgloss.NewStyle().Foreground(colorWarn),
	"restarting":   lipgloss.NewStyle().Foreground(colorWarn),
	"auto-restart": lipgloss.NewStyle().Foreground(colorWarn),
	"waiting":      lipgloss.NewStyle().Foreground(colorWarn),
	"paused":       lipgloss.NewStyle().Foreground(colorWarn),
	"unknown":      lipgloss.NewStyle().Foreground(colorWarn),
	"terminating":  lipgloss.NewStyle().Foreground(colorWarn),

	// Wrong. "dead" is systemd's ordinary word for a stopped unit, so it is
	// deliberately not here.
	"failed":           lipgloss.NewStyle().Foreground(colorErr),
	"error":            lipgloss.NewStyle().Foreground(colorErr),
	"crashloopbackoff": lipgloss.NewStyle().Foreground(colorErr),
	"oomkilled":        lipgloss.NewStyle().Foreground(colorErr),
}

// renderState colors a lifecycle word, preserving the casing a collector used.
func renderState(state string) string {
	if state == "" {
		return ""
	}
	if style, ok := stateStyles[strings.ToLower(state)]; ok {
		return style.Render(state)
	}
	return styleDim.Render(state)
}

// key renders a "<key> <description>" help pair.
func key(k, desc string) string {
	return styleKey.Render(k) + " " + styleHint.Render(desc)
}
