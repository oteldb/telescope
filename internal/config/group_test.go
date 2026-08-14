package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// TestLoadGroup: a group reads the places it names as one stream, and the
// window belongs to the group rather than to any of them.
func TestLoadGroup(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: api
    type: docker
    container: api
    range: 24h
  - name: worker
    type: docker
    container: worker
groups:
  - name: everything
    places: [api, worker]
    range: 1h
    tail: 50
`))
	require.NoError(t, err)

	stream, ready, err := cfg.Groups[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, source.CollectorMerge, stream.Collector)
	require.Equal(t, "merge api + worker", stream.Command())

	children := stream.Children()
	require.Len(t, children, 2)
	require.Equal(t, []string{"api", "worker"}, []string{children[0].Name, children[1].Name})
	for _, child := range children {
		require.Equal(t, "last 1h", child.Range.Label(), "the group is one view, with one window")
		require.Equal(t, 50, child.Tail)
	}
}

// TestGroupOfEndpointsNeedsNoQuery: several log databases read as one, with
// nothing named anywhere — the query is typed once, into the view.
func TestGroupOfEndpointsNeedsNoQuery(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: vl-eu
    type: victorialogs
    url: https://eu.example.com
  - name: vl-us
    type: victorialogs
    url: https://us.example.com
groups:
  - name: prod
    places: [vl-eu, vl-us]
`))
	require.NoError(t, err)

	stream, ready, err := cfg.Groups[0].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Len(t, stream.Children(), 2)
}

// TestLoadGroupErrors: what a group refuses at load, when it is still a mistake
// in the file rather than a stream that failed to open.
func TestLoadGroupErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		wantErr string
	}{
		{"undeclared", `
places:
  - name: api
    type: docker
    container: api
groups:
  - name: everything
    places: [api, nope]
`, `names undeclared place "nope"`},
		{"places that ask for different things", `
places:
  - name: pods
    type: kubectl
  - name: containers
    type: docker
groups:
  - name: everything
    places: [pods, containers]
`, "do not ask for the same thing"},
		{"one place", `
places:
  - name: api
    type: docker
    container: api
groups:
  - name: everything
    places: [api]
`, "length must be at least 2"},
		{"no name", `
places:
  - name: api
    type: docker
    container: api
groups:
  - places: [api]
`, `groups[0] must set "name"`},
		{"declared twice", `
places:
  - name: api
    type: docker
    container: api
  - name: worker
    type: docker
    container: worker
groups:
  - name: both
    places: [api, worker]
  - name: both
    places: [worker, api]
`, "groups[1] repeats groups[0]"},
		{"named after a place", `
places:
  - name: api
    type: docker
    container: api
  - name: worker
    type: docker
    container: worker
groups:
  - name: api
    places: [api, worker]
`, "is also the name of a place"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadFrom(write(t, tt.content))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestGroupAsksOnce: the same deployment usually has the same name on every
// cluster, so a group of clusters that name no pod asks for one, once, and
// gives the answer to all of them.
func TestGroupAsksOnce(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: ops
    type: kubectl
    kubeconfig: /root/ops.yml
  - name: obs
    type: kubectl
    kubeconfig: /root/obs.yml
groups:
  - name: both
    places: [ops, obs]
`))
	require.NoError(t, err, "a group that asks is not a broken one")

	asks, ok := cfg.Groups[0].Asks()
	require.True(t, ok)
	require.Equal(t, source.CollectorKubectl, asks)

	stream, ready, err := cfg.Groups[0].Stream()
	require.NoError(t, err)
	require.False(t, ready, "it cannot open until it is answered")

	answered := stream.WithTarget("flux-system/deploy/kustomize-controller")
	require.NoError(t, answered.Validate())
	for _, child := range answered.Children() {
		require.Equal(t, "flux-system", child.Namespace)
		require.Equal(t, "deploy/kustomize-controller", child.Target)
	}
	require.Equal(t, []string{"/root/ops.yml", "/root/obs.yml"},
		[]string{answered.Merge[0].KubeConfig, answered.Merge[1].KubeConfig},
		"each place is still reached its own way")
}

// TestGroupAsksNothingWhenEveryPlaceSaysEnough: a place that already names a
// target is not asked about again.
func TestGroupAsksNothingWhenEveryPlaceSaysEnough(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: api
    type: docker
    container: api
  - name: worker
    type: docker
    container: worker
groups:
  - name: both
    places: [api, worker]
`))
	require.NoError(t, err)
	_, ok := cfg.Groups[0].Asks()
	require.False(t, ok)
}

// TestLoadGroupUnreadableToken: a group whose place has an unreadable token is
// one group that will not open, not a config that cannot be read.
func TestLoadGroupUnreadableToken(t *testing.T) {
	cfg, err := loadFrom(write(t, `
places:
  - name: prod api
    type: victorialogs
    url: https://logs.example.com
    token:
      env: TELESCOPE_TEST_UNSET_TOKEN
    target: 'error'
  - name: local
    type: docker
    container: api
groups:
  - name: everything
    places: [prod api, local]
`))
	require.NoError(t, err)

	_, ready, err := cfg.Groups[0].Stream()
	require.False(t, ready)
	require.ErrorContains(t, err, `place "prod api"`)
	require.ErrorContains(t, err, "TELESCOPE_TEST_UNSET_TOKEN")

	_, ready, err = cfg.Places[1].Stream()
	require.NoError(t, err, "the places it names still open on their own")
	require.True(t, ready)
}
