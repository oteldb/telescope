package ui

import (
	stdhex "encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/oteldb/telescope/internal/source"
)

const jumpTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

// tempo is a trace store that counts what it was asked, which is the whole of
// what a cache can be checked against: the screen looks the same either way.
type tempo struct {
	*httptest.Server
	asked atomic.Int64
}

// tempoServer answers a trace fetch the way Tempo's v2 path does.
func tempoServer(t *testing.T) *tempo {
	t.Helper()
	id := func(s string) []byte {
		raw, err := stdhex.DecodeString(s)
		require.NoError(t, err)
		return raw
	}
	span := func(sid, pid, name string, start, end uint64) *tracepb.Span {
		s := &tracepb.Span{
			TraceId:           id(jumpTraceID),
			SpanId:            id(sid),
			Name:              name,
			StartTimeUnixNano: start,
			EndTimeUnixNano:   end,
			Attributes: []*commonpb.KeyValue{{
				Key:   "http.route",
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "/checkout"}},
			}},
		}
		if pid != "" {
			s.ParentSpanId = id(pid)
		}
		return s
	}
	res := func(service string, spans ...*tracepb.Span) *tracepb.ResourceSpans {
		return &tracepb.ResourceSpans{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
				Key:   "service.name",
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}},
			}}},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
		}
	}
	const base = 1786694400_000000000
	payload, err := protojson.Marshal(&tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{
		res("gateway", span("00f067aa0ba902b7", "", "POST /checkout", base, base+480e6)),
		res("orders-db", span("d4e5f60718293041", "00f067aa0ba902b7", "INSERT orders", base+130e6, base+340e6)),
	}})
	require.NoError(t, err)

	store := &tempo{}
	store.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, jumpTraceID) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		store.asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trace":` + string(payload) + `}`))
	}))
	t.Cleanup(store.Close)
	return store
}

// tracingLogs is a log view whose place reads traces from url.
func tracingLogs(t *testing.T, url string, lines ...string) tea.Model {
	t.Helper()
	cfg := source.Config{
		Collector: source.CollectorDocker,
		Container: "app",
		Follow:    true,
		Traces:    source.Endpoint{URL: url},
	}
	m := send(t, New(), size(), connectMsg{cfg: cfg})

	batch := make([]source.Line, 0, len(lines))
	for _, l := range lines {
		batch = append(batch, source.Line{Data: []byte(l)})
	}
	return send(t, m, linesMsg{lines: batch, closed: true})
}

const tracedLine = `{"level":"error","ts":"2026-08-13T10:00:00Z","msg":"checkout failed","trace_id":"` +
	jumpTraceID + `"}`

// The jump is the point of the whole thing: a line says which request it was
// written inside, and that request is a keystroke away.
func TestALineOpensTheTraceItWasWrittenIn(t *testing.T) {
	srv := tempoServer(t)
	m := tracingLogs(t, srv.URL, tracedLine)

	m = send(t, m, k("T"))
	require.Equal(t, stateTrace, m.(Model).state)
	require.Contains(t, screen(t, m), "fetching trace", "and says so while it is being fetched")

	m = send(t, m, fetchTrace(source.Endpoint{URL: srv.URL}, jumpTraceID)())
	out := screen(t, m)
	require.Contains(t, out, "gateway POST /checkout")
	require.Contains(t, out, "orders-db INSERT orders")
	require.Contains(t, out, jumpTraceID)
}

// Opened over a list, the way back is the list. Only a trace telescope was
// started on has nothing underneath it.
func TestLeavingATraceOpenedOverALogReturnsToIt(t *testing.T) {
	srv := tempoServer(t)
	m := tracingLogs(t, srv.URL, tracedLine)
	m = send(t, m, k("T"))
	m = send(t, m, fetchTrace(source.Endpoint{URL: srv.URL}, jumpTraceID)())

	m = send(t, m, k("esc"))
	require.Equal(t, stateLogs, m.(Model).state)
	require.Contains(t, screen(t, m), "checkout failed")
}

func TestALineOutsideATraceSaysSoRatherThanOpeningNothing(t *testing.T) {
	srv := tempoServer(t)
	m := tracingLogs(t, srv.URL, `{"level":"info","ts":"2026-08-13T10:00:00Z","msg":"no trace here"}`)

	m = send(t, m, k("T"))
	require.Equal(t, stateLogs, m.(Model).state)
	require.Contains(t, screen(t, m), "not in a trace")
}

// A place that reads logs need not read traces, and that is a thing to say
// where the reader is rather than a screen to open with nothing on it.
func TestAPlaceWithNoTraceStoreSaysWhatIsMissing(t *testing.T) {
	m := logsModel(t, tracedLine)
	m = send(t, m, k("T"))

	require.Equal(t, stateLogs, m.(Model).state)
	require.Contains(t, screen(t, m), "traces:")
}

// A fetch that fails has to land somewhere the reader can read it.
func TestATraceThatCouldNotBeReadSaysWhy(t *testing.T) {
	srv := tempoServer(t)
	m := tracingLogs(t, srv.URL, tracedLine)
	m = send(t, m, k("T"))
	m = send(t, m, fetchTrace(source.Endpoint{URL: srv.URL}, "0000000000000000000000000000dead")())

	out := screen(t, m)
	require.Contains(t, out, "could not read that trace")
	require.Contains(t, out, "404")
}

// A merge is several systems, and a trace id read off one of them means
// nothing to a sibling's trace store.
func TestAMergeAsksTheStoreOfThePlaceTheLineCameFrom(t *testing.T) {
	cfg := source.Config{
		Collector: source.CollectorMerge,
		Merge: []source.Config{
			{
				Name: "eu", Collector: source.CollectorDocker, Container: "a",
				Traces: source.Endpoint{URL: "https://tempo.eu.example.com"},
			},
			{
				Name: "us", Collector: source.CollectorDocker, Container: "b",
				Traces: source.Endpoint{URL: "https://tempo.us.example.com"},
			},
		},
	}
	eu, ok := cfg.TraceEndpoint("eu")
	require.True(t, ok)
	require.Equal(t, "https://tempo.eu.example.com", eu.URL)

	us, ok := cfg.TraceEndpoint("us")
	require.True(t, ok)
	require.Equal(t, "https://tempo.us.example.com", us.URL)

	_, ok = cfg.TraceEndpoint("unknown")
	require.False(t, ok, "and a place that reads none reads none")
}

// pressRoot delivers a key and applies the message its command produced, which is
// how a view hands work back to the root model. Unlike send it does not care
// which key it was, and unlike send it runs exactly one command — so it is for
// keys whose command is a message and not a request over the network.
func pressRoot(t *testing.T, m tea.Model, key string) tea.Model {
	t.Helper()
	m, cmd := m.Update(k(key))
	if cmd == nil {
		return m
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	return m
}

// onto moves the span cursor to the row with this key.
func onto(t *testing.T, m tea.Model, key string) tea.Model {
	t.Helper()
	for range 40 {
		tm := m.(Model).trace
		doc := tm.spanDoc()
		sel := picks(doc)
		require.NotEmpty(t, sel)
		if doc[sel[tm.clampSel(sel)]].key == key {
			return m
		}
		m, _ = m.Update(k("j"))
	}
	t.Fatalf("no row %q on this span", key)
	return m
}

// jumped is a log list opened on the trace of its newest line, which is where
// the cursor sits while the list is following.
func jumped(t *testing.T, lines ...string) tea.Model {
	t.Helper()
	srv := tempoServer(t)
	m := tracingLogs(t, srv.URL, lines...)
	m = send(t, m, k("T"))
	require.Equal(t, stateTrace, m.(Model).state)
	return send(t, m, fetchTrace(source.Endpoint{URL: srv.URL}, jumpTraceID)())
}

// The reverse of the jump: having found the request, ask for everything written
// anywhere inside it.
func TestATraceNarrowsTheLogBackToIt(t *testing.T) {
	other := `{"level":"info","ts":"2026-08-13T10:00:00Z","msg":"unrelated work"}`
	m := jumped(t, other, tracedLine)

	m = pressRoot(t, m, "f")
	require.Equal(t, stateLogs, m.(Model).state, "narrowing lands on the list")

	out := screen(t, m)
	require.Contains(t, out, "trace_id="+jumpTraceID, "and the list says what it is filtered by")
	require.Contains(t, out, "checkout failed")
	require.NotContains(t, out, "unrelated work")
}

// A row of the span is a filter the same way a row of an entry is.
func TestASpanRowNarrowsTheLogByWhateverItHolds(t *testing.T) {
	m := jumped(t, tracedLine)
	m, _ = m.Update(k("enter"))
	m = onto(t, m, "http.route")

	m = pressRoot(t, m, "f")
	require.Equal(t, stateLogs, m.(Model).state)
	require.Contains(t, screen(t, m), `http.route="/checkout"`)
}

// A duration is not something a line carries, and narrowing by one would ask
// the list for something that was never going to be there.
func TestASpanRowThatIsNotALabelDoesNotNarrow(t *testing.T) {
	m := jumped(t, tracedLine)
	m, _ = m.Update(k("enter"))
	m = onto(t, m, "duration")

	m = pressRoot(t, m, "f")
	require.Equal(t, stateTrace, m.(Model).state, "and stays where it was")
	require.Contains(t, screen(t, m), "nothing to narrow")
}

// A trace telescope was started on has no list under it, and dropping into an
// empty one would be worse than saying so.
func TestATraceOpenedOnItsOwnHasNoLogToNarrow(t *testing.T) {
	m := traceModelOf(t, checkout())
	m = pressRoot(t, m, "f")

	require.Equal(t, stateTrace, m.(Model).state)
	require.Contains(t, screen(t, m), "no logs here")
}

// The whole round trip: a line names its request, the request is read, and what
// it did comes back as the lines that did it.
func TestTheRoundTripEndsWhereItStarted(t *testing.T) {
	m := jumped(t, tracedLine)

	m = pressRoot(t, m, "f")
	require.Equal(t, stateLogs, m.(Model).state)

	// And out of the narrowed list into the same trace again.
	m = send(t, m, k("T"))
	require.Equal(t, stateTrace, m.(Model).state)
}

// TestAPlaceWithNoTraceStoreSaysSoWhereTheKeyWasPressed: T is on the list and
// on an entry both, and a refusal drawn on the screen the reader is not looking
// at is a key that did nothing.
func TestAPlaceWithNoTraceStoreSaysSoWhereTheKeyWasPressed(t *testing.T) {
	const missing = "no trace store here"

	// A place that reads logs and was never told where its traces are.
	cfg := source.Config{Collector: source.CollectorDocker, Container: "app", Follow: true}
	open := func(t *testing.T) tea.Model {
		t.Helper()
		m := send(t, New(), size(), connectMsg{cfg: cfg})
		return send(t, m, linesMsg{lines: []source.Line{{Data: []byte(tracedLine)}}, closed: true})
	}

	t.Run("from the list", func(t *testing.T) {
		m := pressRoot(t, open(t), "T")
		require.Contains(t, ansi.Strip(m.View()), missing)
	})

	t.Run("from an entry", func(t *testing.T) {
		m := pressRoot(t, open(t), "enter")
		require.Contains(t, ansi.Strip(m.View()), "checkout failed", "the entry is open")
		m = pressRoot(t, m, "T")
		require.Contains(t, ansi.Strip(m.View()), missing)
	})
}
