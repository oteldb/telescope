package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
)

func renderWatchLine(e *logs.Entry, fields []rowField, marker string, hoff, width int) string {
	w := e.Record.Watch
	text, tail := e.Head, ""
	if w.Event != nil && w.Count > 0 {
		tail = " · " + w.OccurrenceText()
		text = strings.TrimSuffix(text, tail)
		tail = styleDim.Render(tail)
	}
	var namespace []rowField
	for _, field := range fields {
		if field.Key == "namespace" {
			namespace = append(namespace, field)
		}
	}
	room := max(width-lipgloss.Width(marker), 0)
	if hoff > 0 {
		text = ansi.TruncateLeft(text, hoff, "")
	}
	textRoom := max(room-lipgloss.Width(tail), 0)
	text = ansi.Truncate(text, textRoom, "…")
	text += spendFields(namespace, textRoom-lipgloss.Width(text))
	return marker + text + ansi.Truncate(tail, max(room-lipgloss.Width(text), 0), "")
}
