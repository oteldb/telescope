package mcp

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
)

// The two ways a line's time is written. A window inside one day says the day
// once, in the header, and spends nothing on repeating it four hundred times.
const (
	clockStamp = "15:04:05.000"
	dateStamp  = "2006-01-02 15:04:05.000"
	// A bucket is a stretch of time and never shorter than a second, so the
	// milliseconds a line is dated to would be three zeroes on every one.
	bucketStamp = "15:04:05"
)

// sourceKey is what the stream a line came from is called when it is written
// out beside the line's own fields. A merge is the only place it says anything.
const sourceKey = "source"

// row is a line and the lines straight after it that said the same thing.
//
// The list works the same thing out at draw time and cannot share it: what
// repeats there depends on the silence between two lines and on which label
// tells that view's streams apart, both of which are properties of a screen
// being watched. This is a finished answer, and the reader is counting.
type row struct {
	// entry is the oldest line of the run, which is the one drawn: a source
	// repeating itself says the same thing, and when it started is the part
	// worth keeping. latest is when it last went on, which is what the window
	// in the header is measured to.
	entry  *logs.Entry
	latest time.Time
	n      int
}

func fold(entries []*logs.Entry) []row {
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		if n := len(rows); n > 0 && repeats(rows[n-1].entry, e) {
			rows[n-1].n++
			rows[n-1].latest = e.At
			continue
		}
		rows = append(rows, row{entry: e, latest: e.At, n: 1})
	}
	return rows
}

// repeats reports whether b says what a said. Telescope's own notes never
// repeat: a second one is a second failure and worth reading.
func repeats(a, b *logs.Entry) bool {
	switch {
	case a.Kind.IsNote() || b.Kind.IsNote():
		return false
	case a.Source != b.Source, a.Record.TraceID != b.Record.TraceID:
		return false
	case a.Record.Body == "":
		return false
	}
	return a.Record.Body == b.Record.Body
}

// split separates what every line says from what tells them apart.
//
// The list decides this from everything it has ever held, because a key can
// still be disagreed with by the next line to arrive. Here the answer is
// finished: a key that reads the same on all of it is constant for this
// question, whatever the stream does next.
func split(entries []*logs.Entry) (common map[string]string, varies []string) {
	if len(entries) < 2 {
		return nil, keysOf(entries)
	}
	shared := maps.Clone(fieldsOf(entries[0]))
	for _, e := range entries[1:] {
		fields := fieldsOf(e)
		for k, v := range shared {
			if got, ok := fields[k]; !ok || got != v {
				delete(shared, k)
			}
		}
	}
	for _, k := range keysOf(entries) {
		if _, ok := shared[k]; !ok {
			varies = append(varies, k)
		}
	}
	if len(shared) == 0 {
		shared = nil
	}
	return shared, varies
}

// fieldsOf is what one line is labeled with, as the row would show it: the
// message, the time, the level and the trace id are drawn from the record
// itself and are not labels of it.
func fieldsOf(e *logs.Entry) map[string]string {
	out := make(map[string]string, len(e.Row)+1)
	if e.Source != "" {
		out[sourceKey] = e.Source
	}
	for _, f := range e.Row {
		out[f.Key] = f.Value
	}
	return out
}

// keysOf is every key the lines carry, in the order the first line to carry
// one wrote it: the row order is what a reader scans, and sorting it would
// throw that away.
func keysOf(entries []*logs.Entry) []string {
	var (
		out  []string
		seen = map[string]bool{}
	)
	for _, e := range entries {
		if e.Source != "" && !seen[sourceKey] {
			seen[sourceKey] = true
			out = append(out, sourceKey)
		}
		for _, f := range e.Row {
			if !seen[f.Key] {
				seen[f.Key] = true
				out = append(out, f.Key)
			}
		}
	}
	return out
}

// draw writes the answer as the lines themselves, which is what the model
// reads. The facts about it travel beside it as structured content and are
// summarized in the header rather than repeated line by line.
func draw(out logsOutput, rows []row) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "place=%s  read=%d matched=%d shown=%d\n",
		out.Place, out.Read, out.Matched, out.Returned)

	stamp := clockStamp
	if first, last, ok := span(rows); ok {
		if first.Format(time.DateOnly) == last.Format(time.DateOnly) {
			fmt.Fprintf(b, "window: %s %s..%s\n", first.Format(time.DateOnly),
				first.Format(clockStamp), last.Format(clockStamp))
		} else {
			stamp = dateStamp
			fmt.Fprintf(b, "window: %s..%s\n", first.Format(dateStamp), last.Format(dateStamp))
		}
	}
	for _, p := range out.Pushed {
		switch {
		case p.Query != "":
			fmt.Fprintf(b, "asked %s: %s\n", p.Place, p.Query)
		default:
			fmt.Fprintf(b, "asked %s: nothing — it answers no query of its own, "+
				"so the filter was applied here\n", p.Place)
		}
	}
	if len(out.Common) > 0 {
		b.WriteString("common:")
		for _, k := range sortedKeys(out.Common) {
			fmt.Fprintf(b, " %s=%s", k, plain(out.Common[k]))
		}
		b.WriteByte('\n')
	}

	for _, r := range rows {
		b.WriteString(line(r, out.Common, stamp))
		b.WriteByte('\n')
	}
	if out.Note != "" {
		fmt.Fprintf(b, "note: %s\n", out.Note)
	}
	return b.String()
}

func line(r row, common map[string]string, stamp string) string {
	e := r.entry
	b := &strings.Builder{}
	if e.HasTime || !e.At.IsZero() {
		b.WriteString(e.At.Format(stamp))
	}
	if r.n > 1 {
		fmt.Fprintf(b, " ×%d", r.n)
	}
	switch {
	case e.Kind.IsNote():
		// Telescope talking rather than the source, which is the one thing here
		// that is not a log line and has to be readable as such.
		b.WriteString(" !! ")
		b.WriteString(plain(e.Record.Body))
		return b.String()
	case e.Record.HasLevel:
		b.WriteByte(' ')
		b.WriteString(e.Record.Level.CapitalString())
	}
	for _, f := range e.Row {
		if _, ok := common[f.Key]; ok {
			continue
		}
		fmt.Fprintf(b, " %s=%s", f.Key, plain(f.Value))
	}
	if _, ok := common[sourceKey]; !ok && e.Source != "" {
		fmt.Fprintf(b, " %s=%s", sourceKey, e.Source)
	}
	if e.Record.TraceID != "" {
		fmt.Fprintf(b, " trace=%s", e.Record.TraceID)
	}
	if e.Record.Body != "" {
		b.WriteString("  ")
		b.WriteString(plain(e.Record.Body))
	}
	return b.String()
}

// drawSummary writes the shape of a window: the counts, and where in it the
// volume went. A bar is drawn beside each bucket because a spike is what the
// question was about, and a column of numbers hides one.
func drawSummary(out summaryOutput) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "place=%s  counted=%d\n", out.Place, out.Counted)
	if len(out.Levels) > 0 {
		b.WriteString("levels:")
		for _, l := range out.Levels {
			fmt.Fprintf(b, " %s=%d", l.Name, l.Count)
		}
		b.WriteByte('\n')
	}
	if len(out.Buckets) > 0 {
		fmt.Fprintf(b, "volume, every %s:\n", out.Every)
		peak := 0
		for _, k := range out.Buckets {
			peak = max(peak, k.Count)
		}
		for _, k := range out.Buckets {
			fmt.Fprintf(b, "  %s %-*s %d", k.At, barWidth, bar(k.Count, peak), k.Count)
			if k.Errors > 0 {
				fmt.Fprintf(b, " error=%d", k.Errors)
			}
			b.WriteByte('\n')
		}
	}
	writeTally(b, "top messages", out.Messages)
	if out.ByField != "" {
		writeTally(b, "by "+out.ByField, out.By)
	}
	if out.Note != "" {
		fmt.Fprintf(b, "note: %s\n", out.Note)
	}
	return b.String()
}

// barWidth is how wide the volume bars are drawn. A bar is a comparison and
// not a measurement — the count beside it is the measurement — so it is as
// narrow as it can be and still show a spike.
const barWidth = 20

func bar(n, peak int) string {
	if peak <= 0 || n <= 0 {
		return ""
	}
	return strings.Repeat("#", max(n*barWidth/peak, 1))
}

func writeTally(b *strings.Builder, title string, counts []tally) {
	if len(counts) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, c := range counts {
		fmt.Fprintf(b, "  %6d  %s\n", c.Count, plain(c.Name))
	}
}

// span is when the first and last of the shown lines were written.
func span(rows []row) (first, last time.Time, ok bool) {
	for _, r := range rows {
		if r.entry.At.IsZero() {
			continue
		}
		if !ok {
			first, ok = r.entry.At, true
		}
		last = r.latest
	}
	return first, last, ok
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// plain is a value as it may be handed to a reader that is not a terminal:
// somebody else's bytes, with the control characters written out rather than
// obeyed, and without the coloring a rendering for a screen carries.
func plain(s string) string { return logs.Escape(ansi.Strip(s)) }
