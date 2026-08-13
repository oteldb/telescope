package trace

import "time"

// minWindow is the narrowest interval a view is allowed to cover. Zooming is
// unbounded arithmetic and a window of zero divides by it; a microsecond is
// already finer than any span a terminal can distinguish.
const minWindow = time.Microsecond

// Window is the interval a view of a trace covers. It is the view's and not the
// trace's: the whole trace is where reading starts, and everything after that
// is somebody narrowing it.
type Window struct {
	Start time.Time
	Dur   time.Duration
}

// Fit is the window covering the whole trace.
func Fit(t *Tree) Window {
	if t == nil || t.Len() == 0 {
		return Window{Dur: minWindow}
	}
	return Window{Start: t.Start, Dur: max(t.Duration(), minWindow)}
}

// Focus is the window covering one span, with a tenth of it either side so the
// span does not sit flush against both edges with nothing to read it against.
func Focus(n *Node) Window {
	d := max(n.Duration, minWindow)
	pad := d / 10
	return Window{Start: n.Start.Add(-pad), Dur: d + 2*pad}
}

// End is the first instant past the window.
func (w Window) End() time.Time { return w.Start.Add(w.Dur) }

// Cell is where an instant falls, in cells from the left edge of a bar area
// that many wide. It is a float and deliberately unclamped: whether something
// off the left of the window is drawn as a clipped edge or not at all is the
// caller's decision, and rounding here would take it away.
func (w Window) Cell(at time.Time, cells int) float64 {
	if w.Dur <= 0 || cells <= 0 {
		return 0
	}
	return float64(at.Sub(w.Start)) / float64(w.Dur) * float64(cells)
}

// Span is where an interval falls, as a pair of cell positions.
func (w Window) Span(s Span, cells int) (x0, x1 float64) {
	return w.Cell(s.Start, cells), w.Cell(s.End(), cells)
}

// Zoom narrows or widens the window by factor, keeping anchor at the same
// fraction across it — so zooming with the cursor on a span keeps that span
// under the cursor instead of sliding it off the edge.
func (w Window) Zoom(factor float64, anchor time.Time) Window {
	if factor <= 0 || w.Dur <= 0 {
		return w
	}
	dur := max(time.Duration(float64(w.Dur)*factor), minWindow)
	at := float64(anchor.Sub(w.Start)) / float64(w.Dur)
	if at < 0 || at > 1 {
		at = 0.5
	}
	return Window{
		Start: anchor.Add(-time.Duration(at * float64(dur))),
		Dur:   dur,
	}
}

// Pan slides the window without changing what it covers.
func (w Window) Pan(d time.Duration) Window {
	return Window{Start: w.Start.Add(d), Dur: w.Dur}
}

// Clamp keeps a window from wandering off the trace entirely, while still
// allowing it to overhang: an edge of empty next to the first span is how a
// reader sees that it is the first.
//
// The overhang allowed is the window's own width, so however far in somebody
// has zoomed, panning past the end costs the same number of keystrokes to
// undo.
func (w Window) Clamp(bounds Window) Window {
	if w.Dur <= 0 || bounds.Dur <= 0 {
		return w
	}
	if lo := bounds.Start.Add(-w.Dur); w.Start.Before(lo) {
		w.Start = lo
	}
	if hi := bounds.End(); w.Start.After(hi) {
		w.Start = hi
	}
	return w
}

// Tick is one labeled position on the time axis.
type Tick struct {
	// Offset is from the start of the trace, which is what the labels read as:
	// an axis in wall-clock time would be thirteen characters of the same date
	// per tick.
	Offset time.Duration
	Cell   int
}

// Ticks are the axis marks for a bar area cells wide, spaced at least gap
// apart. origin is where offsets are counted from, which is the start of the
// trace and not of the window: the labels have to stay put while somebody pans
// across them.
func (w Window) Ticks(origin time.Time, cells, gap int) []Tick {
	if cells <= 0 || gap <= 0 || w.Dur <= 0 {
		return nil
	}
	step := niceStep(w.Dur * time.Duration(gap) / time.Duration(cells))
	if step <= 0 {
		return nil
	}

	// The first tick at or after the left edge, counted in whole steps from the
	// origin so that panning slides the labels rather than renumbering them.
	from := w.Start.Sub(origin)
	first := from / step * step
	if first < from {
		first += step
	}

	var out []Tick
	for off := first; ; off += step {
		c := int(w.Cell(origin.Add(off), cells))
		if c >= cells {
			break
		}
		if c >= 0 {
			out = append(out, Tick{Offset: off, Cell: c})
		}
	}
	return out
}

// niceStep rounds an interval up to one a reader can do arithmetic in: 1, 2 or
// 5 times a power of ten. An axis stepping by 3.7ms is technically the right
// density and useless.
func niceStep(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Nanosecond
	}
	step := time.Nanosecond
	for step < d {
		for _, m := range [...]time.Duration{2, 5, 10} {
			if step*m >= d {
				return step * m
			}
		}
		step *= 10
	}
	return step
}
