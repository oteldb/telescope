package ui

import (
	stdhex "encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/oteldb/telescope/internal/source"
)

const jumpTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

// tempoServer answers a trace fetch the way Tempo's v2 path does.
func tempoServer(t *testing.T) *httptest.Server {
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, jumpTraceID) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trace":` + string(payload) + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv
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
