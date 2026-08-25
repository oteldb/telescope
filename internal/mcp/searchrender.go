package mcp

import (
	"fmt"
	"strconv"
	"strings"
)

// drawSearch writes what a search found as one line per trace.
//
// Which counts appear is decided by what came back rather than fixed, because
// the two backends count different things and neither can be filled in for the
// other. Drawing a "spans" column against a Tempo answer would put a zero
// beside every trace and read as a trace with no spans in it; drawing
// "matched" against a Jaeger one would read as a query that selected nothing.
// A column nobody can trust is worse than a column that is not there.
func drawSearch(out searchOutput) string {
	b := &strings.Builder{}

	fmt.Fprintf(b, "searched %s: %s\n", out.Store, out.Asked)
	fmt.Fprintf(b, "%s · %s\n", out.Window, plural(out.Returned, "trace"))

	if len(out.Traces) > 0 {
		cols := searchColumns(out.Traces)
		b.WriteString("\n" + searchHead(cols))
		for _, f := range out.Traces {
			b.WriteString(foundLine(f, cols))
		}
	}
	if out.Note != "" {
		fmt.Fprintf(b, "\nnote: %s\n", out.Note)
	}
	return b.String()
}

// searchCols says which counts the store reported, so a column is drawn only
// where it means something.
type searchCols struct {
	spans   bool
	matched bool
	count   int
	width   int
}

func searchColumns(fs []found) searchCols {
	c := searchCols{count: len("matched")}
	for _, f := range fs {
		if f.Spans > 0 || f.Errors > 0 {
			c.spans = true
			c.count = max(c.count, len(spansSaid(f)))
		}
		if f.Matched > 0 {
			c.matched = true
		}
		c.width = max(c.width, len(f.Service)+len(f.Name)+1)
	}
	return c
}

func spansSaid(f found) string {
	said := plural(f.Spans, "span")
	if f.Errors > 0 {
		said += ", " + strconv.Itoa(f.Errors) + " failed"
	}
	return said
}

func searchHead(c searchCols) string {
	head := fmt.Sprintf("%12s  %8s", "started", "dur")
	switch {
	case c.spans:
		head += fmt.Sprintf("  %*s", c.count, "spans")
	case c.matched:
		head += fmt.Sprintf("  %*s", c.count, "matched")
	}
	return head + "  trace\n"
}

func foundLine(f found, c searchCols) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "%12s  %8s", f.At.Format(searchStamp), durOf(f.Duration))
	switch {
	case c.spans:
		fmt.Fprintf(b, "  %*s", c.count, spansSaid(f))
	case c.matched:
		fmt.Fprintf(b, "  %*d", c.count, f.Matched)
	}

	root := strings.TrimSpace(plain(f.Service) + " " + plain(f.Name))
	if root == "" {
		root = "—"
	}
	fmt.Fprintf(b, "  %-*s  %s\n", c.width, root, f.TraceID)
	return b.String()
}

// searchStamp is how a result's start reads. Traces are searched over an hour
// or a day and told apart by the second, so the date is on the window line
// above rather than repeated down the column.
const searchStamp = "15:04:05.000"
