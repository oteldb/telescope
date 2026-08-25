package mcp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

// asking records what a store was sent, so a test can read the query that
// reached it rather than the one it was meant to be.
type asking struct {
	mu     sync.Mutex
	params url.Values
	path   string
}

func (a *asking) got() (string, url.Values) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.path, a.params
}

// searchServer stands in for a store, answering the body it is given.
func searchServer(t *testing.T, body string) (*httptest.Server, *asking) {
	t.Helper()
	got := &asking{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.mu.Lock()
		got.path, got.params = r.URL.Path, r.URL.Query()
		got.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func callSearch(t *testing.T, cfg config.Config, in searchInput) (string, searchOutput, error) {
	t.Helper()
	res, out, err := searchHandler(cfg)(t.Context(), nil, in)
	if err != nil {
		return "", searchOutput{}, err
	}
	return res.Content[0].(*sdk.TextContent).Text, out, nil
}

// tempoFound is what Tempo answers a search with: a summary of each trace, and
// how many of its spans the query selected.
const tempoFound = `{"traces":[
	{"traceID":"4bf92f3577b34da6a3ce929d0e0e4736","rootServiceName":"checkout",
	 "rootTraceName":"POST /api/orders","startTimeUnixNano":"1786528800000000000",
	 "durationMs":1240,"spanSet":{"matched":3}},
	{"traceID":"a1b2c3d4e5f60718293a4b5c6d7e8f90","rootServiceName":"checkout",
	 "rootTraceName":"POST /api/orders","startTimeUnixNano":"1786528740000000000",
	 "durationMs":310,"spanSet":{"matched":1}}]}`

// jaegerFound is what Jaeger answers with: the traces themselves, so how many
// spans each holds and how many failed is known.
const jaegerFound = `{"data":[{"traceID":"4bf92f3577b34da6a3ce929d0e0e4736",
	"processes":{"p1":{"serviceName":"checkout"},"p2":{"serviceName":"payments"}},
	"spans":[
	 {"traceID":"4bf92f3577b34da6a3ce929d0e0e4736","spanID":"a1","operationName":"POST /api/orders",
	  "startTime":1786528800000000,"duration":1240000,"processID":"p1"},
	 {"traceID":"4bf92f3577b34da6a3ce929d0e0e4736","spanID":"e1","operationName":"POST /charge",
	  "references":[{"refType":"CHILD_OF","spanID":"a1"}],
	  "startTime":1786528800955000,"duration":280000,"processID":"p2",
	  "tags":[{"key":"error","type":"bool","value":true}]}]}]}`

// TestASearchNamesEachTraceForTheTraceTool: a search answers what to read next,
// so the id has to come back whole even though the tree shortens it.
func TestASearchNamesEachTraceForTheTraceTool(t *testing.T) {
	srv, got := searchServer(t, tempoFound)
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: srv.URL},
	}, nil)

	text, out, err := callSearch(t, cfg, searchInput{Service: "checkout"})
	require.NoError(t, err)
	require.Equal(t, 2, out.Returned)
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", out.Traces[0].TraceID)
	require.Contains(t, text, "4bf92f3577b34da6a3ce929d0e0e4736",
		"whole, since it is what the trace tool is given")
	require.Contains(t, text, "checkout POST /api/orders")

	_, params := got.got()
	require.Contains(t, params.Get("q"), `resource.service.name="checkout"`,
		"compiled into TraceQL rather than filtered here")
	require.Contains(t, text, "searched "+srv.URL+": ")
}

// TestTheTwoBackendsCountsAreNeverDrawnAsOne: "38 spans" and "3 matched" are
// different claims about the same trace, and a column that meant whichever the
// store happened to report would be a number nobody can read.
func TestTheTwoBackendsCountsAreNeverDrawnAsOne(t *testing.T) {
	tempo, _ := searchServer(t, tempoFound)
	jaeger, _ := searchServer(t, jaegerFound)
	cfg := testConfig(t, []config.Place{
		{Name: "tempo store", Type: "tempo", URL: tempo.URL},
		{Name: "jaeger store", Type: "jaeger", URL: jaeger.URL},
	}, nil)

	fromTempo, tempoOut, err := callSearch(t, cfg, searchInput{
		Place: "tempo store", Service: "checkout",
	})
	require.NoError(t, err)
	require.Equal(t, 3, tempoOut.Traces[0].Matched)
	require.Zero(t, tempoOut.Traces[0].Spans, "tempo answered a summary and cannot know")
	require.Contains(t, fromTempo, "matched")
	require.NotContains(t, fromTempo, "spans",
		"a spans column here would read as a trace with no spans in it")

	fromJaeger, jaegerOut, err := callSearch(t, cfg, searchInput{
		Place: "jaeger store", Service: "checkout",
	})
	require.NoError(t, err)
	require.Equal(t, 2, jaegerOut.Traces[0].Spans)
	require.Equal(t, 1, jaegerOut.Traces[0].Errors)
	require.Zero(t, jaegerOut.Traces[0].Matched, "jaeger has no notion of it")
	require.Contains(t, fromJaeger, "2 spans, 1 failed")
	require.NotContains(t, fromJaeger, "matched",
		"a matched column here would read as a query that selected nothing")
}

// TestASearchSaysWhenTheWindowHoldsMore: a reader that cannot scroll has no
// other way to tell a quiet window from a full one.
func TestASearchSaysWhenTheWindowHoldsMore(t *testing.T) {
	srv, _ := searchServer(t, tempoFound)
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: srv.URL},
	}, nil)

	text, out, err := callSearch(t, cfg, searchInput{Service: "checkout", Limit: 2})
	require.NoError(t, err)
	require.Equal(t, 2, out.Returned)
	require.Contains(t, out.Note, "the whole of the limit")
	require.Contains(t, text, "note: this is the whole of the limit")
}

// TestAnEmptySearchSaysWhatThatMeans: nothing matching is the usual answer at a
// sampled store, and an empty list on its own reads as a broken query.
func TestAnEmptySearchSaysWhatThatMeans(t *testing.T) {
	srv, _ := searchServer(t, `{"traces":[]}`)
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: srv.URL},
	}, nil)

	_, out, err := callSearch(t, cfg, searchInput{Service: "checkout"})
	require.NoError(t, err)
	require.Zero(t, out.Returned)
	require.Contains(t, out.Note, "nothing matched in the window")
	require.Contains(t, out.Note, "sampled")
}

// TestASearchWindowIsReportedAsTwoInstants: what was typed is relative and the
// answer is about a fixed interval, so the instants are what makes one answer
// comparable to the next.
func TestASearchWindowIsReportedAsTwoInstants(t *testing.T) {
	srv, got := searchServer(t, tempoFound)
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: srv.URL},
	}, nil)

	text, out, err := callSearch(t, cfg, searchInput{
		Service: "checkout", Range: "2026-01-02 10:00..2026-01-02 12:00",
	})
	require.NoError(t, err)
	start, end, ok := strings.Cut(out.Window, "..")
	require.True(t, ok, "two instants, not a spec")
	require.Contains(t, start, "2026-01-02T10:00:00")
	require.Contains(t, end, "2026-01-02T12:00:00")
	require.Contains(t, text, out.Window)

	_, params := got.got()
	require.NotEmpty(t, params.Get("start"), "and the store was asked for it")
	require.NotEmpty(t, params.Get("end"))
}

// TestAJaegerStoreSaysItNeedsAServiceBeforeTheRoundTrip: its own complaint
// arrives as a 400 with a parameter name in it and nothing about what to do.
func TestAJaegerStoreSaysItNeedsAServiceBeforeTheRoundTrip(t *testing.T) {
	srv, got := searchServer(t, jaegerFound)
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "jaeger", URL: srv.URL},
	}, nil)

	_, _, err := callSearch(t, cfg, searchInput{})
	require.ErrorContains(t, err, "searches by service: name one")

	path, _ := got.got()
	require.Empty(t, path, "and nothing was sent")
}

// TestATagFilterReachesTheStore: the tags are typed as one field and compiled,
// not matched here.
func TestATagFilterReachesTheStore(t *testing.T) {
	srv, got := searchServer(t, tempoFound)
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: srv.URL},
	}, nil)

	_, _, err := callSearch(t, cfg, searchInput{
		Service: "checkout", Tags: "http.status_code=500 error=true", MinDuration: "500ms",
	})
	require.NoError(t, err)

	_, params := got.got()
	q := params.Get("q")
	require.Contains(t, q, "http.status_code")
	require.Contains(t, q, "500")
	require.Contains(t, q, "traceDuration>500ms")
}

// TestABadDurationSaysWhichOne: two of them can be given and the complaint has
// to name the one that was wrong.
func TestABadDurationSaysWhichOne(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: "https://tempo.example.com"},
	}, nil)

	_, _, err := callSearch(t, cfg, searchInput{MaxDuration: "ages"})
	require.ErrorContains(t, err, "max_duration")
}

// TestAJaegerStoreIsNotToldItWasAskedTraceQL: it takes named parameters and
// compiles nothing, so reporting a TraceQL string would say the store was sent
// something it has never seen — and what was asked is reported precisely so a
// reader can tell what the store did from what was filtered after it.
func TestAJaegerStoreIsNotToldItWasAskedTraceQL(t *testing.T) {
	jaeger, _ := searchServer(t, jaegerFound)
	tempo, _ := searchServer(t, tempoFound)
	cfg := testConfig(t, []config.Place{
		{Name: "jaeger store", Type: "jaeger", URL: jaeger.URL},
		{Name: "tempo store", Type: "tempo", URL: tempo.URL},
	}, nil)

	in := searchInput{Service: "checkout", Tags: "error=true", MinDuration: "500ms"}

	in.Place = "jaeger store"
	_, out, err := callSearch(t, cfg, in)
	require.NoError(t, err)
	require.Equal(t, `service=checkout tags={"error":"true"} minDuration=500ms`, out.Asked)
	require.NotContains(t, out.Asked, "resource.service.name")

	in.Place = "tempo store"
	_, out, err = callSearch(t, cfg, in)
	require.NoError(t, err)
	require.Contains(t, out.Asked, `resource.service.name="checkout"`)
}

// TestALoweredLimitIsSaidOutLoud: a caller comparing what it asked for against
// what it got would otherwise read the cap as the store having run out, and the
// note about reaching the limit would be quoting a number nobody asked for.
func TestALoweredLimitIsSaidOutLoud(t *testing.T) {
	srv, got := searchServer(t, tempoFound)
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: srv.URL},
	}, nil)

	text, out, err := callSearch(t, cfg, searchInput{Service: "checkout", Limit: 5000})
	require.NoError(t, err)
	require.Contains(t, out.Note, "limit 5000 was lowered to 100")
	require.Contains(t, text, "limit 5000 was lowered to 100")

	_, params := got.got()
	require.Equal(t, "100", params.Get("limit"), "and the store was asked for what it will answer")
}

// TestANegativeLimitIsRefusedRatherThanRead: turning it into the default would
// answer a question that was never put.
func TestANegativeLimitIsRefusedRatherThanRead(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: "https://tempo.example.com"},
	}, nil)

	_, _, err := callSearch(t, cfg, searchInput{Service: "checkout", Limit: -5})
	require.ErrorContains(t, err, "limit -5 is negative")
	require.ErrorContains(t, err, "up to 100")
}
