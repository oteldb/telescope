package ui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// TestCopyingALinkToTheList: the other direction of what telescope mcp writes
// — a person hands an agent, or a colleague, a link to what is on their screen.
func TestCopyingALinkToTheList(t *testing.T) {
	copied := watchClipboard(t)

	m := newLogs(source.Config{
		Name:      "prod",
		Collector: source.CollectorVictoriaLogs,
		Range:     source.Range{Spec: "6h..1h"},
	}, logs.NewStore(storeLimit), "level>=error")
	m.resize(120, 40)

	m, cmd := m.copyLink()
	require.NotNil(t, cmd)
	msg := cmd()
	require.Equal(t, `telescope prod --query 'level>=error' --range 6h..1h`, *copied)
	require.Contains(t, msg.(noteMsg).text, "copied")
}

// TestTheLinkCarriesTheFilterInForce: narrowing is most of what happens to a
// list, so a link to the unnarrowed thing is a link to something nobody was
// looking at.
func TestTheLinkCarriesTheFilterInForce(t *testing.T) {
	copied := watchClipboard(t)

	m := newLogs(source.Config{Name: "prod", Collector: source.CollectorVictoriaLogs},
		logs.NewStore(storeLimit), "")
	m.resize(120, 40)
	f := m.view.Filter()
	f.Query = `"connection refused"`
	m.view.SetFilter(f)

	_, cmd := m.copyLink()
	require.NotNil(t, cmd)
	cmd()
	require.Equal(t, `telescope prod --query '"connection refused"'`, *copied)
}

// TestAPlaceWithNoNameHasNoLink: one assembled on the start screen was never
// declared, and a link naming a command would run it on the machine of whoever
// pasted it.
func TestAPlaceWithNoNameHasNoLink(t *testing.T) {
	m := newLogs(source.Config{Collector: source.CollectorJournal, Unit: "nginx"},
		logs.NewStore(storeLimit), "")
	m.resize(120, 40)

	m, cmd := m.copyLink()
	require.Nil(t, cmd, "nothing is copied")
	require.Contains(t, m.note, "not opened from a place the config declares")
}

// TestTheListSaysTheLinkKeyIsThere: every view writes its own keys along the
// bottom, so a key nobody can see is a key nobody presses.
func TestTheListSaysTheLinkKeyIsThere(t *testing.T) {
	m := newLogs(source.Config{Name: "prod", Collector: source.CollectorVictoriaLogs},
		logs.NewStore(storeLimit), "")
	m.resize(120, 40)
	require.Contains(t, ansi.Strip(m.View()), "y link")
}

// watchClipboard replaces the clipboard for one test and reports what was put
// on it, so no test writes to the terminal running it.
func watchClipboard(t *testing.T) *string {
	t.Helper()
	var got string
	prev := copyValue
	copyValue = func(s string) error {
		got = s
		return nil
	}
	t.Cleanup(func() { copyValue = prev })
	return &got
}
