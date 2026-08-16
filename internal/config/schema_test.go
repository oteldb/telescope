package config

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var updateSchema = flag.Bool("update-schema", false, "rewrite config.schema.json from the descriptor")

// schemaPath is where the generated schema is committed, so that the URL a
// config file points at is a file in this repository rather than a release.
func schemaPath() string { return filepath.Join("..", "..", "config.schema.json") }

// TestSchemaIsCommitted: the published schema is what the descriptor says
// today. It is committed rather than served, so nothing but this test notices
// when a key gains a meaning the schema still describes the old way.
func TestSchemaIsCommitted(t *testing.T) {
	got, err := Schema()
	require.NoError(t, err)

	if *updateSchema {
		require.NoError(t, os.WriteFile(schemaPath(), got, 0o644)) //nolint:gosec // G306: a published schema is world-readable
		return
	}

	want, err := os.ReadFile(schemaPath())
	require.NoError(t, err)
	// A checkout on Windows rewrites the line endings of a text file, and what
	// this compares is the document rather than the bytes it was checked out as.
	require.Equal(t, lines(want), lines(got),
		"run go test ./internal/config -update-schema")
}

func lines(data []byte) string { return strings.ReplaceAll(string(data), "\r\n", "\n") }

// TestSchemaRequiresOnlyWhatAPlaceMustName: a place needs a name and a type and
// nothing else, so neither its token nor its trace store may be marked
// required — every key inside either is optional, and a schema that demanded
// them would flag every working file.
func TestSchemaRequiresOnlyWhatAPlaceMustName(t *testing.T) {
	data, err := Schema()
	require.NoError(t, err)

	var doc struct {
		Properties struct {
			Places struct {
				Items struct {
					Required   []string       `json:"required"`
					Properties map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"places"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	require.Equal(t, []string{"name", "type"}, doc.Properties.Places.Items.Required)
	require.Contains(t, doc.Properties.Places.Items.Properties, "traces",
		"which is offered, not demanded")
}
