package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

func testConfig(t *testing.T, places []config.Place, groups []config.Group) config.Config {
	t.Helper()
	cfg, err := config.New(places, groups)
	require.NoError(t, err)
	return cfg
}

func callPlaces(t *testing.T, cfg config.Config) placesOutput {
	t.Helper()
	_, out, err := placesHandler(cfg)(t.Context(), nil, placesInput{})
	require.NoError(t, err)
	return out
}

// TestPlacesReportsWhatEachPlaceHolds: an agent picks a place by what it reads
// and how it is reached, and both are what the file said rather than what a
// screen would draw.
func TestPlacesReportsWhatEachPlaceHolds(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "node", Type: "journalctl", Unit: "nginx", Via: "ssh://ops@node-1"},
		{
			Name:   "prod",
			Type:   "victorialogs",
			URL:    "https://logs.example.com",
			Target: "app:api",
			Traces: config.TraceStore{URL: "https://tempo.example.com"},
		},
	}, nil)

	out := callPlaces(t, cfg)
	require.Len(t, out.Places, 2)
	require.Empty(t, out.Groups)

	node := out.Places[0]
	require.Equal(t, "node", node.Name)
	require.Equal(t, []string{signalLogs}, node.Reads)
	require.Equal(t, "ssh://ops@node-1", node.Via)
	require.Equal(t, "nginx", node.Target)
	require.True(t, node.Ready)
	require.Empty(t, node.URL)

	prod := out.Places[1]
	require.Equal(t, []string{signalLogs, signalTraces}, prod.Reads)
	require.Equal(t, "https://logs.example.com", prod.URL)
	require.Equal(t, &store{URL: "https://tempo.example.com", Type: "tempo"}, prod.Traces,
		"a url says nothing about which of the two APIs answers at it")
	require.Equal(t, "app:api", prod.Target)
	require.Empty(t, prod.Via, "a database is dialed rather than entered")
	require.True(t, prod.Ready)
}

// TestPlacesSaysWhatIsMissing: a place that pins a cluster and leaves the pod
// open is not broken, and an agent told only that it is not ready would have
// nothing to ask for.
func TestPlacesSaysWhatIsMissing(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "staging", Type: "kubectl", Namespace: "api"},
	}, nil)

	place := callPlaces(t, cfg).Places[0]
	require.False(t, place.Ready)
	require.Contains(t, place.Needs, "kubectl requires a pod")
	require.Empty(t, place.Error)
}

// TestPlacesReportsAGroupAsOne: a group is one timeline and is named the way a
// place is, so the tools take either without being told which it was.
func TestPlacesReportsAGroupAsOne(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "eu", Type: "victorialogs", URL: "https://eu.example.com"},
		{Name: "us", Type: "victorialogs", URL: "https://us.example.com"},
	}, []config.Group{
		{Name: "world", Places: []string{"eu", "us"}, Query: "level>=warn"},
	})

	out := callPlaces(t, cfg)
	require.Len(t, out.Groups, 1)
	group := out.Groups[0]
	require.Equal(t, "world", group.Name)
	require.Equal(t, []string{"eu", "us"}, group.Places)
	require.Equal(t, "level>=warn", group.Query)
	require.True(t, group.Ready)
}

// TestPlacesReportsAGroupThatMustBeAsked: every member reads a container and
// none of them says which, which is one question and not two.
func TestPlacesReportsAGroupThatMustBeAsked(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "left", Type: "docker"},
		{Name: "right", Type: "docker"},
	}, []config.Group{
		{Name: "both", Places: []string{"left", "right"}},
	})

	group := callPlaces(t, cfg).Groups[0]
	require.False(t, group.Ready)
	require.Equal(t, "a target for its docker places", group.Needs)
}

// TestPlacesNamesWhatAnswersAtATraceStore: Tempo and Jaeger answer the same
// question differently, and a place that declared which is not asked again.
func TestPlacesNamesWhatAnswersAtATraceStore(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{
			Name:   "prod",
			Type:   "victorialogs",
			URL:    "https://logs.example.com",
			Traces: config.TraceStore{URL: "https://jaeger.example.com", Type: "jaeger"},
		},
	}, nil)

	place := callPlaces(t, cfg).Places[0]
	require.Equal(t, []string{signalLogs, signalTraces}, place.Reads)
	require.Equal(t, &store{URL: "https://jaeger.example.com", Type: "jaeger"}, place.Traces)
}
