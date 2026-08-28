package ui

import (
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
)

// A run is one row of the list: a line, and the lines straight after it that
// said the same thing.
//
// It is worked out at render time, unlike the shading and the sequence, which
// are settled when a line arrives. What repeats is a property of what is on
// screen and not of the log: two lines a filter has just brought together are a
// repetition, and the same two with a third between them are not. There is
// nothing here worth keeping, and keeping it would be keeping something that
// goes stale on the next keystroke.
type run struct {
	// first indexes the line the row draws, which is the oldest of the run: a
	// source repeating itself grows the count and does not move the row, and
	// the reader keeps the moment it started rather than the moment it last
	// went on.
	first int
	n     int
}

// last indexes the newest line of the run, which is what the silence after it
// is measured from.
func (r run) last() int { return r.first + r.n - 1 }

// clampRuns groups each line with the ones straight after it that repeat it.
// With off, every line is its own row and the list is what it always was.
//
// A run never spans a silence. A line saying the same thing once an hour is a
// heartbeat and not chatter, and folding the two together would take the gap
// between them off the screen: the clamp is there to spend fewer rows on one
// thing being said, never to make the log look busier than it was.
func clampRuns(entries []*logs.Entry, on bool, o logs.Origins) []run {
	runs := make([]run, 0, len(entries))
	var prev *logs.Entry
	for i, e := range entries {
		n := len(runs)
		if _, quiet := gap(prev, e); on && n > 0 && !quiet && repeats(entries[runs[n-1].first], e, o) {
			runs[n-1].n++
			prev = e
			continue
		}
		runs = append(runs, run{first: i, n: 1})
		prev = e
	}
	return runs
}

// repeats reports whether b is another copy of a.
//
// It compares what the line said and nothing else. A line repeated is repeated
// whenever it happened, so the time is not part of it; the source is, because
// two pods of one deployment saying the same thing are two of them saying it
// and not one saying it twice.
func repeats(a, b *logs.Entry, o logs.Origins) bool {
	switch {
	case a.Kind.IsNote() || b.Kind.IsNote():
		// Telescope says a thing for a reason, and says it once. A second one
		// is a second failure and has its own row.
		return false
	case a.Source != b.Source, !o.Same(a, b):
		return false
	case a.Record.TraceID != b.Record.TraceID:
		// One message logged under two requests is two requests failing the
		// same way, and the row has to keep the one the reader is looking at:
		// T opens the trace of the line the row draws, and a fold that spanned
		// ids would open the oldest of them whatever the cursor was on.
		return false
	case a.Record.Body == "":
		// A line with nothing to compare — blank, or a rendering the parser
		// found no message in — is not a copy of anything.
		return false
	}
	return a.Record.Body == b.Record.Body
}

// runAt is the row the line at index i is drawn in, so a cursor that is on a
// line stays on it when the rows are worked out again.
func runAt(runs []run, i int) int {
	for r := range runs {
		if i <= runs[r].last() {
			return r
		}
	}
	return max(len(runs)-1, 0)
}

// rowField is one of a row's fields and whether the row can stand behind it.
//
// The clamp folds lines that said the same sentence, and a well-instrumented
// service puts what actually happened in its attributes: nine lines of "updated
// resource" are nine different resources, and the row drew the first one's name
// as if it were the row's. The count beside it said nine and every field beside
// that said one thing, which is the fold claiming more than it folded.
type rowField struct {
	logs.RowField
	// varies says the lines under this row disagreed about the value, so what
	// is drawn is one of them and not the row's.
	varies bool
}

// clampedRow marks the fields the lines of a run did not agree on.
//
// Only the run is compared, not the source: [logs.Store.Row] has already
// dropped what never varies anywhere, and a key that differs across the log but
// reads the same on all nine of these lines is a fact about this row and is
// drawn as one.
func clampedRow(fields []logs.RowField, entries []*logs.Entry, r run) []rowField {
	out := make([]rowField, len(fields))
	for i, f := range fields {
		out[i] = rowField{RowField: f}
	}
	if r.n < 2 {
		return out
	}
	left := len(out)
	for _, e := range entries[r.first+1 : r.first+r.n] {
		if left == 0 {
			break
		}
		for i := range out {
			if out[i].varies {
				continue
			}
			if valueOf(e, out[i].Key) != out[i].Value {
				out[i].varies, left = true, left-1
			}
		}
	}
	return out
}

// valueOf is what e said for a key, empty where it said nothing: a line that
// left a field out disagrees with one that filled it in.
func valueOf(e *logs.Entry, key string) string {
	for _, f := range e.Row {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// render draws the field as the row can honestly show it.
//
// A value the row cannot stand behind is greyed and given an ellipsis: it is
// still worth seeing — one of nine names says what kind of thing the nine were
// — but it is a sample and not the answer, and the coloring that would say what
// kind of value it is would be saying it about a value the row does not have.
// The key is written even where the field is normally drawn without one, since
// an ellipsis on its own names nothing.
func (f rowField) render() string {
	if !f.varies {
		return f.Render()
	}
	text := f.Value + "…"
	if f.Key != "" {
		text = f.Key + "=" + text
	}
	// Escaped, then stripped of the coloring the escaping carries: grey here
	// means the whole field is a sample, and a reset in the middle of it would
	// hand the rest of the value back its usual colors.
	return styleDim.Render(ansi.Strip(logs.Escape(text)))
}
