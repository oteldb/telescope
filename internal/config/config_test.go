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
places:
  - name: node1 pods
    via: ssh://node1
    type: kubectl
    target: oteldb/oteldb-0
    kubeconfig: /root/.kube/ops.kubeconfig
    sudo: true
    query: error
  - name: local docker
    type: docker
    container: navidrome
    tail: 50
    follow: false
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)
	require.Len(t, cfg.Places, 2)

	pods, ready, err := cfg.Places[0].Stream()
	require.NoError(t, err)
	require.True(t, ready, "it names a pod, so it opens straight away")
	require.Equal(t, source.TransportSSH, pods.Transport)
	require.Equal(t, "node1", pods.Host)
	require.True(t, pods.Elevate)
	require.Equal(t, "oteldb", pods.Namespace, "the compact ns/pod form is split")
	require.Equal(t, "oteldb-0", pods.Target)
	require.Equal(t, 1000, pods.Tail, "tail defaults")
	require.True(t, pods.Follow, "follow defaults")
	require.Equal(t, "error", cfg.Places[0].Query)
	require.Equal(t,
		"sudo -n kubectl --kubeconfig=/root/.kube/ops.kubeconfig "+
			"logs -n oteldb oteldb-0 --tail 1000 -f",
		pods.Command())

	docker, ready, err := cfg.Places[1].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, source.TransportLocal, docker.Transport, "via defaults to local")
	require.Equal(t, 50, docker.Tail)
	require.False(t, docker.Follow, "follow: false is not mistaken for unset")
}

func TestLoadUserUnitPrefix(t *testing.T) {
	cfg, err := loadFrom(write(t, "places:\n  - name: sync\n    type: journalctl\n    unit: user/syncthing\n"))
	require.NoError(t, err)

	got, ready, err := cfg.Places[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.True(t, got.UserUnit)
	require.Equal(t, "syncthing", got.Unit)
}

// TestPartialPlaceIsValid: a place may pin a cluster and leave the pod for the
// prompt, which is how one entry covers a whole kubeconfig.
func TestPartialPlaceIsValid(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: k3s-ops
    via: ssh://node1
    type: kubectl
    kubeconfig: /root/.kube/ops.kubeconfig
    sudo: true
`))
	require.NoError(t, err, "an unfinished place is not a broken one")

	got, ready, err := cfg.Places[0].Stream()
	require.NoError(t, err)
	require.False(t, ready, "it still needs a pod")
	require.Equal(t, "/root/.kube/ops.kubeconfig", got.KubeConfig)
	require.True(t, got.Elevate)
	require.Equal(t, "node1", got.Host)
}

// TestLoadRange: a declared window is kept as written and resolved when the
// place is opened, so "24h" means the last day on every run.
func TestLoadRange(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: yesterday's api
    type: docker
    container: api
    range: yesterday
    follow: true
`))
	require.NoError(t, err)

	got, ready, err := cfg.Places[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, "yesterday", got.Range.Spec)
	require.True(t, got.Range.Closed())
	require.Equal(t, 24*time.Hour, got.Range.Until.Sub(got.Range.Since))
	require.Contains(t, got.Command(), "--until")
	require.NotContains(t, got.Command(), " -f", "a window that has closed is not followed")
}

// TestUnreadableTokenKeepsTheConfig: a token telescope cannot read is the
// environment, not the file. It must not take the config down with it, and
// least of all by claiming the place declared no type — which is what happens
// if a place that failed to resolve forgets what it was.
func TestUnreadableTokenKeepsTheConfig(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: prod logs
    type: victorialogs
    url: https://logs.example.com
    token:
      env: TELESCOPE_TEST_UNSET
  - name: local docker
    type: docker
    container: api
`))
	require.NoError(t, err, "one unreadable token is not a broken config")
	require.Len(t, cfg.Places, 2)

	// The place that needs it says why, where it is chosen.
	_, ready, err := cfg.Places[0].Stream()
	require.False(t, ready)
	require.ErrorContains(t, err, "TELESCOPE_TEST_UNSET")

	// And the ones that do not need it still open.
	docker, ready, err := cfg.Places[1].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, source.CollectorDocker, docker.Collector)
}

// TestPlaceErrorsAreNamed: an error names the place it is about, since a number
// means counting entries in the file to find it. It is why the list is keyed by
// name rather than merged by position.
func TestPlaceErrorsAreNamed(t *testing.T) {
	_, err := loadFrom(write(t, "places:\n  - name: prod logs\n    target: pod\n"))
	require.ErrorContains(t, err, "places[name=prod logs]")
	require.ErrorContains(t, err, "type is required")
}

// TestLoadRejectsTheOldShape: sources and endpoints are gone, and a file still
// written in them is a file telescope would otherwise read as empty.
func TestLoadRejectsTheOldShape(t *testing.T) {
	_, err := loadFrom(write(t, "sources:\n  - name: x\n    collector: docker\n    container: a\n"))
	require.ErrorContains(t, err, "sources")
}

// TestLoadAcceptsTheSchemaKey: a file that names the schema describing it is
// how an editor finds one, so the key is read as an annotation rather than as a
// place that is not a place.
func TestLoadAcceptsTheSchemaKey(t *testing.T) {
	cfg, err := loadFrom(write(t,
		"$schema: "+SchemaURL+"\nplaces:\n  - name: x\n    type: docker\n    container: a\n"))
	require.NoError(t, err)
	require.Len(t, cfg.Places, 1)
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	cfg, err := loadFrom(filepath.Join(t.TempDir(), "absent.yaml"))
	require.NoError(t, err)
	require.Empty(t, cfg.Places)
}

func TestLoadEmptyFileIsEmpty(t *testing.T) {
	cfg, err := loadFrom(write(t, ""))
	require.NoError(t, err)
	require.Empty(t, cfg.Places)
}

func TestLoadRejectsBadPlaces(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		errText string
	}{
		{"no name", "places:\n  - type: docker\n    container: app\n", `places[0] must set "name"`},
		{"no type", "places:\n  - name: x\n", "type is required"},
		{"unknown type", "places:\n  - name: x\n    type: nope\n", "must be one of"},
		{"unknown via", "places:\n  - name: x\n    via: telnet://a\n    type: docker\n    container: a\n", "unknown via"},
		{"ssh without host", "places:\n  - name: x\n    via: ssh://\n    type: journalctl\n", "needs a host"},
		{"http reached over ssh", "places:\n  - name: x\n    type: loki\n    url: https://l\n    via: ssh://bastion\n", "reached over HTTP"},
		{"a command with a token", "places:\n  - name: x\n    type: docker\n    container: a\n    token:\n      env: T\n", "means nothing to it"},
		{"an http place with no url", "places:\n  - name: x\n    type: victorialogs\n", "requires a url"},
		{"declared twice", "places:\n  - name: x\n    type: docker\n    container: a\n  - name: x\n    type: docker\n    container: b\n", "places[1] repeats places[0]"},
		{"malformed yaml", "places: [oops\n", "parse"},
		{
			"unreadable range",
			"places:\n  - name: x\n    type: docker\n    container: a\n    range: yesteryear\n",
			"cannot read",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadFrom(write(t, tt.content))
			require.ErrorContains(t, err, tt.errText)
		})
	}
}
