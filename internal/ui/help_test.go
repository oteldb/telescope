package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/query"
)

// openHelpFrom presses key and follows the command it returns, which send does
// not: only enter and esc navigate there.
func openHelpFrom(t *testing.T, m tea.Model, key tea.Msg) tea.Model {
	t.Helper()
	m, cmd := m.Update(key)
	require.NotNil(t, cmd, "the key asked for nothing")
	msg, ok := cmd().(openHelpMsg)
	require.True(t, ok, "the key opened something else")
	m, _ = m.Update(msg)
	return m
}

// TestHelpOpensFromTheList: "?" is free in the list, where a query is not being
// typed.
func TestHelpOpensFromTheList(t *testing.T) {
	m := openHelpFrom(t, logsModel(t, "alpha"), k("?"))
	require.Equal(t, stateHelp, m.(Model).state)

	out := ansi.Strip(screen(t, m))
	require.Contains(t, out, "filter syntax")
	require.Contains(t, out, "level>=warn")
	require.Contains(t, out, "esc close")
}

// TestHelpOpensFromThePrompt: the reference is worth most beside the query being
// written, and "?" there is a regexp quantifier.
func TestHelpOpensFromThePrompt(t *testing.T) {
	m := send(t, logsModel(t, "alpha"), k("/"))
	m = typed(t, m, "err")
	require.Contains(t, ansi.Strip(screen(t, m)), "f1 syntax", "the prompt says where it is")

	m = openHelpFrom(t, m, tea.KeyMsg{Type: tea.KeyF1})
	require.Equal(t, stateHelp, m.(Model).state)

	// The prompt is returned to still open, on what was being typed.
	m = send(t, m, k("esc"))
	require.Equal(t, stateLogs, m.(Model).state)
	require.True(t, m.(Model).logs.searching)
	require.Equal(t, "err", m.(Model).logs.search.Value())
}

// TestHelpReturnsToTheEntry: help opened over an entry goes back to it, not to
// the list underneath.
func TestHelpReturnsToTheEntry(t *testing.T) {
	m := send(t, logsModel(t, `{"level":"error","msg":"exploded"}`), k("enter"))
	require.Equal(t, stateEntry, m.(Model).state)

	m = openHelpFrom(t, m, k("?"))
	m = send(t, m, k("esc"))
	require.Equal(t, stateEntry, m.(Model).state)
}

// TestHelpScrolls: the reference is longer than a short terminal, and reaching
// the end of it must not scroll past it.
func TestHelpScrolls(t *testing.T) {
	m := openHelpFrom(t, send(t, New(), size(), connectMsg{}), k("?"))
	first := screen(t, m)

	m = send(t, m, k("down"), k("down"))
	require.NotEqual(t, first, screen(t, m), "it scrolled")

	for range 200 {
		m, _ = m.Update(k("down"))
	}
	end := m.(Model).help
	require.Equal(t, len(end.lines())-end.bodyHeight(), end.top, "the last screen is the last one")
}

// TestHelpIsTheLanguageThatParses: every row of the reference is something that
// can be typed into the prompt as it stands.
func TestHelpIsTheLanguageThatParses(t *testing.T) {
	for _, section := range helpSections {
		for _, row := range section.rows {
			if row.syntax == "" {
				continue
			}
			t.Run(row.syntax, func(t *testing.T) {
				e, err := query.Parse(row.syntax)
				require.NoError(t, err)
				require.NotNil(t, e, "a row that filters nothing is not an example")
			})
		}
	}
}

// TestHelpFitsANarrowTerminal: nothing in the reference may push the frame wider
// than the screen it is drawn on.
func TestHelpFitsANarrowTerminal(t *testing.T) {
	for _, w := range []int{40, 60, 80, 120} {
		m := send(t, New(), size(), connectMsg{})
		m = openHelpFrom(t, m, k("?"))
		m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 24})

		for line := range strings.SplitSeq(ansi.Strip(m.View()), "\n") {
			require.LessOrEqual(t, ansi.StringWidth(line), w, "width %d: %q", w, line)
		}
	}
}
