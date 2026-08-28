package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/oteldb/telescope/internal/view"
)

// copyLink copies the command that reopens this list.
//
// It is the other direction of what `telescope mcp` writes: an agent hands a
// person a link to what it found, and this hands an agent — or a colleague, or
// tomorrow — a link to what the person is looking at. One form, written by
// whichever end is holding the view.
//
// The filter copied is the one in force and not the one the list was opened on:
// narrowing is most of what happens to a list, and a link to the unnarrowed
// thing is a link to something nobody was looking at.
func (m logModel) copyLink() (logModel, tea.Cmd) {
	v := view.View{Place: m.cfg.Name, Query: m.view.Filter().Query, Range: m.cfg.Range.Spec}
	if v.Place == "" {
		// A place assembled on the start screen rather than declared has no
		// name to put in a link, and a link that named a command instead would
		// run it on the machine of whoever pasted it.
		m.note = "no link for this one: it was not opened from a place the config declares"
		return m, nil
	}
	return m, copyCmd("a link to this view", v.Link())
}
