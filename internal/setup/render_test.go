package setup

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

// TestRenderWritesAFileThatLoads: what init writes is what telescope reads, so
// every key it spells has to be one the loader claims and every value one the
// invariants accept.
func TestRenderWritesAFileThatLoads(t *testing.T) {
	data, err := Render([]Offer{
		{
			Note:  "container running ghcr.io/example/api",
			Place: config.Place{Name: "api", Type: "docker", Container: "api"},
		},
		{
			Note: "kubernetes context in /home/example/.kube/config",
			Place: config.Place{
				Name: "staging", Type: "kubectl",
				KubeConfig: "/home/example/.kube/config",
				Context:    "staging", Namespace: "payments",
			},
		},
		{
			Note: "grafana datasource",
			Place: config.Place{
				Name: "logs", Type: "victorialogs",
				URL:        "https://grafana.example.com",
				Datasource: "abc123",
				Token:      config.Token{Env: "GRAFANA_TOKEN"},
				Traces:     config.TraceStore{URL: "https://tempo.example.com", Type: "tempo"},
			},
		},
		{
			Note: "ssh host",
			Place: config.Place{
				Name: "node-1", Type: "journalctl", Via: "ssh://node-1",
				Traces: config.TraceStore{URL: "https://jaeger.example.com", Type: "jaeger"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, `# yaml-language-server: $schema=`+config.SchemaURL+`
places:
  # container running ghcr.io/example/api
  - name: api
    type: docker
    container: api
  # kubernetes context in /home/example/.kube/config
  - name: staging
    type: kubectl
    kubeconfig: /home/example/.kube/config
    context: staging
    namespace: payments
  # grafana datasource
  - name: logs
    type: victorialogs
    url: https://grafana.example.com
    datasource: abc123
    token:
      env: GRAFANA_TOKEN
    traces: https://tempo.example.com
  # ssh host
  - name: node-1
    type: journalctl
    via: ssh://node-1
    traces:
      url: https://jaeger.example.com
      type: jaeger
`, string(data))

	cfg, err := config.Parse(data)
	require.NoError(t, err)
	require.Len(t, cfg.Places, 4)
}

// TestRenderRefusesAPlaceThatWouldNotOpen: the check is the loader's own, so a
// place that says something the config rejects is reported here rather than on
// a start screen.
func TestRenderRefusesAPlaceThatWouldNotOpen(t *testing.T) {
	_, err := Render([]Offer{{
		Place: config.Place{Name: "logs", Type: "victorialogs"},
	}})
	require.ErrorContains(t, err, "requires a url")
}

// TestRenderKeepsAWordYAMLWouldReadAsSomethingElse: a container may be called
// "no", and a config that turned it into false would look for a container of
// that name and never say why.
func TestRenderKeepsAWordYAMLWouldReadAsSomethingElse(t *testing.T) {
	data, err := Render([]Offer{{
		Place: config.Place{Name: "no", Type: "docker", Container: "no"},
	}})
	require.NoError(t, err)

	cfg, err := config.Parse(data)
	require.NoError(t, err)
	require.Equal(t, "no", cfg.Places[0].Container)
}
