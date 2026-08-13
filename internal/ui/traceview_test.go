package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/jx"

	"github.com/oteldb/telescope/internal/logs"
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

// A hundred-span trace is read by its shape first, and folding down to that one
// keystroke at a time is not reading.
func TestCollapseAndExpandAll(t *testing.T) {
	m := traceModelOf(t, checkout())
	full := len(m.(Model).trace.g.rows)

	// Down to the request and what it called directly, which is the shape
	// somebody opens a trace to see. Folding the root too would leave one row
	// and nothing to read off it.
	m, _ = m.Update(k("C"))
	rows := m.(Model).trace.g.rows
	require.Len(t, rows, 4)
	require.Equal(t, "POST /checkout", rows[0].Name)
	for _, n := range rows[1:] {
		require.Equal(t, 1, n.Depth, "the top level, and nothing below it")
	}
	require.NotContains(t, screen(t, m), "GET rates")

	m, _ = m.Update(k("E"))
	require.Len(t, m.(Model).trace.g.rows, full)
}

func TestTheServicePickerListsWhatTheTraceTouched(t *testing.T) {
	m := traceModelOf(t, checkout())
	m, _ = m.Update(k("s"))

	out := screen(t, m)
	require.Contains(t, out, "services")
	// Ordered by how much of the trace each one is, so the busiest is first.
	require.Contains(t, out, "3 postgres")
	require.Contains(t, out, "1 gateway")
	require.Less(t, strings.Index(out, "postgres"), strings.Index(out, "gateway"))
}

// Hiding a service must not take with it the spans below that are still shown:
// the tree is what says who called whom.
func TestHidingAServiceKeepsWhatHoldsUpTheRest(t *testing.T) {
	tr := trace.Build("t", []trace.Span{
		span("root", "", "gateway", "GET /", 0, 100*ms),
		span("mid", "root", "sidecar", "proxy", 10*ms, 80*ms),
		span("leaf", "mid", "db", "select", 20*ms, 30*ms),
	})
	m := traceModelOf(t, tr)

	m, _ = m.Update(k("s"))
	// Onto "sidecar" — one span each, so they are listed by name.
	for range 3 {
		if name, _ := m.(Model).trace.pick.at(); name == "sidecar" {
			break
		}
		m, _ = m.Update(k("j"))
	}
	name, _ := m.(Model).trace.pick.at()
	require.Equal(t, "sidecar", name)

	m, _ = m.Update(k(" "))
	m, _ = m.Update(k("esc"))

	out := screen(t, m)
	require.Contains(t, out, "db select", "what was under it is still there")
	require.Contains(t, out, "sidecar proxy", "and so is the span holding it up")
	require.Contains(t, out, "2 of 3 services", "and the header says a filter is on")

	m, _ = m.Update(k("s"))
	m, _ = m.Update(k("a"))
	require.Empty(t, m.(Model).trace.g.hidden)
}

// The attributes are the reason to open a span at all.
func TestASpanOpensAsItsAttributes(t *testing.T) {
	failed := span("db", "", "orders-db", "INSERT orders", 0, 10*ms)
	failed.Status = trace.StatusError
	failed.StatusMessage = "deadlock detected"
	failed.Attrs = []logs.Field{
		{Key: "db.statement", Value: jx.Raw(`"INSERT INTO orders"`)},
		{Key: "db.rows", Value: jx.Raw(`3`)},
	}
	m := traceModelOf(t, trace.Build("t", []trace.Span{failed}))

	m, _ = m.Update(k("enter"))
	out := screen(t, m)
	require.Contains(t, out, "INSERT orders")
	require.Contains(t, out, "orders-db")
	require.Contains(t, out, "deadlock detected")
	require.Contains(t, out, "db.statement")
	require.Contains(t, out, "INSERT INTO orders")
	require.Contains(t, out, "db.rows")
	require.Contains(t, out, "into the trace", "and where in the request it ran")

	m, _ = m.Update(k("esc"))
	require.Contains(t, screen(t, m), "orders-db INSERT orders", "esc goes back to the chart")
}

// What a row draws as and what it carries are two values, in a span exactly as
// in a log entry.
func TestCopyingASpanRowTakesTheValueAsItArrived(t *testing.T) {
	s := span("db", "", "orders-db", "INSERT orders", 0, 10*ms)
	s.Attrs = []logs.Field{{Key: "db.statement", Value: jx.Raw(`"SELECT 1"`)}}
	g := newGantt(trace.Build("t", []trace.Span{s}))

	doc := spanDocument(g.rows[0], g.bounds.Start, g.palette, 80)
	var found bool
	for _, it := range doc {
		if it.key == "db.statement" {
			require.Equal(t, "SELECT 1", it.value, "unquoted, and not the rendering")
			found = true
		}
	}
	require.True(t, found, "the attribute is a row the cursor can stop on")
}

// A span row has no log entry behind it, and every row that is not a file or a
// URL reaches the stacktrace fallback, which used to read one.
func TestOpeningASpanRowThatPointsNowhere(t *testing.T) {
	s := span("db", "", "orders-db", "INSERT orders", 0, 10*ms)
	s.Attrs = []logs.Field{
		{Key: "db.statement", Value: jx.Raw(`"SELECT 1"`)},
		{Key: "code.line.number", Value: jx.Raw(`42`)},
	}
	m := traceModelOf(t, trace.Build("t", []trace.Span{s}))
	m, _ = m.Update(k("enter"))

	// Every row of the span, including trace_id and a bare line number, which
	// is the pair that goes looking for a file in the entry.
	doc := m.(Model).trace.spanDoc()
	require.NotEmpty(t, doc)
	for _, it := range doc {
		if !it.pick {
			continue
		}
		require.NotPanics(t, func() {
			require.IsType(t, noteMsg{}, openCmd(nil, it)(), "row %q opens nothing", it.key)
		}, "row %q", it.key)
	}
}

// An attribute that does point somewhere still opens, entry or no entry.
func TestASpanAttributeThatIsAURLOpens(t *testing.T) {
	it := item{key: "http.url", value: "https://example.com/orders", pick: true}
	msg, ok := openCmd(nil, it)().(openMsg)
	require.True(t, ok)
	require.Equal(t, "https://example.com/orders", msg.target.url)
}
