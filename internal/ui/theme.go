package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/logs"
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

	// styleStat colors the figures in the status line. The words around them
	// stay dim so the eye lands on the counts, which is the only thing in that
	// line read at a glance; coloring the words too would leave it a rainbow
	// with nothing standing out of it.
	styleStat = lipgloss.NewStyle().Foreground(colorText).Bold(true)

	// styleOn marks a toggle that is in force. A count of what a toggle did
	// never says whether it is still doing it.
	styleOn = lipgloss.NewStyle().Foreground(colorOK).Bold(true)

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

// levelStyles colors a severity in the log list gutter.
var levelStyles = map[zapcore.Level]lipgloss.Style{
	zapcore.DebugLevel:  lipgloss.NewStyle().Foreground(colorMuted),
	zapcore.InfoLevel:   lipgloss.NewStyle().Foreground(colorWhere),
	zapcore.WarnLevel:   lipgloss.NewStyle().Foreground(colorWarn),
	zapcore.ErrorLevel:  lipgloss.NewStyle().Foreground(colorErr),
	zapcore.DPanicLevel: lipgloss.NewStyle().Foreground(colorErr).Bold(true),
	zapcore.PanicLevel:  lipgloss.NewStyle().Foreground(colorErr).Bold(true),
	zapcore.FatalLevel:  lipgloss.NewStyle().Foreground(colorErr).Bold(true),
}

// styleTrace marks a trace or span id, which is not read so much as recognized
// and carried to whatever else knows it.
var styleTrace = lipgloss.NewStyle().Foreground(colorMatch)

// renderLevelWord renders a severity as the word it is.
func renderLevelWord(l zapcore.Level) string {
	if style, ok := levelStyles[l]; ok {
		return style.Render(l.CapitalString())
	}
	return styleDim.Render(l.CapitalString())
}

// renderValue draws an attribute's value according to what its key says it is,
// and defends against what it might actually be: a value out of a log database
// is bytes somebody else chose, so its control characters are shown rather
// than obeyed. A value with any is left as the escaping renders it — the
// escapes are the thing worth seeing.
func renderValue(key, value string) string {
	if escaped := logs.Escape(value); escaped != value {
		return escaped
	}
	switch logs.KindOf(key) {
	case logs.KindLevel:
		if l, ok := logs.LevelOf(value); ok {
			return renderLevelWord(l)
		}
	case logs.KindTrace:
		return styleTrace.Render(value)
	case logs.KindTime:
		return styleDim.Render(value)
	}
	// What a well-known key says its value is, colored the same here as it is
	// down in the list: a namespace read in one place and in the other is the
	// same namespace.
	if colored, ok := logs.HighlightField(key, value); ok {
		return colored
	}
	return logs.Highlight(value)
}

// levelWidth is the gutter column a severity is rendered in, wide enough for
// the longest word zap spells and no wider.
const levelWidth = 5

// renderLevel renders a severity for the gutter, padded to [levelWidth].
func renderLevel(l zapcore.Level) string {
	word := l.CapitalString()
	if len(word) > levelWidth {
		word = word[:levelWidth]
	}
	word += strings.Repeat(" ", levelWidth-len(word))
	if style, ok := levelStyles[l]; ok {
		return style.Render(word)
	}
	return styleDim.Render(word)
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
