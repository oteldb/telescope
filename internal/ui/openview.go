package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/oteldb/telescope/internal/source"
)

// NewLogs returns the root model opened straight onto one place's list, which
// is what naming a place on the command line does.
//
// The start screen is built behind it rather than skipped: leaving the list is
// how another place is picked, and a view opened from a link that quit on
// escape would be a worse place to arrive than the screen it saved you from.
//
// The filter is part of what the sources are asked and not something the view
// learns about afterwards, the same way [connectMsg] treats one: a place opened
// on a query has already been narrowed by it, and a database that can answer
// the query itself is asked it rather than read past.
func NewLogs(cfg source.Config, query string) Model {
	return Model{start: newStart(), opening: &connectMsg{cfg: cfg, query: query}}
}

// opened is the view the model was started on, delivered once through the same
// message the start screen sends. Going through the message rather than
// building the list here is what keeps one path into a list: the history is
// remembered, the stream is started and the size is applied in one place.
func (m Model) opened() tea.Cmd {
	if m.opening == nil {
		return nil
	}
	open := *m.opening
	return func() tea.Msg { return open }
}
