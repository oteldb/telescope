package mcp

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oteldb/telescope/internal/trace"
)

// traceIndent is how far one level of nesting moves a row right. Two columns is
// enough to read the shape and cheap enough to spend at depth twelve, where
// four would have pushed the name off into the margin.
const traceIndent = 2

// traceDeepest is how many levels are indented before the indent stops growing.
// Nesting is drawn to be read at a glance, and past a dozen levels it has
// stopped being readable and started being a margin: a chain three hundred deep
// would push the name six hundred columns right, and a failure exempt from the
// depth cut can be exactly that deep. Rows below the cap carry their real depth
// instead, since the shape still has to be recoverable.
const traceDeepest = 12

// spanRef is how much of a span id a row prints. A trace holds a few thousand
// spans at the outside and eight hex digits tell them apart; the whole sixteen
// is a column of noise nobody reads across.
const spanRef = 8

// spanRow is one line of the drawn tree: a span, the repeats folded into it,
// and how deep it sits.
//
// at is where the span sits inside the trace, which is what tells a reader
// whether two siblings ran together or one after the other. It is measured off
// the repaired start and not the raw one, so the arithmetic a reader does on
// that column agrees with the nesting above it — see [trace.Tree.ClampSkew],
// and the note a moved row carries.
type spanRow struct {
	node  *trace.Node
	depth int
	at    time.Duration
	// n is how many identical siblings this row stands for, and slowest the
	// longest of them. One means the row is itself.
	n       int
	slowest time.Duration
	total   time.Duration
	// quiet is a row that stands for what the depth cut left out rather than
	// for a span: how many spans were not drawn at this point in the tree, none
	// of which failed. It has no node, and under is the id of the span they sit
	// beneath — the one to ask for to read them.
	quiet int
	under string
}

// drawTrace writes a trace as the text a model reads: what it was, what failed,
// and the tree.
//
// The failures come before the tree rather than only inside it. A reader that
// scrolls finds a red row by looking; a reader that does not has to be handed
// it, and by the time an agent has read two hundred rows to find the one that
// errored it has paid for the whole trace to learn one line of it.
func drawTrace(out traceOutput, rows []spanRow) string {
	b := &strings.Builder{}

	fmt.Fprintf(b, "trace %s", out.TraceID)
	if out.Root != "" {
		fmt.Fprintf(b, " — %s", out.Root)
	}
	b.WriteString("\n")
	b.WriteString(traceHeadline(out) + "\n")

	if len(out.Failed) > 0 {
		b.WriteString("\nfailed:\n")
		w := failWidths(out.Failed)
		for _, f := range out.Failed {
			fmt.Fprintf(b, "  %-*s  %-*s  %6s  %7s",
				w.service, f.Service, w.name, f.Name, atOf(f.At), durOf(f.Duration))
			if f.Message != "" {
				fmt.Fprintf(b, "  %s", plain(f.Message))
			}
			fmt.Fprintf(b, "  span=%s\n", f.Span)
		}
	}

	if len(rows) > 0 {
		b.WriteString("\n")
		fmt.Fprintf(b, "%6s  %7s  span\n", "at", "dur")
		for _, r := range rows {
			b.WriteString(spanLine(r))
		}
	}
	if out.Note != "" {
		fmt.Fprintf(b, "\nnote: %s\n", out.Note)
	}
	return b.String()
}

// traceHeadline is the line of counts under the name. Detached spans are on it
// rather than in the note: a trace missing three spans is a different object
// from a trace with four roots, and a duration read off the first one is not
// the duration of the request.
func traceHeadline(out traceOutput) string {
	said := []string{
		durOf(out.Duration),
		plural(out.Spans, "span"),
		plural(out.Services, "service"),
	}
	if out.Failures > 0 {
		said = append(said, strconv.Itoa(out.Failures)+" failed")
	}
	if out.Detached > 0 {
		said = append(said, plural(out.Detached, "span")+" whose parent never arrived")
	}
	return strings.Join(said, " · ")
}

func spanLine(r spanRow) string {
	b := &strings.Builder{}
	if r.node == nil {
		fmt.Fprintf(b, "%6s  %7s    %s… %s not drawn, none failed", "", "",
			strings.Repeat(" ", min(r.depth, traceDeepest)*traceIndent), plural(r.quiet, "span"))
		if r.under != "" {
			fmt.Fprintf(b, "  under span=%s", ref(r.under))
		}
		b.WriteString("\n")
		return b.String()
	}
	fmt.Fprintf(b, "%6s  %7s  ", atOf(r.at), durOf(r.total))

	mark := "  "
	if r.node.Span.Failed() {
		mark = "! "
	}
	b.WriteString(mark)
	b.WriteString(strings.Repeat(" ", min(r.depth, traceDeepest)*traceIndent))
	if r.depth > traceDeepest {
		fmt.Fprintf(b, "@%d ", r.depth)
	}

	fmt.Fprintf(b, "%s  %s", plain(r.node.Span.Service), plain(r.node.Span.Name))
	if r.n > 1 {
		fmt.Fprintf(b, "  ×%d (%s slowest)", r.n, durOf(r.slowest))
	}
	// Every row carries its id, leaves included: a leaf is where the statement
	// and the url live, so it is the row most likely to be asked about, and a
	// tree that named only the spans with children would be a dead end at
	// exactly the rows worth reading.
	if r.node.Span.SpanID != "" {
		fmt.Fprintf(b, "  span=%s", ref(r.node.Span.SpanID))
	}
	if r.node.Skew != 0 {
		fmt.Fprintf(b, "  [moved %s to sit inside its caller]", durOf(r.node.Skew.Abs()))
	}
	b.WriteString("\n")
	return b.String()
}

type failWidth struct{ service, name int }

func failWidths(fs []failure) failWidth {
	var w failWidth
	for _, f := range fs {
		w.service = max(w.service, len(f.Service))
		w.name = max(w.name, len(f.Name))
	}
	return w
}

// atOf writes an offset in milliseconds, which is the unit a trace is read in:
// a request is a second or two and its spans are tens of milliseconds, so
// everything lands between three and five digits with no unit to parse.
func atOf(d time.Duration) string {
	return strconv.FormatInt(d.Round(time.Millisecond).Milliseconds(), 10)
}

// durOf writes a duration at the precision it is worth reading at. A span is
// not measured well enough for four significant figures and a column of them
// cannot be scanned.
func durOf(d time.Duration) string {
	switch {
	case d >= time.Minute:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	case d >= time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) + "s"
	case d >= time.Millisecond:
		return strconv.FormatInt(d.Round(time.Millisecond).Milliseconds(), 10) + "ms"
	case d > 0:
		return strconv.FormatInt(d.Round(time.Microsecond).Microseconds(), 10) + "µs"
	default:
		return "0"
	}
}

func ref(id string) string {
	if len(id) <= spanRef {
		return id
	}
	return id[:spanRef]
}

func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.Itoa(n) + " " + thing + "s"
}
