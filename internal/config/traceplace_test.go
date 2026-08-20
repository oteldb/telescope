package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// TestATraceStoreIsAPlace: a store is a system's and not a stream's, so it is
// declared once and named by every place whose lines carry ids into it.
func TestATraceStoreIsAPlace(t *testing.T) {
	t.Setenv("TEMPO_TOKEN", "trace-secret")
	cfg, err := Parse([]byte(`
places:
  - name: prod traces
    type: tempo
    url: https://tempo.example.com
    token: {env: TEMPO_TOKEN}
  - name: api
    type: victorialogs
    url: https://logs.example.com
    traces: prod traces
  - name: worker
    type: victorialogs
    url: https://logs.example.com
    traces: prod traces
`))
	require.NoError(t, err)

	for _, name := range []string{"api", "worker"} {
		place, ok := byName(cfg, name)
		require.True(t, ok)
		endpoint, reads, err := place.TraceEndpoint()
		require.NoError(t, err)
		require.True(t, reads, "%s named a store", name)
		require.Equal(t, "https://tempo.example.com", endpoint.URL)
		require.Equal(t, source.CollectorTempo, endpoint.Collector)
	}

	store, ok := byName(cfg, "prod traces")
	require.True(t, ok)
	require.True(t, store.ReadsTraces())
	endpoint, reads, err := store.TraceEndpoint()
	require.NoError(t, err)
	require.True(t, reads, "a store reads its own traces")
	require.Equal(t, "https://tempo.example.com", endpoint.URL)
}

// TestATraceStoreIsReachedItsOwnWay: the store's token is the store's. A place
// naming one is not a place lending it credentials.
func TestATraceStoreIsReachedItsOwnWay(t *testing.T) {
	t.Setenv("TEMPO_TOKEN", "trace-secret")
	t.Setenv("LOGS_TOKEN", "logs-secret")
	cfg, err := Parse([]byte(`
places:
  - name: traces
    type: jaeger
    url: https://jaeger.example.com
    token: {env: TEMPO_TOKEN}
    proxy: socks5h://127.0.0.1:1080
  - name: api
    type: loki
    url: https://loki.example.com
    token: {env: LOGS_TOKEN}
    traces: traces
`))
	require.NoError(t, err)

	place, ok := byName(cfg, "api")
	require.True(t, ok)
	endpoint, _, err := place.TraceEndpoint()
	require.NoError(t, err)
	require.Equal(t, "trace-secret", endpoint.Token)
	require.Equal(t, "socks5h://127.0.0.1:1080", endpoint.Proxy)
	require.Equal(t, source.CollectorJaeger, endpoint.Collector)
}

// TestAStoreWrittenOutStillBorrows: one place with one store needs no name, and
// what it is written on is the door it is read through.
func TestAStoreWrittenOutStillBorrows(t *testing.T) {
	t.Setenv("LOGS_TOKEN", "logs-secret")
	cfg, err := Parse([]byte(`
places:
  - name: homelab
    type: loki
    url: https://loki.example.com
    token: {env: LOGS_TOKEN}
    traces: https://tempo.example.com
`))
	require.NoError(t, err)

	place, ok := byName(cfg, "homelab")
	require.True(t, ok)
	endpoint, reads, err := place.TraceEndpoint()
	require.NoError(t, err)
	require.True(t, reads)
	require.Equal(t, "https://tempo.example.com", endpoint.URL)
	require.Equal(t, "logs-secret", endpoint.Token, "a store written out borrows the door")
}

// TestATraceStoreIsNotAStream: nothing is tailed from a store, and being told
// so is more use than an empty view.
func TestATraceStoreIsNotAStream(t *testing.T) {
	cfg, err := Parse([]byte(`
places:
  - name: traces
    type: tempo
    url: https://tempo.example.com
`))
	require.NoError(t, err)

	place, ok := byName(cfg, "traces")
	require.True(t, ok)
	_, ready, err := place.Stream()
	require.False(t, ready)
	require.ErrorContains(t, err, "reads traces, not logs")
	require.ErrorContains(t, err, "telescope trace --from traces")
}

func TestATraceStoreRefusesWhatItCannotRead(t *testing.T) {
	_, err := Parse([]byte(`
places:
  - name: traces
    type: tempo
    url: https://tempo.example.com
    target: something
`))
	require.ErrorContains(t, err, "target means nothing to it")

	_, err = Parse([]byte(`
places:
  - name: traces
    type: tempo
`))
	require.ErrorContains(t, err, "requires a url")
}

// TestNamingSomethingThatIsNotAStore: the two ways to get the link wrong are
// naming nothing and naming a place that reads lines.
func TestNamingSomethingThatIsNotAStore(t *testing.T) {
	_, err := Parse([]byte(`
places:
  - name: api
    type: loki
    url: https://loki.example.com
    traces: nowhere
`))
	require.ErrorContains(t, err, `names undeclared place "nowhere"`)

	_, err = Parse([]byte(`
places:
  - name: other
    type: loki
    url: https://loki.example.com
  - name: api
    type: loki
    url: https://loki.example.com
    traces: other
`))
	require.ErrorContains(t, err, "reads logs")
}

// TestAStoreSaysOneThing: a link and a store written out in the same breath is
// two answers to one question.
func TestAStoreSaysOneThing(t *testing.T) {
	_, err := Parse([]byte(`
places:
  - name: traces
    type: tempo
    url: https://tempo.example.com
  - name: api
    type: loki
    url: https://loki.example.com
    traces:
      place: traces
      url: https://elsewhere.example.com
`))
	require.ErrorContains(t, err, "the place it names says the rest")
}

func byName(c Config, name string) (Place, bool) {
	for _, p := range c.Places {
		if p.Name == name {
			return p, true
		}
	}
	return Place{}, false
}
