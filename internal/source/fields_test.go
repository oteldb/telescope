package source

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVictoriaLogsFieldNames: the envelope a line is unwrapped from on the way
// in is the same one the prompt has to complete into, so _msg is offered as the
// msg it will turn out to be called.
func TestVictoriaLogsFieldNames(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, vlogsFieldNamesPath, r.URL.Path)
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"values":[
			{"value":"_msg","hits":3},
			{"value":"_time","hits":3},
			{"value":"_stream","hits":3},
			{"value":"pod","hits":2}
		]}`))
	}))
	defer srv.Close()

	names, err := vlogsConfig(srv.URL, false).FieldNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"msg", "time", "_stream", "pod"}, names)
	require.Equal(t, "app:api", got.Get("query"), "the place bounds what is listed")
}

// TestVictoriaLogsFieldValues: a name is asked for under the name the database
// stored it as, not the one it is shown as.
func TestVictoriaLogsFieldValues(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, vlogsFieldValuesPath, r.URL.Path)
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"values":[{"value":"api-7","hits":9},{"value":"api-8","hits":4}]}`))
	}))
	defer srv.Close()

	values, err := vlogsConfig(srv.URL, false).FieldValues(t.Context(), "msg")
	require.NoError(t, err)
	require.Equal(t, []string{"api-7", "api-8"}, values)
	require.Equal(t, "_msg", got.Get("field"))
	require.Equal(t, "200", got.Get("limit"))
}

// TestVictoriaLogsFieldNamesIgnoreTheFilter: what is offered has to be what the
// place holds and not what the half-typed query already narrowed it to.
func TestVictoriaLogsFieldNamesIgnoreTheFilter(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"values":[]}`))
	}))
	defer srv.Close()

	cfg := vlogsConfig(srv.URL, false).WithFilter(filterExpr(t, "reset"))
	_, err := cfg.FieldNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, "app:api", got.Get("query"))
}

// TestLokiLabels: Loki answers a list of strings under "data", and a value
// listing hangs off the label's own path.
func TestLokiLabels(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"status":"success","data":["app","namespace"]}`))
	}))
	defer srv.Close()

	cfg := Config{
		Collector: CollectorLoki,
		Endpoint:  Endpoint{URL: srv.URL, Collector: CollectorLoki},
		Target:    `{app="api"}`,
	}
	names, err := cfg.FieldNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"app", "namespace"}, names)

	_, err = cfg.FieldValues(t.Context(), "app")
	require.NoError(t, err)
	require.Equal(t, []string{lokiLabelsPath, "/loki/api/v1/label/app/values"}, paths)
}

// TestFieldsOfACommand: a process writing to a pipe knows nothing about its own
// output, which is not a failure.
func TestFieldsOfACommand(t *testing.T) {
	cfg := Config{Collector: CollectorDocker, Container: "api"}
	names, err := cfg.FieldNames(t.Context())
	require.NoError(t, err)
	require.Empty(t, names)

	values, err := cfg.FieldValues(t.Context(), "pod")
	require.NoError(t, err)
	require.Empty(t, values)
}

// TestFieldsOfAMerge: a filter over a merge is one filter over all of it, so the
// names are the union — and a child that cannot answer contributes nothing
// rather than failing the lot.
func TestFieldsOfAMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"value":"pod"},{"value":"zone"}]}`))
	}))
	defer srv.Close()

	cfg := Config{Collector: CollectorMerge, Merge: []Config{
		vlogsConfig(srv.URL, false),
		{Collector: CollectorDocker, Container: "api"},
		vlogsConfig(srv.URL, false),
	}}
	names, err := cfg.FieldNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"pod", "zone"}, names, "each name once, whichever child had it")
}

// TestFieldsReportARefusal: an endpoint that answers with an error is not
// silently an endpoint with no fields, since the prompt would then say the
// stream has nothing to complete by.
func TestFieldsReportARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := vlogsConfig(srv.URL, false).FieldNames(t.Context())
	require.ErrorContains(t, err, "401")
}
