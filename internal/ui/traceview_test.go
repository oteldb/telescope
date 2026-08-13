package ui

import (
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/trace"
)

func traceModelOf(t *testing.T, tr *trace.Tree) tea.Model {
	t.Helper()
	m := NewTrace(tr)
	var out tea.Model = m
	out, _ = out.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return out
}

// The whole point of the wiring: a trace off the wire reaches the screen.
func TestATraceFromAResponseReachesTheScreen(t *testing.T) {
	data, err := os.ReadFile("../trace/testdata/checkout.json")
	require.NoError(t, err)
	found, err := trace.DecodeJaeger(data)
	require.NoError(t, err)

	out := screen(t, traceModelOf(t, found[0]))
	require.Contains(t, out, "gateway POST /checkout")
	require.Contains(t, out, "orders-db INSERT orders")
	require.Contains(t, out, "6 spans")
	require.Contains(t, out, "480ms")
	require.Contains(t, out, "█", "and is drawn as bars")
}

// The row is truncated to a column, so what the cursor is on is said in full
// underneath it.
func TestTheFooterSaysWhatTheCursorIsOn(t *testing.T) {
	m := traceModelOf(t, checkout())
	require.Contains(t, screen(t, m), "gw", "the root's span id")

	m, _ = m.Update(k("j"))
	m, _ = m.Update(k("j"))
	m, _ = m.Update(k("j"))
	m, _ = m.Update(k("j"))
	m, _ = m.Update(k("j"))
	require.Contains(t, screen(t, m), "deadlock detected", "and why a failed span failed")
}

func TestTheKeysMoveTheCursorAndTheWindow(t *testing.T) {
	m := traceModelOf(t, checkout())
	g := m.(Model).trace.g

	m, _ = m.Update(k("j"))
	require.Equal(t, 1, g.cursor)
	m, _ = m.Update(k("G"))
	require.Equal(t, len(g.rows)-1, g.cursor)
	m, _ = m.Update(k("g"))
	require.Zero(t, g.cursor)

	before := g.win
	m, _ = m.Update(k("+"))
	require.Less(t, g.win.Dur, before.Dur)
	m, _ = m.Update(k("0"))
	require.Equal(t, g.bounds, g.win)

	m, _ = m.Update(k("l"))
	require.True(t, g.win.Start.After(g.bounds.Start), "and pans off the start")
	require.NotEmpty(t, screen(t, m))
}

func TestSpaceFoldsWhatTheCursorIsOn(t *testing.T) {
	m := traceModelOf(t, checkout())
	for range 3 {
		m, _ = m.Update(k("j"))
	}
	require.Equal(t, "charge order", m.(Model).trace.g.at().Name)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	out := screen(t, m)
	require.NotContains(t, out, "GET rates")
	require.Contains(t, out, "+3 ✗")
}

// A trace read from a file has nothing under it, and a start screen that cannot
// reopen it would be a trapdoor rather than a way back.
func TestLeavingATraceOpenedOnItsOwnQuits(t *testing.T) {
	m := traceModelOf(t, checkout())
	_, cmd := m.Update(k("esc"))
	require.NotNil(t, cmd)
	require.IsType(t, backMsg{}, cmd())

	m, cmd = m.Update(backMsg{})
	require.Equal(t, stateTrace, m.(Model).state, "and it did not fall through to the start screen")
	require.NotNil(t, cmd)
}

// Colors are handed out in first-seen order so that no two services in a trace
// share one, which a hash into the same palette could not promise.
func TestTwoServicesInATraceNeverShareAColor(t *testing.T) {
	var spans []trace.Span
	for i, name := range []string{
		"gateway", "identity", "sessions", "checkout", "payments", "orders-db",
		"search", "cart", "inventory", "shipping", "email", "billing",
	} {
		spans = append(spans, span(string(rune('a'+i)), "", name, "op", 0, ms))
	}
	p := newServicePalette(trace.Build("t", spans))

	seen := map[int]string{}
	for service, idx := range p {
		require.NotContains(t, seen, idx, "%s and %s share a color", seen[idx], service)
		seen[idx] = service
	}
	require.Len(t, seen, 12)
}

// The same trace has to draw the same way every time anybody opens it, which is
// what a per-trace counter buys over jaeger-ui's per-session one.
func TestATraceDrawsTheSameWayTwice(t *testing.T) {
	first := ansi.Strip(newGantt(checkout()).View(100, 24))
	second := ansi.Strip(newGantt(checkout()).View(100, 24))
	require.Equal(t, first, second)

	colored := newGantt(checkout()).View(100, 24)
	require.NotEqual(t, colored, first, "and it is drawn in color")

	// Read off a row the cursor is not on: the cursor lays a gradient under its
	// row, which re-arms the background between every few cells.
	p := newServicePalette(checkout())
	require.Contains(t, colored, p.style("identity").Render("identity"),
		"out of the palette the trace was given")
	require.Zero(t, p["gateway"], "and the root's service took the first swatch")
}
