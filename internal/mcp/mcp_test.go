package mcp

import (
	"encoding/json"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

// connect speaks to a server over a pipe rather than a process, which is the
// same conversation an agent has with one.
func connect(t *testing.T, cfg config.Config) *sdk.ClientSession {
	t.Helper()
	client, server := sdk.NewInMemoryTransports()
	_, err := New(cfg, "test").Connect(t.Context(), server, nil)
	require.NoError(t, err)
	session, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "test"}, nil).
		Connect(t.Context(), client, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestServerOffersItsTools: the schemas are derived from the handlers, so a
// type that cannot be described is a panic at registration and not a tool that
// fails when it is first called.
func TestServerOffersItsTools(t *testing.T) {
	session := connect(t, testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	}, nil))

	list, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)

	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		require.NotEmpty(t, tool.Description, "%s says nothing about itself", tool.Name)
		require.True(t, tool.Annotations.ReadOnlyHint, "%s is not marked read-only", tool.Name)
	}
	require.ElementsMatch(t, []string{
		"places", "fields", "field_values", "logs", "summary",
		"trace", "trace_search", "trace_fields", "trace_tag_values",
	}, names)
}

// TestServerAnswersOverTheWire: what a tool returns has to survive being
// encoded, which is the one thing calling the handler directly does not check.
func TestServerAnswersOverTheWire(t *testing.T) {
	session := connect(t, testConfig(t, []config.Place{
		{Name: "node", Type: "journalctl", Unit: "nginx"},
	}, nil))

	res, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "places"})
	require.NoError(t, err)
	require.False(t, res.IsError)

	out, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	places, ok := out["places"].([]any)
	require.True(t, ok)
	require.Len(t, places, 1)
	require.Equal(t, "node", places[0].(map[string]any)["name"])

	// Both halves reach the caller, and they are not the same half twice: the
	// facts are the structured content and the text is a reading of them.
	require.Len(t, res.Content, 1)
	text := res.Content[0].(*sdk.TextContent).Text
	require.NotEmpty(t, text)
	require.False(t, json.Valid([]byte(text)),
		"the text block is the answer read out, not the answer serialized again")
	require.Contains(t, text, "node")
}

// TestServerReportsAWrongNameToTheCaller: a tool that was asked for a place
// that does not exist has not failed, and the agent is the one that can fix it.
func TestServerReportsAWrongNameToTheCaller(t *testing.T) {
	session := connect(t, testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	}, nil))

	res, err := session.CallTool(t.Context(), &sdk.CallToolParams{
		Name:      "fields",
		Arguments: map[string]any{"place": "prd"},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content[0].(*sdk.TextContent).Text, "the ones declared are prod")
}
