package setup

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

// datasourceList is what a Grafana answers with: two stores telescope reads,
// one it does not, and a Tempo beside them.
const datasourceList = `[
  {"uid":"log1","name":"Loki","type":"loki","url":"http://loki:3100"},
  {"uid":"log2","name":"VictoriaLogs","type":"victoriametrics-logs-datasource","url":"http://vl:9428"},
  {"uid":"met1","name":"Prometheus","type":"prometheus","url":"http://prom:9090"},
  {"uid":"trc1","name":"Tempo","type":"tempo","url":"http://tempo:3200"}
]`

func grafana(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		require.Equal(t, datasourcesPath, r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGrafanaWritesAPlacePerDatasourceItCanRead: what comes back is everything
// the Grafana can query, and telescope reads two kinds of it.
func TestGrafanaWritesAPlacePerDatasourceItCanRead(t *testing.T) {
	t.Setenv("TEST_GRAFANA_TOKEN", "secret")
	srv := grafana(t, datasourceList)

	g := Grafana{URL: srv.URL, Token: config.Token{Env: "TEST_GRAFANA_TOKEN"}}
	offers, notes, err := g.offers(t.Context(), srv.Client())
	require.NoError(t, err)
	require.Empty(t, notes)
	require.Equal(t, []string{"Loki", "VictoriaLogs"}, names(offers))

	loki := offers[0].Place
	require.Equal(t, "loki", loki.Type)
	require.Equal(t, srv.URL, loki.URL, "the query goes through the grafana the token opens")
	require.Equal(t, "log1", loki.Datasource)
	require.Equal(t, config.Token{Env: "TEST_GRAFANA_TOKEN"}, loki.Token)
	require.Equal(t, "victorialogs", offers[1].Place.Type)
}

// TestGrafanaNamesTheTokenRatherThanCopyingIt: init reads the secret to ask the
// API and writes down where it read it from, so the file it leaves behind is
// one that can be shared.
func TestGrafanaNamesTheTokenRatherThanCopyingIt(t *testing.T) {
	t.Setenv("TEST_GRAFANA_TOKEN", "secret")
	srv := grafana(t, datasourceList)

	offers, _, err := Grafana{
		URL:   srv.URL,
		Token: config.Token{Env: "TEST_GRAFANA_TOKEN"},
	}.offers(t.Context(), srv.Client())
	require.NoError(t, err)

	data, err := Render(offers)
	require.NoError(t, err)
	require.NotContains(t, string(data), "secret")
	require.Contains(t, string(data), "env: TEST_GRAFANA_TOKEN")
}

// TestTheOneTraceStoreIsAttachedToEveryPlace: a place reads its traces through
// the same door as its logs, and one store beside a handful of log datasources
// has nowhere else to belong.
func TestTheOneTraceStoreIsAttachedToEveryPlace(t *testing.T) {
	t.Setenv("TEST_GRAFANA_TOKEN", "secret")
	srv := grafana(t, datasourceList)

	offers, _, err := Grafana{
		URL:   srv.URL,
		Token: config.Token{Env: "TEST_GRAFANA_TOKEN"},
	}.offers(t.Context(), srv.Client())
	require.NoError(t, err)
	for _, o := range offers {
		require.Equal(t, config.DatasourceURL(srv.URL, "trc1"), o.Place.Traces.URL)
		require.Equal(t, "tempo", o.Place.Traces.Type)
	}
}

// TestSeveralTraceStoresAreSaidRatherThanGuessed: pointing half the places at
// the wrong store is worse than pointing none of them anywhere.
func TestSeveralTraceStoresAreSaidRatherThanGuessed(t *testing.T) {
	t.Setenv("TEST_GRAFANA_TOKEN", "secret")
	srv := grafana(t, `[
  {"uid":"log1","name":"Loki","type":"loki","url":"http://loki:3100"},
  {"uid":"t1","name":"Tempo","type":"tempo","url":"http://tempo:3200"},
  {"uid":"t2","name":"Jaeger","type":"jaeger","url":"http://jaeger:16686"}
]`)

	offers, notes, err := Grafana{
		URL:   srv.URL,
		Token: config.Token{Env: "TEST_GRAFANA_TOKEN"},
	}.offers(t.Context(), srv.Client())
	require.NoError(t, err)
	require.True(t, offers[0].Place.Traces.IsZero())
	require.Len(t, notes, 1)
	require.Contains(t, notes[0], "several trace stores")
}

// TestGrafanaSaysWhatAnsweredInsteadOfTheDatasources: listing datasources is an
// admin's right, so a token that works everywhere else answers 403 here, and
// which status came back is the difference between fixing the token and
// doubting the url.
func TestGrafanaSaysWhatAnsweredInsteadOfTheDatasources(t *testing.T) {
	t.Setenv("TEST_GRAFANA_TOKEN", "wrong")
	srv := grafana(t, datasourceList)

	_, _, err := Grafana{
		URL:   srv.URL,
		Token: config.Token{Env: "TEST_GRAFANA_TOKEN"},
	}.offers(t.Context(), srv.Client())
	require.ErrorContains(t, err, "403")
}

// TestProvisioningIsReadOffDisk: an operator has the files Grafana itself is
// configured from, and no API token at all.
func TestProvisioningIsReadOffDisk(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "logs.yaml"), []byte(`
apiVersion: 1
datasources:
  - name: Loki
    type: loki
    url: http://loki:3100
  - name: Tempo
    type: tempo
    url: http://tempo:3200
  - name: Guarded
    type: loki
    url: http://private:3100
    basicAuth: true
`), 0o600))

	offers, notes, err := Grafana{Provisioning: dir}.offers(t.Context(), http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, []string{"Loki", "Guarded"}, names(offers))
	require.Equal(t, "http://loki:3100", offers[0].Place.URL,
		"there is no grafana to proxy through, so the datasource's own url is it")
	require.True(t, offers[0].Place.Token.IsZero())
	require.Equal(t, "http://tempo:3200", offers[0].Place.Traces.URL)
	require.Len(t, notes, 1)
	require.Contains(t, notes[0], "password of its own",
		"the provisioning file holds it in the clear and telescope will not copy it")
}

func TestParseToken(t *testing.T) {
	for _, tt := range []struct {
		spec string
		want config.Token
	}{
		{"env:GRAFANA_TOKEN", config.Token{Env: "GRAFANA_TOKEN"}},
		{"file:~/.grafana-token", config.Token{File: "~/.grafana-token"}},
		{"exec:pass show grafana", config.Token{Exec: config.Argv{"sh", "-c", "pass show grafana"}}},
	} {
		got, err := ParseToken(tt.spec)
		require.NoError(t, err)
		require.Equal(t, tt.want, got)
	}

	// A secret written on the command line is one in the shell history and then
	// in the config file, which is the whole thing the config avoids.
	for _, spec := range []string{"", "glsa_deadbeef", "keychain:grafana"} {
		_, err := ParseToken(spec)
		require.Error(t, err, spec)
	}
}
