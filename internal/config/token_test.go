package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// needsShell skips where there is no sh to run a token command with.
func needsShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell to run a token command with")
	}
}

func TestTokenRead(t *testing.T) {
	t.Setenv("TELESCOPE_TEST_TOKEN", "  s3cret\n")

	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte("s3cret\n"), 0o600))

	for _, tt := range []struct {
		name  string
		token Token
	}{
		{"env", Token{Env: "TELESCOPE_TEST_TOKEN"}},
		{"file", Token{File: file}},
		{"exec", Token{Exec: Argv{"sh", "-c", "printf 's3cret\\n'"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.token.Exec != nil {
				needsShell(t)
			}
			got, err := tt.token.Read(t.Context())
			require.NoError(t, err)
			require.Equal(t, "s3cret", got, "surrounding whitespace is not part of the token")
		})
	}

	t.Run("nothing named", func(t *testing.T) {
		got, err := Token{}.Read(t.Context())
		require.NoError(t, err, "an endpoint may need no credentials at all")
		require.Empty(t, got)
	})
}

// TestTokenExecErrors: a password manager explains itself on stderr, and that
// is the only clue to why nothing came back.
func TestTokenExecErrors(t *testing.T) {
	needsShell(t)
	for _, tt := range []struct {
		name    string
		exec    Argv
		wantErr string
	}{
		{"stderr is kept", Argv{"sh", "-c", "echo 'entry not found' >&2; exit 1"}, "entry not found"},
		{"no output", Argv{"sh", "-c", "exit 0"}, "printed no token"},
		{"no such command", Argv{"telescope-no-such-command"}, "telescope-no-such-command"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.exec.run(t.Context())
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestTokenExecTakesTheFirstLine: a manager that prints the secret with notes
// after it is the common case.
func TestTokenExecTakesTheFirstLine(t *testing.T) {
	needsShell(t)
	got, err := Argv{"sh", "-c", "printf 's3cret\\nurl: https://example.com\\n'"}.run(t.Context())
	require.NoError(t, err)
	require.Equal(t, "s3cret", got)
}

// TestTokenExecForms: a command line goes through a shell, since that is what
// it was written for; a list is its own arguments and needs no quoting.
func TestTokenExecForms(t *testing.T) {
	needsShell(t)
	cfg, err := loadFrom(write(t, `
endpoints:
  - name: shell
    type: victorialogs
    url: https://logs.example.com
    token:
      exec: printf 'sh3ll' | tr -d ' '
  - name: argv
    type: loki
    url: https://logs.example.com
    token:
      exec: ["sh", "-c", "printf 'argv token' | cut -d' ' -f1"]
sources: []
`))
	require.NoError(t, err)

	endpoints, err := cfg.Resolved()
	require.NoError(t, err)
	require.Len(t, endpoints, 2)
	require.Equal(t, "sh3ll", endpoints[0].Token)
	require.Equal(t, "argv", endpoints[1].Token)
}

// TestTokenExecRunsOnce: reading a secret may cost a keyring prompt, so it must
// not happen again for every part of the screen that wants it.
func TestTokenExecRunsOnce(t *testing.T) {
	needsShell(t)
	counter := filepath.Join(t.TempDir(), "runs")
	cfg, err := loadFrom(write(t, `
endpoints:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      exec: echo run >> `+counter+`; printf s3cret
sources:
  - name: prod
    endpoint: prod
    target: error
`))
	require.NoError(t, err)

	for range 3 {
		endpoints, err := cfg.Resolved()
		require.NoError(t, err)
		require.Equal(t, "s3cret", endpoints[0].Token)
	}
	stream, _, err := cfg.Sources[0].Stream()
	require.NoError(t, err)
	require.Equal(t, "s3cret", stream.Endpoint.Token)

	runs, err := os.ReadFile(counter)
	require.NoError(t, err)
	require.Equal(t, "run\n", string(runs))
}

// TestTokenExecFailureIsPerEndpoint: a keyring that will not answer is one
// endpoint that cannot be reached, not a config that cannot be read.
func TestTokenExecFailureIsPerEndpoint(t *testing.T) {
	needsShell(t)
	cfg, err := loadFrom(write(t, `
endpoints:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      exec: echo 'the keyring is locked' >&2; exit 1
sources:
  - name: prod logs
    endpoint: prod
    target: error
  - name: local docker
    collector: docker
    container: api
`))
	require.NoError(t, err, "one unreachable secret is not a broken config")

	_, ready, err := cfg.Sources[0].Stream()
	require.False(t, ready)
	require.ErrorContains(t, err, "the keyring is locked")

	_, ready, err = cfg.Sources[1].Stream()
	require.NoError(t, err)
	require.True(t, ready)
}

func TestTokenValidate(t *testing.T) {
	require.NoError(t, Token{}.Validate())
	require.NoError(t, Token{Env: "A"}.Validate())
	require.ErrorContains(t, Token{Env: "A", File: "/b"}.Validate(), "env and file")
	require.ErrorContains(t, Token{File: "/b", Exec: Argv{"pass"}}.Validate(), "file and exec")
}

func TestTokenExecEmpty(t *testing.T) {
	for _, content := range []string{
		"endpoints:\n  - name: p\n    type: loki\n    url: http://x\n    token:\n      exec: \"\"\nsources: []\n",
		"endpoints:\n  - name: p\n    type: loki\n    url: http://x\n    token:\n      exec: []\nsources: []\n",
	} {
		_, err := loadFrom(write(t, content))
		require.ErrorContains(t, err, "exec is empty")
	}
}
