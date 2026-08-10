package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoad(t *testing.T) {
	path := write(t, `
sources:
  - name: node1 pods
    transport: ssh
    host: node1
    collector: kubectl
    target: oteldb/oteldb-0
    kubeconfig: /root/.kube/ops.kubeconfig
    sudo: true
    query: error
  - name: local docker
    collector: docker
    container: navidrome
    tail: 50
    follow: false
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)
	require.Len(t, cfg.Sources, 2)

	pods, ready, err := cfg.Sources[0].Stream()
	require.NoError(t, err)
	require.True(t, ready, "it names a pod, so it opens straight away")
	require.Equal(t, source.TransportSSH, pods.Transport)
	require.True(t, pods.Elevate)
	require.Equal(t, "oteldb", pods.Namespace, "the compact ns/pod form is split")
	require.Equal(t, "oteldb-0", pods.Target)
	require.Equal(t, 1000, pods.Tail, "tail defaults")
	require.True(t, pods.Follow, "follow defaults")
	require.Equal(t, "error", cfg.Sources[0].Query)
	require.Equal(t,
		"sudo -n kubectl --kubeconfig=/root/.kube/ops.kubeconfig "+
			"logs -n oteldb oteldb-0 --tail 1000 -f",
		pods.Command())

	docker, ready, err := cfg.Sources[1].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, source.TransportLocal, docker.Transport, "transport defaults to local")
	require.Equal(t, 50, docker.Tail)
	require.False(t, docker.Follow, "follow: false is not mistaken for unset")
}

func TestLoadUserUnitPrefix(t *testing.T) {
	cfg, err := loadFrom(write(t, "sources:\n  - name: sync\n    collector: journalctl\n    unit: user/syncthing\n"))
	require.NoError(t, err)

	got, ready, err := cfg.Sources[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.True(t, got.UserUnit)
	require.Equal(t, "syncthing", got.Unit)
}

// TestPartialSourceIsValid: a source may pin a cluster and leave the pod for
// the prompt, which is how one config entry covers a whole kubeconfig.
func TestPartialSourceIsValid(t *testing.T) {
	cfg, err := loadFrom(write(t, `
sources:
  - name: k3s-ops
    transport: ssh
    host: node1
    collector: kubectl
    kubeconfig: /root/.kube/ops.kubeconfig
    sudo: true
`))
	require.NoError(t, err, "an unfinished source is not a broken one")

	got, ready, err := cfg.Sources[0].Stream()
	require.NoError(t, err)
	require.False(t, ready, "it still needs a pod")
	require.Equal(t, "/root/.kube/ops.kubeconfig", got.KubeConfig)
	require.True(t, got.Elevate)
	require.Equal(t, "node1", got.Host)
}

// TestLoadRange: a declared window is kept as written and resolved when the
// source is opened, so "24h" means the last day on every run.
func TestLoadRange(t *testing.T) {
	cfg, err := loadFrom(write(t, `
sources:
  - name: yesterday's api
    collector: docker
    container: api
    range: yesterday
    follow: true
`))
	require.NoError(t, err)

	got, ready, err := cfg.Sources[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, "yesterday", got.Range.Spec)
	require.True(t, got.Range.Closed())
	require.Equal(t, 24*time.Hour, got.Range.Until.Sub(got.Range.Since))
	require.Contains(t, got.Command(), "--until")
	require.NotContains(t, got.Command(), " -f", "a window that has closed is not followed")
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	cfg, err := loadFrom(filepath.Join(t.TempDir(), "absent.yaml"))
	require.NoError(t, err)
	require.Empty(t, cfg.Sources)
}

func TestLoadRejectsBadSources(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		errText string
	}{
		{"no name", "sources:\n  - collector: docker\n    container: app\n", "name is required"},
		{"unknown collector", "sources:\n  - name: x\n    collector: nope\n", "unknown collector"},
		{"unknown transport", "sources:\n  - name: x\n    transport: telnet\n    collector: docker\n    container: a\n", "unknown transport"},
		{"ssh without host", "sources:\n  - name: x\n    transport: ssh\n    collector: journalctl\n", "requires a host"},
		{"malformed yaml", "sources: [oops\n", "parse"},
		{
			"unreadable range",
			"sources:\n  - name: x\n    collector: docker\n    container: a\n    range: yesteryear\n",
			"cannot read",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadFrom(write(t, tt.content))
			require.ErrorContains(t, err, tt.errText)
		})
	}
}
