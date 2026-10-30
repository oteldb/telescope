package ui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

func TestControllerConsoleLogsRenderAsStructuredRows(t *testing.T) {
	m := newLogs(source.Config{Collector: source.CollectorKubectl, Target: "controller"}, logs.NewStore(100), "")
	m.resize(140, 30)
	m.clamped = false
	for _, path := range []string{"/version", "/nodes"} {
		line := `2026-10-30T13:50:46Z    DEBUG   proxmox request {"controller":"proxmoxcluster","controllerGroup":"imagebuilder.example.test","name":"demo","method":"GET","path":"` + path + `"}`
		m.append(source.Line{Data: []byte(line)})
	}
	view := ansi.Strip(m.View())
	for _, path := range []string{"/version", "/nodes"} {
		row := rowWith(t, m.View(), path)
		require.Contains(t, ansi.Strip(row), "proxmox request")
		require.NotContains(t, ansi.Strip(row), "controllerGroup")
		require.NotContains(t, ansi.Strip(row), `{"`)
		require.Contains(t, row, "\x1b[", "the path and level retain their colors")
	}
	require.Contains(t, view, "DEBUG")
	m.view.SetFilter(logs.Filter{Query: `level=debug method=GET path="/nodes"`})
	require.Len(t, m.view.Entries(m.store), 1)
	require.NotContains(t, ansi.Strip(m.View()), "/version")
	e := m.store.Entries()[0]
	entry := newEntry(m.cfg, e)
	entry.resize(140, 40)
	doc := ansi.Strip(entry.View())
	require.Contains(t, doc, "controllerGroup")
	require.Contains(t, doc, "imagebuilder.example.test")
}
