package trace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func window(ms, dur int) Window {
	return Window{
		Start: epoch.Add(time.Duration(ms) * time.Millisecond),
		Dur:   time.Duration(dur) * time.Millisecond,
	}
}

func TestAnInstantFallsWhereTheWindowPutsIt(t *testing.T) {
	w := window(0, 100)
	require.Equal(t, 0.0, w.Cell(epoch, 80))
	require.Equal(t, 40.0, w.Cell(epoch.Add(50*time.Millisecond), 80))
	require.Equal(t, 80.0, w.Cell(epoch.Add(100*time.Millisecond), 80))
}

// Unclamped on purpose: whether something off the edge is drawn as clipped or
// not at all is the caller's decision, and rounding here would take it away.
func TestAnInstantOutsideTheWindowStillHasAPlace(t *testing.T) {
	w := window(50, 100)
	require.Equal(t, -40.0, w.Cell(epoch, 80))
	require.Equal(t, 120.0, w.Cell(epoch.Add(200*time.Millisecond), 80))
}

// A trace of one instantaneous span still has to divide by something.
func TestAWindowIsNeverZeroWide(t *testing.T) {
	tr := Build("t", []Span{at("a", "", "svc", "instant", 0, 0)})
	require.Equal(t, minWindow, Fit(tr).Dur)
	require.Equal(t, minWindow, window(0, 100).Zoom(1e-12, epoch).Dur, "nor after zooming past what a terminal can show")
}

// Zooming with the cursor on a span keeps that span under the cursor.
func TestZoomHoldsWhatItWasAnchoredOn(t *testing.T) {
	w := window(0, 100)
	anchor := epoch.Add(25 * time.Millisecond)
	before := w.Cell(anchor, 80)

	w = w.Zoom(0.5, anchor)
	require.Equal(t, 50*time.Millisecond, w.Dur)
	require.Equal(t, before, w.Cell(anchor, 80), "the anchor did not move on screen")
}

func TestZoomFallsBackToTheMiddleForAnAnchorOffScreen(t *testing.T) {
	w := window(0, 100).Zoom(0.5, epoch.Add(500*time.Millisecond))
	require.Equal(t, epoch.Add(475*time.Millisecond), w.Start)
}

// An edge of empty next to the first span is how a reader sees that it is the
// first, so a window may overhang; it may not wander off entirely.
func TestPanningStopsShortOfNowhere(t *testing.T) {
	bounds := window(0, 100)
	w := bounds.Pan(-time.Hour).Clamp(bounds)
	require.Equal(t, bounds.Start.Add(-bounds.Dur), w.Start)

	w = bounds.Pan(time.Hour).Clamp(bounds)
	require.Equal(t, bounds.End(), w.Start)
}

func TestFocusLeavesRoomEitherSideOfASpan(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("mid", "root", "db", "select", 40, 20),
	})
	mid, _ := tr.Node("mid")

	w := Focus(mid)
	require.True(t, w.Start.Before(mid.Start), "the span does not sit flush against the edge")
	require.True(t, w.End().After(mid.End()))
	require.Equal(t, 24*time.Millisecond, w.Dur)
}

// An axis stepping by 3.7ms is the right density and useless.
func TestTheAxisStepsInNumbersAReaderCanCount(t *testing.T) {
	for _, tt := range []struct {
		want time.Duration
		from time.Duration
	}{
		{time.Nanosecond, 0},
		{time.Nanosecond, time.Nanosecond},
		{5 * time.Nanosecond, 3 * time.Nanosecond},
		{20 * time.Nanosecond, 11 * time.Nanosecond},
		{time.Millisecond, 700 * time.Microsecond},
		{2 * time.Second, 1500 * time.Millisecond},
	} {
		require.Equal(t, tt.want, niceStep(tt.from), "step for %s", tt.from)
	}
}

// The labels have to stay put while somebody pans across them, so offsets are
// counted from the trace and in whole steps.
func TestTheAxisCountsFromTheTraceAndNotTheWindow(t *testing.T) {
	ticks := window(37, 100).Ticks(epoch, 80, 12)
	require.NotEmpty(t, ticks)
	for _, tick := range ticks {
		require.Zero(t, tick.Offset%(20*time.Millisecond), "%s is a round offset", tick.Offset)
		require.GreaterOrEqual(t, tick.Cell, 0)
		require.Less(t, tick.Cell, 80)
	}
	require.Equal(t, 40*time.Millisecond, ticks[0].Offset)
}

func TestTheAxisIsNothingWhereThereIsNoRoom(t *testing.T) {
	require.Empty(t, window(0, 100).Ticks(epoch, 0, 12))
	require.Empty(t, window(0, 100).Ticks(epoch, 80, 0))
}
