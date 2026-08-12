package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func TestLoadHTTPPlace(t *testing.T) {
	t.Setenv("TELESCOPE_TEST_TOKEN", "s3cret")
	path := write(t, `
places:
  - name: prod
    type: victorialogs
    url: https://grafana.example.com
    datasource: abc123
    token:
      env: TELESCOPE_TEST_TOKEN
    tenant: "1:1"
    headers:
      X-Scope: logs
    target: 'kubernetes.namespace:oteldb'
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)

	stream, ready, err := cfg.Places[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, source.CollectorVictoriaLogs, stream.Collector)
	require.Equal(t,
		"https://grafana.example.com/api/datasources/proxy/uid/abc123",
		stream.Endpoint.URL, "a datasource uid is resolved against the Grafana it lives in")
	require.Equal(t, "s3cret", stream.Endpoint.Token)
	require.Equal(t, "1:1", stream.Endpoint.Tenant)
	require.Equal(t, map[string]string{"X-Scope": "logs"}, stream.Endpoint.Header)
	require.Equal(t, "kubernetes.namespace:oteldb", stream.Target)
}

// TestHTTPPlaceNeedsNoTarget: LogsQL has a match-all, so a VictoriaLogs place
// opens as it stands and can be named by a group without a query anywhere.
func TestHTTPPlaceNeedsNoTarget(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
`))
	require.NoError(t, err)
	_, ready, err := cfg.Places[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
}

func TestLoadPlaceTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("s3cret\n"), 0o600))

	path := write(t, `
places:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      file: `+tokenPath+`
    target: 'error'
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)
	stream, _, err := cfg.Places[0].Stream()
	require.NoError(t, err)
	require.Equal(t, "s3cret", stream.Endpoint.Token, "the trailing newline is not part of the token")
}

// TestLoadPlaceMissingToken: a place whose token is not exported is one place
// that cannot open, not a config that cannot be read.
func TestLoadPlaceMissingToken(t *testing.T) {
	path := write(t, `
places:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      env: TELESCOPE_TEST_UNSET_TOKEN
    target: 'error'
  - name: local docker
    type: docker
    container: navidrome
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)
	require.Len(t, cfg.Places, 2)

	_, ready, err := cfg.Places[0].Stream()
	require.False(t, ready)
	require.ErrorContains(t, err, "TELESCOPE_TEST_UNSET_TOKEN")

	_, ready, err = cfg.Places[1].Stream()
	require.NoError(t, err, "the other places still open")
	require.True(t, ready)
}

func TestLoadHTTPPlaceErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		wantErr string
	}{
		{"no url", `
places:
  - name: prod
    type: loki
`, "requires a url"},
		{"two tokens", `
places:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      env: A
      file: /b
`, "token names env and file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadFrom(write(t, tt.content))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestLokiPlaceIsAnEndpointAndNothingElse: Loki selects streams by label and
// has no match-all, but the labels are the filter's and the filter is the
// view's — so the place is the endpoint, and it opens as it stands.
func TestLokiPlaceIsAnEndpointAndNothingElse(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: prod
    type: loki
    url: https://logs.example.com
    query: app=api
`))
	require.NoError(t, err)

	_, ready, err := cfg.Places[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, "app=api", cfg.Places[0].Query,
		"which is the filter, and what selects the stream with it")
}

// TestLokiPlaceHasNoQueryOfItsOwn: LogQL used to be typed into target, and a
// config that still says so is told where the filter lives now rather than
// quietly ignored.
func TestLokiPlaceHasNoQueryOfItsOwn(t *testing.T) {
	_, err := loadFrom(write(t, `
places:
  - name: prod
    type: loki
    url: https://logs.example.com
    target: '{app="api"}'
`))
	require.ErrorContains(t, err, "query: app=api")
}

// TestEndpointScope: a query written for one cluster means nothing against
// another, and nothing at all against the host telescope runs on.
func TestEndpointScope(t *testing.T) {
	vlogs := func(name string) source.Config {
		return source.Config{
			Transport: source.TransportSSH,
			Host:      "node1",
			Collector: source.CollectorVictoriaLogs,
			Endpoint:  source.Endpoint{Name: name, URL: "https://logs.example.com"},
			Target:    "error",
		}
	}
	require.Equal(t, Scope(vlogs("prod")), Scope(vlogs("prod")))
	require.NotEqual(t, Scope(vlogs("prod")), Scope(vlogs("staging")))

	var h History
	h.Remember(vlogs("prod"))
	require.Equal(t, []string{"error"}, h.Recent(vlogs("prod")))
	require.Empty(t, h.Recent(vlogs("staging")))
	require.Empty(t, h.Hosts, "an endpoint is not reached over ssh")
}

// TestRememberTypedEndpoint: a URL typed at the prompt is remembered; a
// declared one is not, since the config file already holds it under a name.
func TestRememberTypedEndpoint(t *testing.T) {
	typed := source.Config{
		Collector: source.CollectorVictoriaLogs,
		Endpoint:  source.Endpoint{URL: "https://logs.example.com"},
		Target:    "error",
	}
	declared := typed
	declared.Endpoint.Name = "prod"

	var h History
	h.Remember(typed)
	h.Remember(declared)
	require.Equal(t, []string{"https://logs.example.com"}, h.Endpoints)
}

// TestPlaceProxy: a corporate place may need a proxy of its own, and a mistyped
// one is a mistake in the file rather than a silent direct connection.
func TestPlaceProxy(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: corp
    type: victorialogs
    url: https://logs.corp.example.com
    proxy: socks5h://127.0.0.1:1080
`))
	require.NoError(t, err)
	endpoints, err := cfg.Endpoints()
	require.NoError(t, err)
	require.Equal(t, "socks5h://127.0.0.1:1080", endpoints[0].Proxy)

	_, err = loadFrom(write(t, `
places:
  - name: corp
    type: victorialogs
    url: https://logs.corp.example.com
    proxy: "://nonsense"
`))
	require.ErrorContains(t, err, "cannot use proxy")
}

// TestVia: one field says how a place is reached, whether that is this machine
// or a host across an ssh connection.
func TestVia(t *testing.T) {
	for _, tt := range []struct {
		via       string
		transport source.Transport
		host      string
	}{
		{"", source.TransportLocal, ""},
		{"local", source.TransportLocal, ""},
		{"ssh://node1", source.TransportSSH, "node1"},
		{"ssh://ops@node1", source.TransportSSH, "ops@node1"},
	} {
		t.Run(tt.via, func(t *testing.T) {
			transport, host, err := parseVia(tt.via)
			require.NoError(t, err)
			require.Equal(t, tt.transport, transport)
			require.Equal(t, tt.host, host)
		})
	}
}
