package source

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func collect(t *testing.T, cfg Config) (stdout, stderr []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sources are executed through sh")
	}

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)

	for l := range s.Lines() {
		if l.Stderr {
			stderr = append(stderr, string(l.Data))
			continue
		}
		stdout = append(stdout, string(l.Data))
	}
	return stdout, stderr
}

func TestStream(t *testing.T) {
	out, errOut := collect(t, Config{
		Collector: CollectorCommand,
		Args:      "printf 'a\\nb\\n'; printf 'oops\\n' >&2",
	})
	require.Equal(t, []string{"a", "b"}, out)
	require.Equal(t, []string{"oops"}, errOut)
	require.Empty(t, errOut[0:0])
}

func TestStreamExitError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sources are executed through sh")
	}
	s, err := Start(t.Context(), Config{Collector: CollectorCommand, Args: "exit 3"})
	require.NoError(t, err)
	t.Cleanup(s.Close)

	for range s.Lines() {
	}
	require.Error(t, <-s.Done())
}

func TestStreamCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sources are executed through sh")
	}
	ctx, cancel := context.WithCancel(t.Context())
	s, err := Start(ctx, Config{Collector: CollectorCommand, Args: "printf 'x\\n'; sleep 60"})
	require.NoError(t, err)

	require.Equal(t, "x", string((<-s.Lines()).Data))
	cancel()

	for range s.Lines() {
	}
	// A canceled stream reports no error: the exit is ours, not the command's.
	require.NoError(t, <-s.Done())
}

func TestStreamStartError(t *testing.T) {
	_, err := Start(t.Context(), Config{Collector: CollectorDocker})
	require.Error(t, err)
}
