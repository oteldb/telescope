package mcp

import (
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
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

// TestPlacesReportsAStoreAsAPlace: a trace store is a place with a name the
// others point at, so it is in the same list and says which signal it holds.
func TestPlacesReportsAStoreAsAPlace(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: "https://tempo.example.com"},
		{
			Name:   "api",
			Type:   "victorialogs",
			URL:    "https://logs.example.com",
			Traces: config.TraceStore{Name: "prod traces"},
		},
	}, nil)

	out := callPlaces(t, cfg)
	require.Len(t, out.Places, 2)

	tempo := out.Places[0]
	require.Equal(t, []string{signalTraces}, tempo.Reads)
	require.Equal(t, "tempo", tempo.Type)
	require.Equal(t, "https://tempo.example.com", tempo.URL)
	require.True(t, tempo.Ready, "a store that holds no lines is not a broken place")
	require.Empty(t, tempo.Error)
	require.Nil(t, tempo.Traces, "it is the store rather than naming one")

	api := out.Places[1]
	require.Equal(t, []string{signalLogs, signalTraces}, api.Reads)
	require.Equal(t, &store{Place: "prod traces", URL: "https://tempo.example.com", Type: "tempo"},
		api.Traces, "the link is by the name the tools take, with where it points beside it")
}

// TestPlacesIsNotItsOwnStructuredContentTwice: a tool that leaves the text to
// the SDK gets the JSON of its facts as the text block, which is the same bytes
// said again under another key. places is the tool every session calls first,
// so it is the one worth not paying double for.
func TestPlacesIsNotItsOwnStructuredContentTwice(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "node", Type: "journalctl", Unit: "nginx", Via: "ssh://ops@node-1"},
		{
			Name: "prod", Type: "victorialogs", URL: "https://logs.example.com",
			Target: "app:api", Query: "level>=warn",
			Traces: config.TraceStore{Name: "prod traces"},
		},
		{Name: "prod traces", Type: "tempo", URL: "https://tempo.example.com"},
	}, []config.Group{{Name: "everything", Places: []string{"node", "prod"}}})

	res, out, err := placesHandler(cfg)(t.Context(), nil, placesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, out.Places, "the facts are still answered")

	text := res.Content[0].(*sdk.TextContent).Text
	require.NotContains(t, text, `{"places"`, "and the text is not those facts serialized")
	require.NotContains(t, text, `"name":`)

	require.Contains(t, text, "node         journalctl    reads logs  nginx  via ssh://ops@node-1")
	require.Contains(t, text, "prod         victorialogs  reads logs,traces")
	require.Contains(t, text, "      filtered by level>=warn")
	require.Contains(t, text, "      traces: prod traces — https://tempo.example.com (tempo)",
		"named as the place it is, since that is what the trace tools take")
	require.Contains(t, text, "prod traces  tempo         reads traces")
	require.Contains(t, text, "everything   node + prod")
}

// TestAPlaceThatDoesNotOpenSaysWhatItNeeds: it is not an error
// and not a place to skip, and a column for it would be empty on everything
// that works.
func TestAPlaceThatDoesNotOpenSaysWhatItNeeds(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "checkout", Type: "kubectl"},
	}, nil)

	res, out, err := placesHandler(cfg)(t.Context(), nil, placesInput{})
	require.NoError(t, err)
	require.False(t, out.Places[0].Ready)

	text := res.Content[0].(*sdk.TextContent).Text
	require.Contains(t, text, "needs ")
	require.Contains(t, text, out.Places[0].Needs)
}

// TestNoPlacesSaysWhatToDoAboutIt: an empty list is the one answer a reader
// cannot act on.
func TestNoPlacesSaysWhatToDoAboutIt(t *testing.T) {
	res, _, err := placesHandler(testConfig(t, nil, nil))(t.Context(), nil, placesInput{})
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(*sdk.TextContent).Text, "telescope init")
}
