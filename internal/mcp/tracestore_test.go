package mcp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

// jaegerTrace is one trace as the Jaeger query API answers with it, which is
// the encoding a store is asked for by name rather than by url.
func jaegerTrace(id string) string {
	return fmt.Sprintf(`{"data":[{"traceID":%q,"processes":{
		"p1":{"serviceName":"checkout"},"p2":{"serviceName":"payments"}},"spans":[
		{"traceID":%q,"spanID":"a1","operationName":"POST /api/orders",
		 "startTime":1786528800000000,"duration":1240000,"processID":"p1"},
		{"traceID":%q,"spanID":"e1","operationName":"POST /charge",
		 "references":[{"refType":"CHILD_OF","spanID":"a1"}],
		 "startTime":1786528800955000,"duration":280000,"processID":"p2",
		 "tags":[{"key":"error","type":"bool","value":true},
		         {"key":"http.status_code","type":"int64","value":502}]}]}]}`, id, id, id)
}

func traceServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/api/traces/"):]
		if id != "4bf92f3577b34da6a3ce929d0e0e4736" {
			http.Error(w, "trace not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jaegerTrace(id)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func callTrace(t *testing.T, cfg config.Config, in traceInput) (string, traceOutput, error) {
	t.Helper()
	res, out, err := traceHandler(cfg)(t.Context(), nil, in)
	if err != nil {
		return "", traceOutput{}, err
	}
	return res.Content[0].(*sdk.TextContent).Text, out, nil
}

// TestATraceIsReadByItsIdAlone: a trace id identifies the request and not the
// place it was kept, so an agent holding one off a log line has already said
// everything that names it.
func TestATraceIsReadByItsIdAlone(t *testing.T) {
	srv := traceServer(t)
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
		{Name: "prod traces", Type: "jaeger", URL: srv.URL},
	}, nil)

	text, out, err := callTrace(t, cfg, traceInput{ID: "4bf92f3577b34da6a3ce929d0e0e4736"})
	require.NoError(t, err)
	require.Equal(t, 2, out.Spans)
	require.Equal(t, 1, out.Failures)
	require.Contains(t, text, "checkout POST /api/orders")
	require.Contains(t, text, "!   payments  POST /charge")
}

// TestATraceIsReadThroughThePlaceThatNamesTheStore: the ids come off a log
// line, so the name an agent is holding is the log place and not the store.
func TestATraceIsReadThroughThePlaceThatNamesTheStore(t *testing.T) {
	srv := traceServer(t)
	cfg := testConfig(t, []config.Place{
		{
			Name:   "prod",
			Type:   "victorialogs",
			URL:    "https://logs.example.com",
			Traces: config.TraceStore{Name: "prod traces"},
		},
		{Name: "prod traces", Type: "jaeger", URL: srv.URL},
	}, nil)

	_, out, err := callTrace(t, cfg, traceInput{
		ID: "4bf92f3577b34da6a3ce929d0e0e4736", Place: "prod",
	})
	require.NoError(t, err)
	require.Equal(t, 2, out.Spans)
}

// TestSeveralStoresAreNotGuessedBetween: a trace id read at the wrong store
// answers that it is not there, which reads as the trace having aged out
// rather than as the question having gone to the wrong place.
func TestSeveralStoresAreNotGuessedBetween(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "jaeger", URL: "https://jaeger.example.com"},
		{Name: "staging traces", Type: "tempo", URL: "https://tempo.example.com"},
	}, nil)

	_, _, err := callTrace(t, cfg, traceInput{ID: "4bf92f3577b34da6a3ce929d0e0e4736"})
	require.ErrorContains(t, err, "name which store to read")
	require.ErrorContains(t, err, "prod traces, staging traces")
}

// TestAPlaceThatReadsNoTracesSaysWhichDo: a near miss on a name is the usual
// way of getting one wrong, and the list is short enough to write out.
func TestAPlaceThatReadsNoTracesSaysWhichDo(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "node", Type: "journalctl", Unit: "nginx"},
		{Name: "prod traces", Type: "tempo", URL: "https://tempo.example.com"},
	}, nil)

	_, _, err := callTrace(t, cfg, traceInput{ID: "4bf92f35", Place: "node"})
	require.ErrorContains(t, err, `"node" reads no traces`)
	require.ErrorContains(t, err, "the stores are prod traces")
}

// TestATraceThatIsNotThereSaysWhatThatMeans: an id that answers nothing is more
// often one that aged out than one that was mistyped, and a bare 404 says
// neither.
func TestATraceThatIsNotThereSaysWhatThatMeans(t *testing.T) {
	srv := traceServer(t)
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "jaeger", URL: srv.URL},
	}, nil)

	_, _, err := callTrace(t, cfg, traceInput{ID: "0000000000000000"})
	require.Error(t, err)
}

// TestATraceNeedsAnId: the store is optional and the id never is.
func TestATraceNeedsAnId(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: "https://tempo.example.com"},
	}, nil)

	_, _, err := callTrace(t, cfg, traceInput{})
	require.ErrorContains(t, err, "name a trace id")
}
