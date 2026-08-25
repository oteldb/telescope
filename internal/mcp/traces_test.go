package mcp

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/trace"
)

// traceStart is when the fixture traces begin. A fixed instant so the offsets a
// row prints are the same on every run.
var traceStart = time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)

// span builds one fixture span, at an offset from the start of the trace.
func fixture(id, parent, service, name string, at, dur time.Duration) trace.Span {
	return trace.Span{
		TraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:   id,
		ParentID: parent,
		Service:  service,
		Name:     name,
		Start:    traceStart.Add(at),
		Duration: dur,
	}
}

func failed(s trace.Span, msg string) trace.Span {
	s.Status, s.StatusMessage = trace.StatusError, msg
	return s
}

const ms = time.Millisecond

// deepChain is a call chain n levels deep, each level properly inside the one
// above it so nothing has to be moved for the clocks. failAt names the level
// that errored, or -1 for a chain that worked.
func deepChain(n, failAt int) []trace.Span {
	spans := []trace.Span{fixture("root", "", "svc0", "op0", 0, time.Duration(n+2)*ms)}
	parent := "root"
	for i := 1; i <= n; i++ {
		id := "n" + strconv.Itoa(i)
		s := fixture(id, parent, "svc"+strconv.Itoa(i%4), "op"+strconv.Itoa(i),
			time.Duration(i)*ms, time.Duration(n+2-i)*ms)
		if i == failAt {
			s = failed(s, "deep and broken")
		}
		spans = append(spans, s)
		parent = id
	}
	return spans
}

func drawn(t *testing.T, spans []trace.Span) (string, traceOutput) {
	t.Helper()
	tree := trace.Build("4bf92f3577b34da6a3ce929d0e0e4736", spans)
	moved := tree.ClampSkew()
	out, rows := describeTrace(tree, moved)
	return drawTrace(out, rows), out
}

// TestATraceReadsAsItsShapeAndItsTiming: the tree is what a span called and the
// offsets are when, which together say whether two siblings ran at once or one
// waited for the other.
func TestATraceReadsAsItsShapeAndItsTiming(t *testing.T) {
	text, out := drawn(t, []trace.Span{
		fixture("a1", "", "checkout", "POST /api/orders", 0, 1240*ms),
		fixture("b1", "a1", "auth", "GET /verify", 2*ms, 12*ms),
		fixture("c1", "a1", "pricing", "POST /quote", 48*ms, 890*ms),
		fixture("d1", "c1", "fx", "GET /rates", 490*ms, 440*ms),
	})

	require.Equal(t, 4, out.Spans)
	require.Equal(t, 4, out.Services)
	require.Equal(t, "checkout POST /api/orders", out.Root)
	require.Equal(t, int64(1240), out.Millis)

	require.Contains(t, text, "trace 4bf92f3577b34da6a3ce929d0e0e4736 — checkout POST /api/orders")
	require.Contains(t, text, "1.24s · 4 spans · 4 services")
	require.Contains(t, text, "   490    440ms        fx  GET /rates",
		"nested under the span that called it, at the offset it began")
	require.NotContains(t, text, "moved", "nothing had to be moved for the clocks")
	require.NotContains(t, text, "failed:", "nothing errored, so nothing is listed")
}

// TestIdenticalSiblingsFoldIntoOneRow: fourteen calls to the same statement are
// one fact about the code and fourteen rows about the trace, and only the fact
// is worth what it costs to read.
func TestIdenticalSiblingsFoldIntoOneRow(t *testing.T) {
	spans := []trace.Span{
		fixture("a1", "", "checkout", "POST /api/orders", 0, 900*ms),
		fixture("c1", "a1", "pricing", "POST /quote", 10*ms, 800*ms),
	}
	for i := range 14 {
		at := time.Duration(20+i*10) * ms
		spans = append(spans, fixture("d"+string(rune('a'+i)), "c1", "pricing", "SELECT rules", at, time.Duration(10+i)*ms))
	}

	text, out := drawn(t, spans)
	require.Equal(t, 16, out.Spans, "the count is of spans and not of rows")
	require.Equal(t, 13, out.Folded)
	require.Equal(t, 3, out.Drawn)
	require.Contains(t, text, "SELECT rules  ×14 (23ms slowest)")
	require.Equal(t, 1, strings.Count(text, "SELECT rules"))
	require.Contains(t, out.Note, "folded into a sibling")
}

// TestAFailedSpanIsSaidBeforeTheTree: an agent that has to read two hundred
// rows to find the one that errored has paid for the whole trace to learn one
// line of it.
func TestAFailedSpanIsSaidBeforeTheTree(t *testing.T) {
	text, out := drawn(t, []trace.Span{
		fixture("a1", "", "checkout", "POST /api/orders", 0, 1240*ms),
		fixture("b1", "a1", "auth", "GET /verify", 2*ms, 12*ms),
		failed(fixture("e1", "a1", "payments", "POST /charge", 955*ms, 280*ms), "upstream timeout"),
	})

	require.Equal(t, 1, out.Failures)
	require.Equal(t, "payments", out.Failed[0].Service)
	require.Equal(t, "upstream timeout", out.Failed[0].Message)
	require.Equal(t, int64(955), out.Failed[0].AtMillis)

	failedAt := strings.Index(text, "failed:")
	treeAt := strings.Index(text, "    at      dur  span")
	require.Positive(t, failedAt)
	require.Greater(t, treeAt, failedAt, "what went wrong is read before the tree, not found inside it")

	require.Contains(t, text, "payments  POST /charge     955    280ms  upstream timeout  span=e1")
	require.Contains(t, text, "!   payments  POST /charge",
		"and marked on its row too, in a column that lines up however deep the row sits")
}

// TestADeepTraceIsCutByDepthAndSaysSo: a reader that cannot scroll has no other
// way to tell a small trace from a trimmed one.
func TestADeepTraceIsCutByDepthAndSaysSo(t *testing.T) {
	// A chain far deeper than the row budget, each level branching so the cut
	// has something to leave behind.
	spans := deepChain(300, -1)

	text, out := drawn(t, spans)
	require.Equal(t, 301, out.Spans)
	require.LessOrEqual(t, out.Drawn, traceRows)
	require.Positive(t, out.Dropped)
	require.Equal(t, out.Spans, out.Drawn+out.Dropped+out.Folded)

	require.Contains(t, text, "not drawn, none failed  under span=")
	require.Contains(t, out.Note, "were not drawn")
	require.Contains(t, out.Note, "Nothing that failed was cut")
}

// TestTheDepthCutNeverTakesAFailureWithIt: the depth a trace is cut at is a
// budget, and the one thing it may not spend is the span somebody opened the
// trace to find.
func TestTheDepthCutNeverTakesAFailureWithIt(t *testing.T) {
	spans := deepChain(300, 280)

	text, out := drawn(t, spans)
	require.Equal(t, 1, out.Failures)
	require.Contains(t, text, "deep and broken", "listed above the tree however deep it sat")
	require.Contains(t, text, "op280", "and drawn, though its depth was past the cut")
	require.Contains(t, text, "@280 ", "at its real depth, since the indent stops growing before then")
}

// TestASpanIsReadWithItsAttributes: the tree leaves attributes out on purpose,
// so the span somebody picked is where they are paid for.
func TestASpanIsReadWithItsAttributes(t *testing.T) {
	s := fixture("d1", "a1", "pricing", "SELECT rules", 61*ms, 412*ms)
	s.Attrs = []logs.Field{
		{Key: "db.system", Value: []byte(`"postgresql"`)},
		{Key: "db.statement", Value: []byte(`"SELECT * FROM rules WHERE sku = $1"`)},
	}
	tree := trace.Build("4bf92f3577b34da6a3ce929d0e0e4736", []trace.Span{
		fixture("a1", "", "checkout", "POST /api/orders", 0, time.Second),
		s,
	})

	res, out, err := drawSpan(tree, "d1")
	require.NoError(t, err)
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", out.TraceID)

	text := ansi.Strip(res.Content[0].(*sdk.TextContent).Text)
	require.Contains(t, text, "span d1 in trace 4bf92f3577b34da6a3ce929d0e0e4736")
	require.Contains(t, text, "pricing SELECT rules · at 61 · 412ms")
	require.Contains(t, text, "db.statement  SELECT * FROM rules WHERE sku = $1")
	require.Contains(t, text, "db.system     postgresql")
	require.Contains(t, text, "called by a1")
}

// TestASpanThatIsNotThereSaysWhereTheIdsCameFrom: a wrong id is most often a
// near miss on one the tree printed.
func TestASpanThatIsNotThereSaysWhereTheIdsCameFrom(t *testing.T) {
	tree := trace.Build("t1", []trace.Span{fixture("a1", "", "checkout", "POST /orders", 0, time.Second)})
	_, _, err := drawSpan(tree, "nope")
	require.ErrorContains(t, err, "no span nope")
	require.ErrorContains(t, err, "span= on a row of the tree")
}
