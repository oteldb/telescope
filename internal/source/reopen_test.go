package source

import (
	"context"
	"io"
	"os/exec"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeCollector makes each opening of a collector run a shell script chosen by
// how many openings there have been, so a stream that ends and is opened again
// can be read without a cluster to restart it.
func fakeCollector(t *testing.T, script func(attempt int) string) (*atomic.Int64, *atomic.Pointer[Config]) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake collector is a shell script")
	}

	var (
		opened atomic.Int64
		last   atomic.Pointer[Config]
		prev   = spawnCommand
		pace   = reopenBase
	)
	reopenBase, spawnCommand = time.Millisecond, func(
		ctx context.Context, cfg Config,
	) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
		n := int(opened.Add(1)) - 1
		last.Store(&cfg)

		cmd := exec.CommandContext(ctx, "sh", "-c", script(n))
		stdout, err := cmd.StdoutPipe()
		require.NoError(t, err)
		stderr, err := cmd.StderrPipe()
		require.NoError(t, err)
		return cmd, stdout, stderr, cmd.Start()
	}
	t.Cleanup(func() { spawnCommand, reopenBase = prev, pace })
	return &opened, &last
}

func kubeFollow() Config {
	return Config{Collector: CollectorKubectl, Target: "api-7d9f", Follow: true, Stamp: true}
}

// TestAContainerComingBackDoesNotEndTheStream: "kubectl logs -f" ends when the
// container does, and a view that took that for the end would go dead beside a
// pod that is running.
func TestAContainerComingBackDoesNotEndTheStream(t *testing.T) {
	opened, last := fakeCollector(t, func(attempt int) string {
		switch attempt {
		case 0:
			return `echo '2026-08-14T09:00:00Z before the restart'`
		case 1:
			return `echo '2026-08-14T09:05:00Z after it'`
		default:
			return `exit 0`
		}
	})

	s, err := Start(t.Context(), kubeFollow())
	require.NoError(t, err)

	var got []string
	for l := range s.Lines() {
		got = append(got, string(l.Data))
	}
	require.NoError(t, <-s.Done())
	require.Equal(t, []string{"before the restart", "after it"}, got,
		"both sides of the restart, as one stream")
	require.EqualValues(t, 2+maxReopen, opened.Load(),
		"and it stopped asking once there was nothing more to read")

	// Reading resumes where it got to, so the lines already held are not read
	// again from the top.
	require.Equal(t, time.Date(2026, 8, 14, 9, 5, 0, 0, time.UTC), last.Load().Range.Since.UTC())
	require.Zero(t, last.Load().Tail, "and the tail was how far back to start, which is behind us")
}

// TestAPlaceThatOnlyComplainsIsNotReopenedForever: a target the cluster does not
// have writes its refusal on stderr every time round.
func TestAPlaceThatOnlyComplainsIsNotReopenedForever(t *testing.T) {
	opened, _ := fakeCollector(t, func(int) string {
		return `echo 'Error from server (NotFound): pods "api-7d9f" not found' >&2; exit 1`
	})

	s, err := Start(t.Context(), kubeFollow())
	require.NoError(t, err)

	var said int
	for l := range s.Lines() {
		if l.Stderr {
			said++
		}
	}
	require.Error(t, <-s.Done(), "and the reader is told why in the end")
	require.EqualValues(t, maxReopen, opened.Load())
	require.Equal(t, maxReopen, said, "each attempt said its piece")
}

// TestACommandThatWasAskedToRunOnceRunsOnce: only kubectl ends before what it
// reads does.
func TestACommandThatWasAskedToRunOnceRunsOnce(t *testing.T) {
	opened, _ := fakeCollector(t, func(int) string { return `echo hi` })

	s, err := Start(t.Context(), Config{
		Collector: CollectorCommand,
		Args:      "true",
		Follow:    true,
	})
	require.NoError(t, err)
	for range s.Lines() {
	}
	require.NoError(t, <-s.Done())
	require.EqualValues(t, 1, opened.Load())
}

// TestReadingPicksUpWhereItGotTo: what a resumed config asks for.
func TestReadingPicksUpWhereItGotTo(t *testing.T) {
	at := time.Date(2026, 8, 14, 9, 5, 0, 0, time.UTC)
	cfg := kubeFollow()
	cfg.Tail = 500
	cfg.Range = Range{Since: at.Add(-time.Hour), Until: at.Add(time.Hour)}

	got := cfg.resume(at)
	require.Equal(t, at, got.Range.Since)
	require.True(t, got.Range.Until.IsZero(), "the window's far end is behind us")
	require.Zero(t, got.Tail)

	require.Equal(t, cfg, cfg.resume(time.Time{}),
		"a place not stamping its lines opens again as it opened")
}
