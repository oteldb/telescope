package source

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// searchServer answers one path and records what it was asked.
func searchServer(t *testing.T, path, body string) (*httptest.Server, *url.Values, *http.Header) {
	t.Helper()
	var (
		got    url.Values
		header http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.Error(w, "no such path", http.StatusNotFound)
			return
		}
		got, header = r.URL.Query(), r.Header.Clone()
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &header
}

func TestASearchAsksTempoForTraceQL(t *testing.T) {
	srv, got, header := searchServer(t, tempoSearchPath, `{"traces":[{"traceID":"abc"}]}`)

	e := Endpoint{URL: srv.URL, Collector: CollectorTempo, Tenant: "1:2", Token: "s3cret"}
	data, err := e.SearchTraces(t.Context(), TraceQuery{Service: "api", Limit: 7})
	require.NoError(t, err)
	require.JSONEq(t, `{"traces":[{"traceID":"abc"}]}`, string(data))

	require.Equal(t, `{resource.service.name="api"}`, got.Get("q"))
	require.Equal(t, "7", got.Get("limit"))
	require.NotEmpty(t, got.Get("start"))
	require.Equal(t, "1:2", header.Get("X-Scope-OrgID"),
		"the trace store borrows the place's tenant, and Tempo takes it whole")
	require.Equal(t, "Bearer s3cret", header.Get("Authorization"))
}

// The Jaeger API is served by VictoriaTraces as often as by Jaeger, and that
// one splits a tenant the way its logs do.
func TestAJaegerStoreSplitsTheTenantTheWayItsLogsAre(t *testing.T) {
	srv, _, header := searchServer(t, jaegerSearchPath, `{"data":[]}`)

	e := Endpoint{URL: srv.URL, Collector: CollectorJaeger, Tenant: "1:2"}
	_, err := e.SearchTraces(t.Context(), TraceQuery{Service: "api"})
	require.NoError(t, err)
	require.Equal(t, "1", header.Get("AccountID"))
	require.Equal(t, "2", header.Get("ProjectID"))
}

// One trace comes off the API the store declared. Both serve it on the same
// path and answer with different documents, so asking the wrong one is not a
// failure that could be caught and retried — it is a trace that decodes into
// nothing.
func TestATraceIsFetchedFromTheAPITheStoreDeclared(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	_, err := Endpoint{URL: srv.URL, Collector: CollectorJaeger}.Trace(t.Context(), "abc")
	require.NoError(t, err)
	require.Equal(t, []string{"/api/traces/abc"}, asked)

	asked = nil
	_, err = Endpoint{URL: srv.URL, Collector: CollectorTempo}.Trace(t.Context(), "abc")
	require.NoError(t, err)
	require.Equal(t, []string{"/api/v2/traces/abc"}, asked)
}

// The store declares which API answers there, so a Jaeger one is asked Jaeger's
// question the first time rather than after a 404.
func TestASearchAsksJaegerForItsOwnParameters(t *testing.T) {
	srv, got, _ := searchServer(t, jaegerSearchPath, `{"data":[]}`)

	e := Endpoint{URL: srv.URL, Collector: CollectorJaeger}
	_, err := e.SearchTraces(t.Context(), TraceQuery{Service: "api", Operation: "GET /"})
	require.NoError(t, err)

	require.Equal(t, "api", got.Get("service"))
	require.Equal(t, "GET /", got.Get("operation"))
	require.Empty(t, got.Get("q"), "TraceQL means nothing here")
}

// A `traces:` url that says nothing about its API is the Tempo it meant before
// it could say, so a config written against an older telescope still works.
func TestATraceStoreWithNoTypeIsTempo(t *testing.T) {
	srv, got, _ := searchServer(t, tempoSearchPath, `{"traces":[]}`)

	_, err := Endpoint{URL: srv.URL}.SearchTraces(t.Context(), TraceQuery{})
	require.NoError(t, err)
	require.Equal(t, "{}", got.Get("q"))
}

// A search Jaeger will refuse is refused here, before the round trip.
func TestASearchJaegerWillNotAnswerIsNotSent(t *testing.T) {
	var asked bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		asked = true
	}))
	defer srv.Close()

	_, err := Endpoint{URL: srv.URL, Collector: CollectorJaeger}.SearchTraces(t.Context(), TraceQuery{})
	require.ErrorContains(t, err, "service")
	require.False(t, asked, "nothing was asked")
}

func TestASearchReportsWhatTheStoreRefusedWith(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid TraceQL query", http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := Endpoint{URL: srv.URL}.SearchTraces(t.Context(), TraceQuery{})
	require.ErrorContains(t, err, "invalid TraceQL query")
}

func TestServicesAreListedTheWayEachStoreListsThem(t *testing.T) {
	t.Run("jaeger", func(t *testing.T) {
		srv, _, _ := searchServer(t, jaegerServicesPath, `{"data":["gateway","api","api"]}`)
		got, err := Endpoint{URL: srv.URL, Collector: CollectorJaeger}.TraceServices(t.Context())
		require.NoError(t, err)
		require.Equal(t, []string{"api", "gateway"}, got)
	})

	t.Run("tempo", func(t *testing.T) {
		srv, _, _ := searchServer(t, tempoTagValuesPath+tempoServiceTag+"/values",
			`{"tagValues":[{"type":"string","value":"gateway"},{"type":"string","value":"api"}]}`)
		got, err := Endpoint{URL: srv.URL, Collector: CollectorTempo}.TraceServices(t.Context())
		require.NoError(t, err)
		require.Equal(t, []string{"api", "gateway"}, got)
	})
}

// Both APIs changed their minds about whether an element of these lists is a
// string or an object holding one, and both spellings are still served.
func TestAListedNameIsReadWhicheverWayItWasWritten(t *testing.T) {
	srv, _, _ := searchServer(t, jaegerServicesPath+"/api/operations",
		`{"data":[{"name":"GET /v1/orders","spanKind":"server"},"POST /v1/orders",{"spanKind":"client"}]}`)

	got, err := Endpoint{URL: srv.URL, Collector: CollectorJaeger}.TraceOperations(t.Context(), "api")
	require.NoError(t, err)
	require.Equal(t, []string{"GET /v1/orders", "POST /v1/orders"}, got)
}

// Tempo has no index of what one service was called to do, so it answers with
// every span name it holds rather than with nothing.
func TestTempoListsOperationsWithoutAService(t *testing.T) {
	srv, _, _ := searchServer(t, tempoTagValuesPath+tempoOperationTag+"/values",
		`{"tagValues":["GET /v1/orders"]}`)

	got, err := Endpoint{URL: srv.URL}.TraceOperations(t.Context(), "")
	require.NoError(t, err)
	require.Equal(t, []string{"GET /v1/orders"}, got)
}

func TestJaegerListsNoOperationsUntilAServiceIsNamed(t *testing.T) {
	var asked bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		asked = true
	}))
	defer srv.Close()

	got, err := Endpoint{URL: srv.URL, Collector: CollectorJaeger}.TraceOperations(t.Context(), " ")
	require.NoError(t, err)
	require.Empty(t, got)
	require.False(t, asked)
}

// A store that will not say what it holds costs the suggestions and never the
// search, which is the bargain the filter prompt already makes.
func TestAStoreThatWillNotListIsNotAFailedSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := Endpoint{URL: srv.URL}.TraceServices(t.Context())
	require.Error(t, err, "the caller decides what to do about it")

	_, searchErr := Endpoint{URL: srv.URL}.SearchTraces(t.Context(), TraceQuery{Range: Range{
		Since: time.Now().Add(-time.Hour),
	}})
	require.Error(t, searchErr)
}
