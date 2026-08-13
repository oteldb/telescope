package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/trace"
)

// spanDocument renders one span as the rows it is made of, in the shape the
// entry view already reads.
//
// It shares [item] and everything under it with the log entry rather than
// inventing a second way to draw a key and a value, which is why `Span.Attrs`
// is `[]logs.Field` in the first place: a span attribute is a log attribute
// that arrived on a different endpoint, and the escaping, the wrapping, the
// coloring by key and the copying are all the same problem.
//
// origin is the start of the trace, which is what a span's own time is worth
// reading against — an absolute timestamp says when, and the offset says where
// in the request.
func spanDocument(n *trace.Node, origin time.Time, palette servicePalette, width int) []item {
	var out []item

	labels := []string{"service", "span_id", "parent_id", "trace_id", "started", "duration", "status"}
	for _, f := range n.Attrs {
		labels = append(labels, f.Key)
	}
	indent := labelColumn(labels, width)

	text := func(lines ...string) { out = append(out, item{lines: lines}) }
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
	field := func(key, raw string) { row(key, raw, renderValue(key, raw)) }

	text(styleTitle.Render(logs.Sanitize(n.Name)), "")
	row("service", n.Service, palette.style(n.Service).Render(logs.Sanitize(n.Service)))

	switch {
	case n.Failed() && n.StatusMessage != "":
		row("status", n.StatusMessage, styleErr.Render(logs.Sanitize(n.StatusMessage)))
	case n.Failed():
		row("status", "error", styleErr.Render("error"))
	case n.Status == trace.StatusOK:
		// Only where the span said so. Unset is most spans, and drawing that as
		// "ok" would be telling the reader something the instrumentation did not.
		row("status", "ok", styleOK.Render("ok"))
	}

	if !n.Start.IsZero() {
		stamp := n.Start.Local().Format(time.RFC3339Nano)
		row("started", stamp, stamp+styleDim.Render("  +"+humanDur(n.Start.Sub(origin))+" into the trace"))
	}
	row("duration", n.Duration.String(), durStyle(n).Render(humanDur(n.Duration)))

	// Only worth a row where it happened: a skew of zero is every span in a
	// trace whose clocks agreed.
	if n.Skew != 0 {
		moved := fmt.Sprintf("moved %s to start no earlier than its parent", humanDur(n.Skew))
		text("", styleDim.Render("  "+moved))
	}

	text("", styleTitle.Render("identity"))
	field("span_id", n.SpanID)
	field("parent_id", n.ParentID)
	if n.Detached {
		text("  " + styleErr.Render("that parent is not in this trace"))
	}
	field("trace_id", n.TraceID)

	if len(n.Attrs) > 0 {
		text("", styleTitle.Render("attributes"))
		for _, f := range n.Attrs {
			field(f.Key, f.String())
		}
	}
	return out
}
