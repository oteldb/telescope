package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func TestLoadEndpoints(t *testing.T) {
	t.Setenv("TELESCOPE_TEST_TOKEN", "s3cret")
	path := write(t, `
endpoints:
  - name: prod
    type: victorialogs
    url: https://grafana.example.com
    datasource: abc123
    token:
      env: TELESCOPE_TEST_TOKEN
    tenant: "1:1"
    headers:
      X-Scope: logs

sources:
  - name: prod api
    collector: victorialogs
    endpoint: prod
    target: 'kubernetes.namespace:oteldb'
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)

	stream, ready, err := cfg.Sources[0].Stream()
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

func TestLoadEndpointTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("s3cret\n"), 0o600))

	path := write(t, `
endpoints:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      file: `+tokenPath+`
sources:
  - name: prod
    collector: victorialogs
    endpoint: prod
    target: 'error'
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)
	stream, _, err := cfg.Sources[0].Stream()
	require.NoError(t, err)
	require.Equal(t, "s3cret", stream.Endpoint.Token, "the trailing newline is not part of the token")
}

// TestLoadEndpointMissingToken: an endpoint whose token is not exported is one
// source that cannot open, not a config that cannot be read.
func TestLoadEndpointMissingToken(t *testing.T) {
	path := write(t, `
endpoints:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      env: TELESCOPE_TEST_UNSET_TOKEN
sources:
  - name: prod
    collector: victorialogs
    endpoint: prod
    target: 'error'
  - name: local docker
    collector: docker
    container: navidrome
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)
	require.Len(t, cfg.Sources, 2)

	_, ready, err := cfg.Sources[0].Stream()
	require.False(t, ready)
	require.ErrorContains(t, err, "TELESCOPE_TEST_UNSET_TOKEN")

	_, ready, err = cfg.Sources[1].Stream()
	require.NoError(t, err, "the other sources still open")
	require.True(t, ready)
}

func TestLoadEndpointErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		wantErr string
	}{
		{"undeclared", `
sources:
  - name: prod
    collector: victorialogs
    endpoint: nope
    target: error
`, "undeclared endpoint"},
		{"no endpoint named", `
sources:
  - name: prod
    collector: victorialogs
    target: error
`, "requires an endpoint"},
		{"declared twice", `
endpoints:
  - name: prod
    type: loki
    url: https://logs.example.com
  - name: prod
    type: loki
    url: https://other.example.com
sources: []
`, "declared twice"},
		{"no url", `
endpoints:
  - name: prod
    type: loki
sources: []
`, "url is required"},
		{"no type", `
endpoints:
  - name: prod
    url: https://logs.example.com
sources: []
`, "type must be victorialogs or loki"},
		{"the endpoint speaks something else", `
endpoints:
  - name: prod
    type: loki
    url: https://logs.example.com
sources:
  - name: prod
    collector: victorialogs
    endpoint: prod
    target: 'error'
`, "speaks loki, not victorialogs"},
		{"two tokens", `
endpoints:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      env: A
      file: /b
sources: []
`, "token names env and file"},
		{"the spelling token replaced", `
endpoints:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token_env: GRAFANA_TOKEN
sources: []
`, "token_env is now token: {env: GRAFANA_TOKEN}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadFrom(write(t, tt.content))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
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

// TestSourceTakesCollectorFromEndpoint: an endpoint already says which API it
// speaks, so a source naming one need not repeat it.
func TestSourceTakesCollectorFromEndpoint(t *testing.T) {
	cfg, err := loadFrom(write(t, `
endpoints:
  - name: prod
    type: loki
    url: https://logs.example.com
sources:
  - name: prod api
    endpoint: prod
    target: '{app="api"}'
`))
	require.NoError(t, err)
	stream, ready, err := cfg.Sources[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, source.CollectorLoki, stream.Collector)
	require.Equal(t, `{app="api"}`, stream.Target)
}
