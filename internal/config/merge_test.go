package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// TestLoadMerge: a source that names others reads them as one stream, and the
// window belongs to the merge rather than to any of them.
func TestLoadMerge(t *testing.T) {
	cfg, err := loadFrom(write(t, `
sources:
  - name: api
    collector: docker
    container: api
    range: 24h
  - name: worker
    collector: docker
    container: worker
  - name: everything
    merge: [api, worker]
    range: 1h
    tail: 50
`))
	require.NoError(t, err)

	stream, ready, err := cfg.Sources[2].Stream()
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, source.CollectorMerge, stream.Collector, "merging is what makes it a merge")
	require.Equal(t, "merge api + worker", stream.Command())

	children := stream.Children()
	require.Len(t, children, 2)
	require.Equal(t, []string{"api", "worker"}, []string{children[0].Name, children[1].Name})
	for _, child := range children {
		require.Equal(t, "last 1h", child.Range.Label(), "the merge is one view, with one window")
		require.Equal(t, 50, child.Tail)
	}
}

// TestLoadMergeErrors: what a merge refuses at load, when it is still a mistake
// in the file rather than a stream that failed to open.
func TestLoadMergeErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		wantErr string
	}{
		{"undeclared", `
sources:
  - name: api
    collector: docker
    container: api
  - name: everything
    merge: [api, nope]
`, `merges undeclared source "nope"`},
		{"itself", `
sources:
  - name: everything
    merge: [everything]
`, "which is itself a merge"},
		{"a merge of merges", `
sources:
  - name: api
    collector: docker
    container: api
  - name: worker
    collector: docker
    container: worker
  - name: half
    merge: [api, worker]
  - name: all
    merge: [half, api]
`, `source "all" merges "half", which is itself a merge`},
		{"a source that would have to ask", `
sources:
  - name: pods
    collector: kubectl
  - name: api
    collector: docker
    container: api
  - name: everything
    merge: [api, pods]
`, `merges "pods", which does not say enough to open`},
		{"one source", `
sources:
  - name: api
    collector: docker
    container: api
  - name: everything
    merge: [api]
`, "a merge reads two or more sources"},
		{"declared twice", `
sources:
  - name: api
    collector: docker
    container: api
  - name: api
    collector: docker
    container: api2
`, `source "api" is declared twice`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadFrom(write(t, tt.content))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestLoadMergeUnreadableToken: a merge whose endpoint token cannot be read is
// one source that will not open, not a config that cannot be read.
func TestLoadMergeUnreadableToken(t *testing.T) {
	cfg, err := loadFrom(write(t, `
endpoints:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      env: TELESCOPE_TEST_UNSET_TOKEN
sources:
  - name: prod api
    endpoint: prod
    target: 'error'
  - name: local
    collector: docker
    container: api
  - name: everything
    merge: [prod api, local]
`))
	require.NoError(t, err)

	_, ready, err := cfg.Sources[2].Stream()
	require.False(t, ready)
	require.ErrorContains(t, err, `merged "prod api"`)
	require.ErrorContains(t, err, "TELESCOPE_TEST_UNSET_TOKEN")

	_, ready, err = cfg.Sources[1].Stream()
	require.NoError(t, err, "the sources it names still open on their own")
	require.True(t, ready)
}
