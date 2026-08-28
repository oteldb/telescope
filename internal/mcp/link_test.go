package mcp

import (
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/view"
)

func callLink(t *testing.T, cfg config.Config, in linkInput) (string, linkOutput, error) {
	t.Helper()
	res, out, err := linkHandler(cfg)(t.Context(), nil, in)
	if err != nil {
		return "", linkOutput{}, err
	}
	return res.Content[0].(*sdk.TextContent).Text, out, nil
}

func linkConfig(t *testing.T) config.Config {
	t.Helper()
	return testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
		{Name: "node", Type: "journalctl", Unit: "nginx"},
		{Name: "prod traces", Type: "tempo", URL: "https://tempo.example.com"},
	}, []config.Group{{Name: "everything", Places: []string{"prod", "node"}}})
}

// TestALinkIsTheCommandThatOpensTheView: a command line runs where it is
// pasted, needs nothing registered with the desktop, and can be read by
// whoever is about to run it.
func TestALinkIsTheCommandThatOpensTheView(t *testing.T) {
	text, out, err := callLink(t, linkConfig(t), linkInput{
		Place: "prod", Query: `level>=error pod=api-1`, Range: "6h..1h",
	})
	require.NoError(t, err)
	require.Equal(t,
		`telescope prod --query 'level>=error pod=api-1' --range 6h..1h`, out.Link)
	require.Equal(t, "logs", out.Opens)
	require.Contains(t, text, out.Link)
}

// TestALinkQuotesWhatAShellWouldEat: a place is named by a person and a filter
// is typed by one, so both hold spaces, and a filter holds quotes and globs
// besides.
func TestALinkQuotesWhatAShellWouldEat(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "humo observer", Type: "victorialogs", URL: "https://logs.example.com"},
	}, nil)

	_, out, err := callLink(t, cfg, linkInput{
		Place: "humo observer", Query: `"connection refused" pod=api-*`,
	})
	require.NoError(t, err)
	require.Equal(t,
		`telescope 'humo observer' --query '"connection refused" pod=api-*'`, out.Link)
}

// TestALinkToAGroupIsALinkLikeAnyOther: whoever wrote it should not have had to
// know whether the name was a place or a group.
func TestALinkToAGroupIsALinkLikeAnyOther(t *testing.T) {
	_, out, err := callLink(t, linkConfig(t), linkInput{Place: "everything"})
	require.NoError(t, err)
	require.Equal(t, "telescope everything", out.Link)
	require.Contains(t, out.Note, "opens on the place's own and follows")
}

// TestALinkToATraceOpensTheTraceCommand: two of the three views already had a
// command line, and a link is that command line.
func TestALinkToATraceOpensTheTraceCommand(t *testing.T) {
	_, out, err := callLink(t, linkConfig(t), linkInput{
		Place: "prod traces", Trace: "4bf92f3577b34da6a3ce929d0e0e4736",
	})
	require.NoError(t, err)
	require.Equal(t,
		`telescope trace --from 'prod traces' 4bf92f3577b34da6a3ce929d0e0e4736`, out.Link)
	require.Equal(t, "trace", out.Opens)
}

// TestAStoreWithNoTraceOpensItsSearch: naming a store and no id is somebody who
// does not know the id yet, which is the ordinary case.
func TestAStoreWithNoTraceOpensItsSearch(t *testing.T) {
	_, out, err := callLink(t, linkConfig(t), linkInput{Place: "prod traces"})
	require.NoError(t, err)
	require.Equal(t, `telescope trace --from 'prod traces'`, out.Link)
	require.Equal(t, "search", out.Opens)
}

// TestATraceLinkSaysWhatItDroppedOnTheFloor: a view opened without them would
// otherwise look like the link having been written wrong.
func TestATraceLinkSaysWhatItDroppedOnTheFloor(t *testing.T) {
	_, out, err := callLink(t, linkConfig(t), linkInput{
		Place: "prod traces", Trace: "4bf92f35", Query: "level>=error", Range: "6h",
	})
	require.NoError(t, err)
	require.NotContains(t, out.Link, "--query")
	require.Contains(t, out.Note, "carries its own interval")
}

// TestALinkThatCannotBeOpenedIsRefusedHere: the person running it did not write
// it, and has no way to tell a name that was wrong from a window that was quiet.
func TestALinkThatCannotBeOpenedIsRefusedHere(t *testing.T) {
	cfg := linkConfig(t)

	_, _, err := callLink(t, cfg, linkInput{Place: "prd"})
	require.ErrorContains(t, err, `no place named "prd"`)

	_, _, err = callLink(t, cfg, linkInput{Place: "prod", Query: "level>="})
	require.ErrorContains(t, err, "query")

	_, _, err = callLink(t, cfg, linkInput{Place: "prod", Range: "sometime"})
	require.ErrorContains(t, err, "range")

	_, _, err = callLink(t, cfg, linkInput{})
	require.ErrorContains(t, err, "name a place to open")
}

// TestALinkParsesBackThroughTheCommandItNames: a link is only worth what it
// opens, so the flags it writes are checked against the ones the command binds
// rather than against themselves.
func TestALinkParsesBackThroughTheCommandItNames(t *testing.T) {
	_, out, err := callLink(t, linkConfig(t), linkInput{
		Place: "prod", Query: `"connection refused" pod=api-*`, Range: "6h..1h",
	})
	require.NoError(t, err)

	// The argv a shell would hand the binary, which the test for the quoting
	// itself checks against a real sh.
	argv := []string{"prod", "--query", `"connection refused" pod=api-*`, "--range", "6h..1h"}
	require.Equal(t, out.Link, viewOf(argv).Link(), "the same view, written the same way")
}

func viewOf(argv []string) view.View {
	v := view.View{Place: argv[0]}
	for i := 1; i+1 < len(argv); i += 2 {
		switch argv[i] {
		case "--" + view.FlagQuery:
			v.Query = argv[i+1]
		case "--" + view.FlagRange:
			v.Range = argv[i+1]
		}
	}
	return v
}
