package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// A url on its own is what `traces:` meant before it could say what answers
// there, and it still means the Tempo it meant then.
func TestATraceStoreWrittenAsAURLIsTempo(t *testing.T) {
	path := write(t, `
places:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    traces: https://tempo.example.com/
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)

	at, ok, err := cfg.Places[0].TraceEndpoint()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "https://tempo.example.com", at.URL, "the trailing slash is not part of a path")
	require.Equal(t, source.CollectorTempo, at.Collector)
}

// A store says which API answers there for the reason a place says whether it
// is Loki or VictoriaLogs: the paths, the query and what comes back all differ.
func TestATraceStoreSaysWhichAPIAnswers(t *testing.T) {
	path := write(t, `
places:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    tenant: "1:2"
    traces:
      url: https://victoria.example.com/select/jaeger
      type: jaeger
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)

	at, ok, err := cfg.Places[0].TraceEndpoint()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "https://victoria.example.com/select/jaeger", at.URL)
	require.Equal(t, source.CollectorJaeger, at.Collector)
	require.Equal(t, "1:2", at.Tenant, "a system's traces sit behind the same door as its logs")
}

func TestATraceStoreIsRefusedIfItNamesNoAPITelescopeReads(t *testing.T) {
	path := write(t, `
places:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    traces:
      url: https://traces.example.com
      type: zipkin
`)
	// A place that is not usable as declared is a mistake in the file, and the
	// file is refused for it the way an unknown `type:` is.
	_, err := loadFrom(path)
	require.ErrorContains(t, err, "unknown traces type")
	require.ErrorContains(t, err, "tempo, jaeger")
	require.ErrorContains(t, err, "prod", "the broken place is named")
}

// A place that reads no traces says nothing about them, which is not an error:
// most places have none.
func TestAPlaceWithNoTraceStore(t *testing.T) {
	path := write(t, `
places:
  - name: prod
    type: docker
    container: api
`)
	cfg, err := loadFrom(path)
	require.NoError(t, err)

	_, ok, err := cfg.Places[0].TraceEndpoint()
	require.NoError(t, err)
	require.False(t, ok)
	require.True(t, cfg.Places[0].Traces.IsZero())
}
