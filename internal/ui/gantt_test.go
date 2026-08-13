package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/trace"
)

var traceEpoch = time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

func span(id, parent, service, name string, start, dur time.Duration) trace.Span {
	return trace.Span{
		TraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:   id,
		ParentID: parent,
		Service:  service,
		Name:     name,
		Start:    traceEpoch.Add(start),
		Duration: dur,
	}
}

const (
	ms = time.Millisecond
	us = time.Microsecond
)

// checkout is a request across four services, with the things that make a real
// trace awkward in it: a span too short to fill a cell, one that outlives the
// response, and a failure three levels down.
func checkout() *trace.Tree {
	failed := span("db-write", "charge", "postgres", "INSERT orders", 130*ms, 210*ms)
	failed.Status = trace.StatusError
	failed.StatusMessage = "deadlock detected"

	return trace.Build("4bf92f3577b34da6a3ce929d0e0e4736", []trace.Span{
		span("gw", "", "gateway", "POST /checkout", 0, 480*ms),
		span("auth", "gw", "identity", "verify token", 5*ms, 40*ms),
		span("cache", "auth", "redis", "GET session", 12*ms, 900*us),
		span("charge", "gw", "checkout", "charge order", 60*ms, 380*ms),
		span("rates", "charge", "payments", "GET rates", 65*ms, 30*ms),
		failed,
		span("retry", "charge", "postgres", "INSERT orders", 350*ms, 80*ms),
		span("flush", "gw", "postgres", "flush wal", 470*ms, 90*ms),
	})
}

func ganttOf(t *testing.T, tr *trace.Tree) *gantt {
	t.Helper()
	g := newGantt(tr)
	require.NotNil(t, g.at())
	return g
}

func render(t *testing.T, g *gantt, width, height int) string {
	t.Helper()
	return ansi.Strip(g.View(width, height))
}

// Every column the gantt draws has to end where the frame does, or a row that
// overflowed would wrap and push the whole picture down by a line.
func TestNoRowIsWiderThanTheTerminal(t *testing.T) {
	g := ganttOf(t, checkout())
	for _, width := range []int{40, 60, 80, 100, 160} {
		for row := range strings.SplitSeq(render(t, g, width, 20), "\n") {
			require.LessOrEqual(t, ansi.StringWidth(row), width, "at width %d: %q", width, row)
		}
	}
}

func TestASpanReadsAsItsServiceAndItsName(t *testing.T) {
	out := render(t, ganttOf(t, checkout()), 100, 20)
	require.Contains(t, out, "gateway POST /checkout")
	require.Contains(t, out, "postgres INSERT orders")
	require.Contains(t, out, "480ms")
}

// The bars are only readable against something that says how long they are.
func TestTheSummarySaysWhatTheTraceIs(t *testing.T) {
	out := render(t, ganttOf(t, checkout()), 100, 20)
	require.Contains(t, out, "8 spans")
	require.Contains(t, out, "6 services")
	require.Contains(t, out, "560ms", "measured to the last span, not the root")
}

// A span whose parent never arrived means the trace may still be being
// written, which is worth saying before somebody reads a latency off it.
func TestAMissingParentIsSaidOutLoud(t *testing.T) {
	tr := trace.Build("t", []trace.Span{
		span("orphan", "gone", "checkout", "charge", 0, 60*ms),
	})
	require.Contains(t, render(t, ganttOf(t, tr), 100, 20), "1 span missing a parent")
}

func TestTheTreeIsDrawnDownTheNameColumn(t *testing.T) {
	out := render(t, ganttOf(t, checkout()), 100, 20)
	require.Contains(t, out, "├─", "a span with siblings below it")
	require.Contains(t, out, "└─", "and the last of them")
	require.Contains(t, out, "│ ", "and the line past a row to its uncle")
}

// The whole point of the view: a bar's left edge is when the span started.
func TestABarStartsWhereTheSpanDid(t *testing.T) {
	tr := trace.Build("t", []trace.Span{
		span("root", "", "gateway", "GET /", 0, 100*ms),
		span("half", "root", "db", "select", 50*ms, 50*ms),
	})
	g := ganttOf(t, tr)
	_, barWidth := layout(100)

	require.Zero(t, barStart(bar(t, g, "GET /")), "the request opens the window")
	require.InDelta(t, barWidth/2, barStart(bar(t, g, "select")), 1,
		"and the child starts halfway across it")
}

// A span too short to cover a cell is still a span that happened.
func TestASpanTooShortToDrawIsStillDrawn(t *testing.T) {
	out := render(t, ganttOf(t, checkout()), 100, 20)
	var line string
	for row := range strings.SplitSeq(out, "\n") {
		if strings.Contains(row, "GET session") {
			line = row
		}
	}
	require.NotEmpty(t, line)
	require.True(t, strings.ContainsAny(line, "▏▎▍▌▋▊▉█▐"), "a 900µs span in a 560ms trace: %q", line)
}

func TestTheAxisCountsFromTheStartOfTheTrace(t *testing.T) {
	axis := strings.Split(render(t, ganttOf(t, checkout()), 100, 20), "\n")[1]
	require.Contains(t, axis, "0ns", "the trace opens at zero")
	require.Contains(t, axis, "ms")
}

func TestFoldingHidesASubtreeAndCountsIt(t *testing.T) {
	g := ganttOf(t, checkout())
	before := len(strings.Split(render(t, g, 100, 20), "\n"))

	g.move(3) // charge
	require.Equal(t, "charge order", g.at().Name)
	g.toggle()

	out := render(t, g, 100, 20)
	require.NotContains(t, out, "GET rates")
	require.Contains(t, out, "+3")
	require.Less(t, len(strings.Split(out, "\n")), before)
}

// Folding must not be a way to lose the one span somebody opened the trace for.
func TestAFoldOverAFailureSaysSo(t *testing.T) {
	g := ganttOf(t, checkout())
	g.move(3)
	g.toggle()
	require.Contains(t, render(t, g, 100, 20), "+3 ✗")
}

// Folding a subtree the cursor was inside has to put the cursor somewhere the
// reader is looking, which is the fold itself.
func TestTheCursorFollowsWhatItWasOn(t *testing.T) {
	g := ganttOf(t, checkout())
	g.move(4) // GET rates, inside charge
	require.Equal(t, "GET rates", g.at().Name)

	charge, ok := g.tree.Node("charge")
	require.True(t, ok)
	g.collapsed[charge] = true
	g.reflow()
	require.Equal(t, "charge order", g.at().Name)
}

func TestZoomingHoldsTheSpanUnderTheCursor(t *testing.T) {
	g := ganttOf(t, checkout())
	g.move(5) // the failed insert
	require.Equal(t, "INSERT orders", g.at().Name)

	at := func() int { return barStart(bar(t, g, "INSERT orders")) }
	before := at()
	g.zoom(0.5)
	require.InDelta(t, before, at(), 1, "the span did not slide out from under the cursor")
	require.Less(t, g.win.Dur, g.bounds.Dur)
}

func TestFitReturnsToTheWholeTrace(t *testing.T) {
	g := ganttOf(t, checkout())
	g.zoom(0.25)
	g.pan(0.5)
	require.NotEqual(t, g.bounds, g.win)

	g.fit()
	require.Equal(t, g.bounds, g.win)
}

// Zoomed in far enough, most of the trace is off the edge, and a bar that ran
// off it has to say which way it went.
func TestASpanRunningOffTheWindowSaysSo(t *testing.T) {
	g := ganttOf(t, checkout())
	g.move(3)
	g.focus()

	row := bar(t, g, "POST /checkout")
	require.Contains(t, row, "‹", "the request began before this window")
	require.Contains(t, row, "›", "and ended after it")
}

// bar is the bar area of the row naming a span, taken in runes: every glyph the
// gantt draws is one cell wide, and none of them is one byte.
func bar(t *testing.T, g *gantt, name string) string {
	t.Helper()
	_, barWidth := layout(100)
	for row := range strings.SplitSeq(render(t, g, 100, 20), "\n") {
		if strings.Contains(row, name) {
			cells := []rune(row)
			return string(cells[len(cells)-barWidth:])
		}
	}
	t.Fatalf("no row for %q", name)
	return ""
}

// barStart is the cell a bar begins in, counted in cells and not in bytes.
func barStart(row string) int {
	for i, r := range []rune(row) {
		if r != ' ' {
			return i
		}
	}
	return -1
}

func TestADurationReadsAsATraceWritesOne(t *testing.T) {
	for _, tt := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0ns"},
		{750 * time.Nanosecond, "750ns"},
		{1500 * time.Nanosecond, "1.5µs"},
		{900 * us, "900µs"},
		{1234 * us, "1.23ms"},
		{15500 * us, "15.5ms"},
		{480 * ms, "480ms"},
		{1500 * ms, "1.5s"},
		{45 * time.Second, "45s"},
		{125 * time.Second, "2m05s"},
		{-5 * ms, "-5ms"},
	} {
		require.Equal(t, tt.want, humanDur(tt.in), "%s", tt.in)
	}
}

func TestABarIsDrawnWhereItsCellsSay(t *testing.T) {
	for _, tt := range []struct {
		name   string
		x0, x1 float64
		want   string
		cells  int
	}{
		{"the whole width", 0, 8, "████████", 8},
		{"half of it", 0, 4, "████    ", 8},
		{"the right half", 4, 8, "    ████", 8},
		{"an eighth past a cell", 0, 2.125, "██▏     ", 8},
		{"starting mid-cell", 2.5, 6, "  ▐███  ", 8},
		{"inside one cell", 3.1, 3.4, "   ▌    ", 8},
		{"nothing at all", 3, 3, "   ▏    ", 8},
		{"off the left", -5, -1, "‹       ", 8},
		{"off the right", 9, 12, "       ›", 8},
		{"across the whole window", -3, 12, "‹██████›", 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, string(barCells(tt.cells, tt.x0, tt.x1)))
		})
	}
}

// The name column gives way first: the bars are what the view is for, and a
// name column that ate an eighty-column terminal would leave nothing to read
// them in.
func TestTheColumnsShareWhateverWidthThereIs(t *testing.T) {
	for _, width := range []int{40, 80, 120, 200} {
		name, bar := layout(width)
		require.LessOrEqual(t, name+bar+durWidth+2, width, "at width %d", width)
		require.GreaterOrEqual(t, bar, barMin, "at width %d", width)
	}
	name, bar := layout(24)
	require.Zero(t, bar, "and below that there is no bar area worth drawing")
	require.Positive(t, name)
}
