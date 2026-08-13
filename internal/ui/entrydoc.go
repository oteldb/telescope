package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/source"
)

// item is one piece of an entry: the lines it draws as, and — when the cursor
// may stop on it — the key and the value as they were received.
//
// The two are kept apart on purpose. What the screen gets is escaped, wrapped
// and colored, and none of that belongs anywhere the value is going next: a
// path with an escape drawn as \e is not a path any editor will open, and a
// wrapped trace id is not a trace id.
type item struct {
	key   string
	value string
	lines []string
	pick  bool
}

// document renders the entry as the rows it is made of. The order is the order
// on screen, and the pickable ones are what the cursor walks.
func (m entryModel) document(width int) []item {
	e := m.entry
	var out []item

	// The header labels are fixed; the fields bring their own, and the widest
	// of all of them is what everything lines up on.
	labels := []string{"received", "source", "level", "trace_id", "span_id", "body"}
	for _, f := range e.Record.Fields {
		labels = append(labels, f.Key)
	}
	origin := m.cfg.SourceLabels(e.Source)
	for _, l := range origin {
		labels = append(labels, l.Key)
	}
	for _, l := range e.Labels {
		labels = append(labels, l.Key)
	}
	indent := labelColumn(labels, width)

	text := func(lines ...string) { out = append(out, item{lines: lines}) }

	// row is a "label  value" the cursor can stop on, drawn as rendered says and
	// carrying raw for whatever is done with it next. What decides whether there
	// is a row is the value received and never its rendering: coloring an empty
	// string still writes the escapes around it, and a blank trace_id the cursor
	// could stop on is a row that narrows by a value nothing has.
	row := func(key, raw, rendered string) {
		if raw == "" {
			return
		}
		out = append(out, item{
			key:   key,
			value: raw,
			pick:  true,
			lines: strings.Split(wrapField(logs.Escape(key), rendered, indent, width), "\n"),
		})
	}
	// A key says what its value is, and a value that is somebody else's bytes
	// says nothing about how it should be drawn.
	field := func(key, raw string) { row(key, raw, renderValue(key, raw)) }

	stream := "stdout"
	if e.Stderr {
		stream = "stderr"
	}
	text(styleTitle.Render(fmt.Sprintf("entry #%d", e.Seq))+styleDim.Render("  "+stream), "")

	// A time the line was written with, whether it said so itself or the source
	// said it for the line; failing both, when it turned up here. The age beside
	// it is for reading, not for copying.
	if e.HasTime {
		t := e.At.Local()
		stamp := t.Format(time.RFC3339Nano)
		row("time", stamp, stamp+styleDim.Render("  "+humanSince(t)))
	} else {
		stamp := e.At.Local().Format(time.RFC3339Nano)
		row("received", stamp, stamp)
	}
	// Which source a line came from, for a merge of several.
	row("source", e.Source, e.Source)
	if e.Record.HasLevel {
		row("level", e.Record.Level.String(), renderLevelWord(e.Record.Level))
	}
	field("trace_id", e.Record.TraceID)
	field("span_id", e.Record.SpanID)
	row("body", e.Record.Body, logs.Escape(e.Record.Body))

	// Where the whole stream comes from, then what this line brought with it.
	// A log database says more about a line than the line does, and none of it
	// fits in the list.
	section := func(title string, labels []source.Label) {
		if len(labels) == 0 {
			return
		}
		text("", styleTitle.Render(title))
		for _, l := range labels {
			field(l.Key, l.Value)
		}
	}
	section("source", origin)
	section("labels", e.Labels)

	// The full rendering, stacktrace and all: this is where it belongs. It is one
	// item however many lines it runs to, because it is one thing to copy.
	text("", styleTitle.Render("rendered"))
	out = append(out, item{
		key:   "rendered",
		value: e.Text,
		pick:  true,
		lines: block(e.Text, width, func(l string) string { return l }),
	})

	if len(e.Record.Fields) > 0 {
		text("", styleTitle.Render("fields"))
		for _, f := range e.Record.Fields {
			field(f.Key, f.String())
		}
	}

	// The entry as it arrived. Copying this is copying the whole entry, which is
	// why the shortcut for it and this row take the same value.
	text("", styleTitle.Render("raw"))
	out = append(out, item{
		key:   "raw",
		value: string(e.Raw),
		pick:  true,
		lines: block(prettyJSON(e.Raw), width, func(l string) string {
			// Escaping brings its own color, and dimming it over would put the
			// escapes back at the mercy of what they escaped.
			if escaped := logs.Escape(l); escaped != l {
				return escaped
			}
			return styleDim.Render(l)
		}),
	})
	return out
}

// block draws a multi-line value indented under its heading, each line truncated
// rather than wrapped: a stacktrace reads by its left edge.
func block(s string, width int, render func(string) string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		out = append(out, "  "+ansi.Truncate(render(l), max(width-2, 1), "…"))
	}
	return out
}

// term is the query that picks out the lines sharing this row's value, or nil
// where narrowing by it would mean nothing. A timestamp selects the one line it
// came from, and a rendering or a raw line selects itself; what is worth
// narrowing by is what a line has in common with others.
func (it item) term() query.Expr {
	switch it.key {
	case "", "time", "received", "body", "rendered", "raw":
		return nil
	case "level":
		l, ok := query.ParseLevel(it.value)
		if !ok {
			return nil
		}
		return query.Level{Op: query.OpEq, Level: l}
	default:
		return query.Field{Key: it.key, Op: query.OpEq, Value: it.value}
	}
}

// starts is the screen line each item begins on.
func starts(doc []item) []int {
	out := make([]int, len(doc))
	at := 0
	for i, it := range doc {
		out[i] = at
		at += len(it.lines)
	}
	return out
}

// picks returns the indices of the items the cursor may stop on.
func picks(doc []item) []int {
	var out []int
	for i, it := range doc {
		if it.pick {
			out = append(out, i)
		}
	}
	return out
}
