package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/trace"
)

// configHome points config.Path at dir, so a test never reads the developer's
// own config: what a name resolves to is whatever the test wrote. Both variables
// are set because os.UserConfigDir reads XDG_CONFIG_HOME on unix and %AppData%
// on Windows.
func configHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
}

// A file has no content type, so which format it holds is worked out from what
// comes out of it.
func TestATraceIsReadWhicheverFormatItIsIn(t *testing.T) {
	for _, name := range []string{"checkout.json", "otlp-hex.json", "otlp-tempo.json"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "internal", "trace", "testdata", name))
			require.NoError(t, err)

			tree, err := trace.Decode(data)
			require.NoError(t, err)
			require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", tree.ID)
			require.Equal(t, 6, tree.Len())
		})
	}
}

func TestBytesThatAreNoTraceAreReported(t *testing.T) {
	_, err := trace.Decode([]byte(`<html>502 Bad Gateway</html>`))
	require.Error(t, err)
}

// A url is itself; anything else has to be declared, and saying so is more use
// than a connection refused to whatever the name resolved to.
func TestFromTakesAUrlOrTheNameOfAPlace(t *testing.T) {
	configHome(t, t.TempDir())

	e, err := traceEndpoint("https://tempo.example.com/")
	require.NoError(t, err)
	require.Equal(t, "https://tempo.example.com", e.URL, "and loses the trailing slash")

	_, err = traceEndpoint("not-a-place")
	require.ErrorContains(t, err, "not a url")
}

// A place declares where its traces are read from beside where its logs are,
// since they are rarely the same server even when they are the same system.
func TestAPlaceLendsItsTraceEndpointItsWayIn(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "telescope"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "telescope", "config.yaml"), []byte(
		"places:\n"+
			"  - name: prod\n"+
			"    type: victorialogs\n"+
			"    url: https://logs.example.com\n"+
			"    tenant: \"1:2\"\n"+
			"    traces: https://tempo.example.com\n"+
			"  - name: nearby\n"+
			"    type: journalctl\n"), 0o644))
	configHome(t, dir)

	e, err := traceEndpoint("prod")
	require.NoError(t, err)
	require.Equal(t, "https://tempo.example.com", e.URL)
	require.Equal(t, "1:2", e.Tenant, "and the way in the logs already had")

	_, err = traceEndpoint("nearby")
	require.ErrorContains(t, err, "reads no traces")

	_, err = traceEndpoint("missing")
	require.ErrorContains(t, err, "prod", "the ones that do are worth naming")
}
