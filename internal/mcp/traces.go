package mcp

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/trace"
)

const traceDescription = "Reads one trace by its id and draws it as a tree: " +
	"every span, who called whom, when each started relative to the request and " +
	"how long it took. What failed is listed above the tree as well as marked in " +
	"it. Span attributes are not included — name a span to read those."

// traceRows is how many rows a drawn tree is allowed. A trace that fits is
// drawn whole; one that does not is cut by depth, which is the only cut a
// reader can reason about without being told what was thrown away.
const traceRows = 200

// traceFetchTimeout bounds the fetch. A trace is one request with an end,
// unlike the streams telescope otherwise opens.
const traceFetchTimeout = 30 * time.Second

type traceInput struct {
	ID    string `json:"id" jsonschema:"The trace id, as it appears on a log line"`
	Place string `json:"place,omitempty" jsonschema:"Which store to read, as places reports it: a store, or a place whose logs carry ids into one. May be left out when the config declares a single store"`
	Span  string `json:"span,omitempty" jsonschema:"Read one span whole, with its attributes, instead of the tree. Takes a span= id from the tree or the failure list"`
}

// failure is one span that errored, lifted out of the tree so that what went
// wrong is read before the tree rather than found inside it.
type failure struct {
	Span     string        `json:"span"`
	Service  string        `json:"service"`
	Name     string        `json:"name"`
	At       time.Duration `json:"-"`
	AtMillis int64         `json:"at_ms" jsonschema:"Milliseconds from the start of the trace"`
	Duration time.Duration `json:"-"`
	Millis   int64         `json:"duration_ms"`
	Message  string        `json:"message,omitempty" jsonschema:"What the span said about the failure"`
}

type traceOutput struct {
	TraceID  string        `json:"trace_id"`
	Root     string        `json:"root,omitempty" jsonschema:"The service and operation the request entered by"`
	Duration time.Duration `json:"-"`
	Millis   int64         `json:"duration_ms"`
	Spans    int           `json:"spans"`
	Services int           `json:"services"`
	Failures int           `json:"failures"`
	// Detached is how many spans named a parent that never arrived. Any at all
	// means the trace is partial, which has to be said before a latency is read
	// off it.
	Detached int `json:"detached,omitempty" jsonschema:"Spans whose parent is missing, so the trace is partial"`
	// Drawn, Folded and Dropped account for every span between them: a row can
	// stand for several identical siblings, so the row count is not the span
	// count, and what was cut has to be reachable from what was not.
	Drawn  int `json:"drawn" jsonschema:"Spans drawn in the tree, one per row before folding"`
	Folded int `json:"folded,omitempty" jsonschema:"Spans folded into a row with an identical sibling"`
	// Dropped is what the depth cut left out, which is never a failure and
	// never an ancestor of one.
	Dropped int       `json:"dropped,omitempty" jsonschema:"Spans below the depth drawn"`
	Failed  []failure `json:"failed,omitempty"`
	Note    string    `json:"note,omitempty" jsonschema:"What was cut, and what the answer leaves out"`
}

func addTraces(s *sdk.Server, cfg config.Config) {
	addTool(s, "trace", traceDescription, traceHandler(cfg))
}

func traceHandler(cfg config.Config) sdk.ToolHandlerFor[traceInput, traceOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in traceInput) (*sdk.CallToolResult, traceOutput, error) {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, traceOutput{}, errors.New(
				"name a trace id: it is what a log line carries, under trace_id or traceID")
		}
		at, err := traceStore(cfg, in.Place)
		if err != nil {
			return nil, traceOutput{}, err
		}

		ctx, cancel := context.WithTimeout(ctx, traceFetchTimeout)
		defer cancel()

		data, err := at.Trace(ctx, id)
		if err != nil {
			return nil, traceOutput{}, err
		}
		tree, err := trace.Decode(data)
		if err != nil {
			return nil, traceOutput{}, err
		}
		if tree == nil || tree.Len() == 0 {
			return nil, traceOutput{}, errors.Errorf(
				"no trace %s at %s: it may have aged out, or never been sampled", id, at.URL)
		}
		moved := tree.ClampSkew()

		if span := strings.TrimSpace(in.Span); span != "" {
			return drawSpan(tree, span)
		}

		out, rows := describeTrace(tree, moved)
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: drawTrace(out, rows)}},
		}, out, nil
	}
}

// describeTrace walks the tree into rows and the facts about them.
func describeTrace(t *trace.Tree, moved int) (traceOutput, []spanRow) {
	out := traceOutput{
		TraceID:  t.ID,
		Duration: t.Duration(),
		Spans:    t.Len(),
		Services: len(t.Services()),
		Detached: t.Detached,
	}
	out.Millis = out.Duration.Round(time.Millisecond).Milliseconds()
	if len(t.Roots) > 0 {
		r := t.Roots[0].Span
		out.Root = strings.TrimSpace(r.Service + " " + r.Name)
	}

	keep := depthToDraw(t)
	rows := walkRows(t, keep, &out)

	t.Walk(func(n *trace.Node) bool {
		if n.Span.Failed() {
			out.Failures++
			out.Failed = append(out.Failed, failure{
				Span:     ref(n.Span.SpanID),
				Service:  n.Span.Service,
				Name:     n.Span.Name,
				At:       offsetIn(t, n),
				AtMillis: offsetIn(t, n).Round(time.Millisecond).Milliseconds(),
				Duration: n.Span.Duration,
				Millis:   n.Span.Duration.Round(time.Millisecond).Milliseconds(),
				Message:  n.Span.StatusMessage,
			})
		}
		return true
	})

	for _, r := range rows {
		if r.node != nil {
			out.Drawn++
		}
	}
	out.Note = traceNote(out, keep, moved)
	return out, rows
}

// depthToDraw is the deepest level still drawn, which is as deep as fits. It is found by trying rather than computed: folding means a level
// costs fewer rows than it holds spans, so how much a depth costs is not known
// until the folding at that depth has been done.
func depthToDraw(t *trace.Tree) int {
	deepest := 0
	t.Walk(func(n *trace.Node) bool {
		deepest = max(deepest, n.Depth)
		return true
	})
	for d := deepest; d > 0; d-- {
		var probe traceOutput
		if len(walkRows(t, d, &probe)) <= traceRows {
			return d
		}
	}
	return 0
}

// walkRows draws the tree down to keep, folding identical siblings and cutting
// what is deeper.
//
// A failed span is never cut, nor is anything above it: failures are rare, so
// the exemption costs almost nothing, and without it the depth cut could hide
// the one thing the trace was opened to find. What is cut is stubbed at its
// parent rather than dropped silently, and a stub says whether a failure went
// with it — [trace.Node.FailedBelow] exists for exactly this.
func walkRows(t *trace.Tree, keep int, out *traceOutput) []spanRow {
	var rows []spanRow
	var visit func(nodes []*trace.Node, depth int)
	visit = func(nodes []*trace.Node, depth int) {
		past := depth > keep
		var (
			quiet int
			under string
		)
		for _, group := range foldSiblings(nodes) {
			n := group.node
			// Past the cut only the way to a failure is followed. Following
			// everything under a node that happens to hold one would let a
			// single deep failure drag a thousand of its siblings' rows in
			// with it, which is the budget gone on the spans nobody asked
			// about.
			if past && !n.Span.Failed() && !n.FailedBelow() {
				quiet += group.n + countBelow(n)
				if under == "" && n.Parent != nil {
					under = n.Parent.Span.SpanID
				}
				continue
			}
			row := spanRow{
				node:    n,
				depth:   depth,
				at:      offsetIn(t, n),
				n:       group.n,
				slowest: group.slowest,
				total:   group.total,
			}
			if group.n > 1 {
				out.Folded += group.n - 1
			}
			rows = append(rows, row)
			visit(n.Children, depth+1)
		}
		if quiet > 0 {
			out.Dropped += quiet
			rows = append(rows, spanRow{depth: depth, quiet: quiet, under: under})
		}
	}
	visit(t.Roots, 0)
	return rows
}

// sibling is a run of identical spans under one parent, folded into one row.
// Fourteen calls to the same statement in a loop are one fact about the code
// and fourteen rows about the trace, and the fact is the part worth paying for.
type sibling struct {
	node    *trace.Node
	n       int
	slowest time.Duration
	total   time.Duration
}

// foldSiblings groups children that say the same thing. They are grouped by
// what they are and not by where they sit, since a loop's calls interleave with
// whatever else the parent did and are still the same call.
func foldSiblings(nodes []*trace.Node) []sibling {
	var (
		out   []sibling
		where = map[string]int{}
	)
	for _, n := range nodes {
		// Anything with children of its own is not folded: the fold would take
		// its subtree with it, and a row standing for fourteen subtrees is a
		// claim nobody can check.
		if len(n.Children) > 0 || n.Span.Failed() {
			out = append(out, sibling{node: n, n: 1, slowest: n.Span.Duration, total: n.Span.Duration})
			continue
		}
		key := n.Span.Service + "\x00" + n.Span.Name
		i, ok := where[key]
		if !ok {
			where[key] = len(out)
			out = append(out, sibling{node: n, n: 1, slowest: n.Span.Duration, total: n.Span.Duration})
			continue
		}
		out[i].n++
		out[i].total += n.Span.Duration
		out[i].slowest = max(out[i].slowest, n.Span.Duration)
	}
	return out
}

func countBelow(n *trace.Node) int {
	total := 0
	for _, c := range n.Children {
		total += 1 + countBelow(c)
	}
	return total
}

// offsetIn is where a span sits inside the trace, measured off the repaired
// start so it agrees with the nesting drawn around it.
func offsetIn(t *trace.Tree, n *trace.Node) time.Duration {
	return n.Span.Start.Sub(t.Start)
}

func traceNote(out traceOutput, keep, moved int) string {
	var note string
	if out.Dropped > 0 {
		note = join(note, strconv.Itoa(out.Dropped)+" of "+strconv.Itoa(out.Spans)+
			" spans sit below depth "+strconv.Itoa(keep)+
			" and were not drawn. Each row saying so names the span "+
			"they sit under: ask again with that span= to read one whole. Nothing that "+
			"failed was cut, nor anything above it")
	}
	if out.Folded > 0 {
		note = join(note, strconv.Itoa(out.Folded)+
			" spans were folded into a sibling saying the same thing, marked ×n")
	}
	if moved > 0 {
		note = join(note, plural(moved, "subtree")+
			" began before the span that called it and was moved to sit inside it, "+
			"marked on the row. Clocks on different machines disagree, and the offsets "+
			"here are the repaired ones")
	}
	if out.Detached > 0 {
		note = join(note, "the trace is partial: "+plural(out.Detached, "span")+
			" named a parent that never arrived, so it is drawn as a root and the "+
			"total duration may be short")
	}
	return note
}
