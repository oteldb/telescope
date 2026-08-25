package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-faster/errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/trace"
)

// drawSpan answers one span whole, which is what the tree deliberately leaves
// out: a span carries forty attributes and two of them matter, so they are read
// for the span somebody has already picked rather than for all of them at once.
//
// The subtree under it comes too, drawn from that span as its own root. A span
// is asked for because a stub said what was below it, and answering without
// what was below it would send the reader straight back.
func drawSpan(t *trace.Tree, want string) (*sdk.CallToolResult, traceOutput, error) {
	n, ok := findSpan(t, want)
	if !ok {
		return nil, traceOutput{}, errors.Errorf(
			"no span %s in trace %s: the ids are the span= on a row of the tree", want, t.ID)
	}

	sub := subtreeOf(t, n)
	out, rows := describeTrace(sub, 0)
	out.TraceID = t.ID

	b := &strings.Builder{}
	s := n.Span
	fmt.Fprintf(b, "span %s in trace %s\n", ref(s.SpanID), t.ID)
	fmt.Fprintf(b, "%s %s · at %s · %s", plain(s.Service), plain(s.Name),
		atOf(offsetIn(t, n)), durOf(s.Duration))
	if s.Failed() {
		b.WriteString(" · FAILED")
		if s.StatusMessage != "" {
			fmt.Fprintf(b, ": %s", plain(s.StatusMessage))
		}
	}
	b.WriteString("\n")
	if s.ParentID != "" {
		fmt.Fprintf(b, "called by %s\n", ref(s.ParentID))
	}

	if len(s.Attrs) > 0 {
		b.WriteString("\nattributes:\n")
		attrs := append([]attr(nil), attrsOf(s)...)
		sort.Slice(attrs, func(i, j int) bool { return attrs[i].Key < attrs[j].Key })
		w := 0
		for _, a := range attrs {
			w = max(w, len(a.Key))
		}
		for _, a := range attrs {
			fmt.Fprintf(b, "  %-*s  %s\n", w, a.Key, plain(a.Value))
		}
	}

	if len(rows) > 1 {
		fmt.Fprintf(b, "\n%6s  %7s  span\n", "at", "dur")
		for _, r := range rows {
			b.WriteString(spanLine(r))
		}
	}
	if out.Note != "" {
		fmt.Fprintf(b, "\nnote: %s\n", out.Note)
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: b.String()}},
	}, out, nil
}

type attr struct{ Key, Value string }

func attrsOf(s trace.Span) []attr {
	out := make([]attr, 0, len(s.Attrs))
	for _, f := range s.Attrs {
		out = append(out, attr{Key: f.Key, Value: f.String()})
	}
	return out
}

// findSpan takes the shortened id a row printed as well as the whole one, since
// what the tree wrote is what an agent has to give back.
func findSpan(t *trace.Tree, want string) (*trace.Node, bool) {
	want = strings.TrimSpace(want)
	if n, ok := t.Node(want); ok {
		return n, true
	}
	var found *trace.Node
	t.Walk(func(n *trace.Node) bool {
		if strings.HasPrefix(n.Span.SpanID, want) {
			found = n
			return false
		}
		return true
	})
	return found, found != nil
}

// subtreeOf rebuilds one span and its descendants as a tree of their own, so
// that the same walk draws it: a span read on its own is a trace whose root is
// that span, and the depth cut has to fall from there rather than from where it
// happened to sit in the whole.
func subtreeOf(t *trace.Tree, n *trace.Node) *trace.Tree {
	var spans []trace.Span
	var collect func(*trace.Node)
	collect = func(x *trace.Node) {
		s := x.Span
		if x == n {
			// Detached from whatever called it, so the rebuilt tree has it as a
			// root rather than counting it as a span whose parent went missing.
			s.ParentID = ""
		}
		spans = append(spans, s)
		for _, c := range x.Children {
			collect(c)
		}
	}
	collect(n)
	sub := trace.Build(t.ID, spans)
	sub.ClampSkew()
	return sub
}
