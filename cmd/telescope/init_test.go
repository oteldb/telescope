package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

// provisioning writes the files a Grafana is configured from, which is the one
// source init can read without a network or a tool installed.
func provisioning(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "logs.yaml"), []byte(
		"apiVersion: 1\n"+
			"datasources:\n"+
			"  - name: logs\n"+
			"    type: loki\n"+
			"    url: http://127.0.0.1:3100\n"), 0o600))
	return dir
}

// run executes telescope with the machine left out of it: a test that probed
// would offer whatever the developer happens to be running.
func run(t *testing.T, out *bytes.Buffer, args ...string) error {
	t.Helper()
	cmd := root()
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{
		"init", "--probe=false", "--yes",
		"--grafana-provisioning", provisioning(t),
	}, args...))
	return cmd.Execute()
}

func TestInitWritesAConfigThatLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	require.NoError(t, run(t, &bytes.Buffer{}, "--config", path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	cfg, err := config.Parse(data)
	require.NoError(t, err)
	require.Equal(t, "logs", cfg.Places[0].Name)
	require.Equal(t, "http://127.0.0.1:3100", cfg.Places[0].URL)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a config may name a token file, so it is nobody else's to read")
}

// TestInitWillNotReplaceAConfigItWasNotAskedTo: a config is somebody's own
// work, and init has no idea what is in it.
func TestInitWillNotReplaceAConfigItWasNotAskedTo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("places: []\n"), 0o600))

	err := run(t, &bytes.Buffer{}, "--config", path)
	require.ErrorContains(t, err, "already there")

	require.NoError(t, run(t, &bytes.Buffer{}, "--config", path, "--force"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "name: logs")
}

// TestInitPrintsWhatItWouldWrite: the questions go to stderr and the file to
// stdout, so redirecting one does not swallow the other.
func TestInitPrintsWhatItWouldWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer
	require.NoError(t, run(t, &out, "--config", path, "--print"))

	_, err := config.Parse(out.Bytes())
	require.NoError(t, err)
	require.NoFileExists(t, path)
}

// TestInitRefusesASecretWrittenOutRatherThanNamed: a token on the command line
// is one in the shell history, and then one in the config file.
func TestInitRefusesASecretWrittenOutRatherThanNamed(t *testing.T) {
	err := run(t, &bytes.Buffer{},
		"--config", filepath.Join(t.TempDir(), "config.yaml"),
		"--grafana", "https://grafana.example.com",
		"--grafana-token", "glsa_deadbeef")
	require.ErrorContains(t, err, "names no source")
}
