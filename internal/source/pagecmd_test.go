package source

import (
	"context"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// asked is what each opening of a collector was built to run, so a test can read
// the page a tool was actually sent rather than the one it was meant to be.
type asked struct {
	mu   sync.Mutex
	cmds []string
}

func (a *asked) list() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.cmds...)
}

// fakePages runs a shell script in place of the collector, chosen by how many
// openings there have been, and records the command line each one stood in for.
func fakePages(t *testing.T, script func(attempt int) string) *asked {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake collector is a shell script")
	}

	got := &asked{}
	prev := spawnCommand
	spawnCommand = func(ctx context.Context, cfg Config) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
		got.mu.Lock()
		n := len(got.cmds)
		got.cmds = append(got.cmds, cfg.Command())
		got.mu.Unlock()

		cmd := exec.CommandContext(ctx, "sh", "-c", script(n))
		stdout, err := cmd.StdoutPipe()
		require.NoError(t, err)
		stderr, err := cmd.StderrPipe()
		require.NoError(t, err)
		return cmd, stdout, stderr, cmd.Start()
	}
	t.Cleanup(func() { spawnCommand = prev })
	return got
}

// stamped dates a line by the timestamp it starts with, standing in for the
// parser a view brings.
func stamped(l Line) time.Time {
	ts, _, ok := strings.Cut(string(l.Data), " ")
	if !ok {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return at
}

func pageStart(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
}

// TestJournalPagesWithAnUntil: journalctl bounds both ends, so a page is the
// stream's own command run again below the window on the screen.
func TestJournalPagesWithAnUntil(t *testing.T) {
	got := fakePages(t, func(int) string {
		return `printf '%s\n' '2026-08-11T09:59:57Z third from last' ` +
			`'2026-08-11T09:59:58Z second from last' '2026-08-11T09:59:59Z last'`
	})

	cfg := Config{Collector: CollectorJournal, Unit: "api.service", Follow: true, Tail: 500}
	lines, err := cfg.Page(t.Context(), pageStart(t), 3, WithTimeFunc(stamped))
	require.NoError(t, err)
	require.Equal(t, []string{
		"2026-08-11T09:59:57Z third from last",
		"2026-08-11T09:59:58Z second from last",
		"2026-08-11T09:59:59Z last",
	}, texts(lines), "oldest first, the way the stream reads")

	cmd := got.list()
	require.Len(t, cmd, 1, "one window, asked for once")
	require.Contains(t, cmd[0], "-u api.service", "the same unit the stream reads")
	require.Contains(t, cmd[0], "-n 3", "and no more than a page of it")
	require.NotContains(t, cmd[0], " -f", "a window that has already happened is not followed")

	require.Equal(t, pageStart(t).In(time.Local).Format(journalStamp), untilOf(t, cmd[0]),
		"an instant already on a second is the second the reader is holding")
}

// TestJournalPageDropsWhatTheReaderHolds: journalctl reads --until to the second
// and inclusively, so the bound is rounded up and the overlap dropped here —
// a duplicate second the list folds beats a hole nobody can find later.
func TestJournalPageDropsWhatTheReaderHolds(t *testing.T) {
	got := fakePages(t, func(int) string {
		return `printf '%s\n' '2026-08-11T09:59:59.5Z below' ` +
			`'2026-08-11T10:00:00.7Z already on the screen'`
	})

	before := pageStart(t).Add(500 * time.Millisecond)
	lines, err := Config{Collector: CollectorJournal, Unit: "api.service"}.
		Page(t.Context(), before, 10, WithTimeFunc(stamped))
	require.NoError(t, err)
	require.Equal(t, []string{"2026-08-11T09:59:59.5Z below"}, texts(lines))
	require.Equal(t, pageStart(t).Add(time.Second).In(time.Local).Format(journalStamp),
		untilOf(t, got.list()[0]), "asked for the whole of the second the reader is inside")
}

// TestDockerPagesToTheNanosecond: a page ends just short of the line the reader
// is holding, and a bound rounded down to the second would fall below lines it
// must not lose.
func TestDockerPagesToTheNanosecond(t *testing.T) {
	got := fakePages(t, func(int) string {
		return `printf '%s\n' '2026-08-11T09:59:59.25Z earlier' '2026-08-11T09:59:59.75Z later'`
	})

	before := pageStart(t).Add(500 * time.Millisecond)
	cfg := Config{Collector: CollectorDocker, Container: "app", Follow: true}
	lines, err := cfg.Page(t.Context(), before, 4)
	require.NoError(t, err)
	require.Equal(t, []string{"earlier", "later"}, texts(lines),
		"docker stamps its own lines, so the time comes off them")
	require.Equal(t, pageStart(t).Add(-250*time.Millisecond), lines[1].At.UTC())

	cmd := got.list()
	require.Len(t, cmd, 1)
	require.Contains(t, cmd[0], "--timestamps")
	require.Contains(t, cmd[0], "--tail 4")
	require.Contains(t, cmd[0], "--until "+before.Add(-time.Nanosecond).Format(time.RFC3339Nano))
}

// untilOf reads back the --until a command was given, quotes and all.
func untilOf(t *testing.T, cmd string) string {
	t.Helper()
	_, rest, ok := strings.Cut(cmd, "--until ")
	require.True(t, ok, cmd)
	if quoted, ok := strings.CutPrefix(rest, "'"); ok {
		until, _, ok := strings.Cut(quoted, "'")
		require.True(t, ok, cmd)
		return until
	}
	until, _, _ := strings.Cut(rest, " ")
	return until
}

func kubePlace() Config {
	return Config{
		Collector: CollectorKubectl,
		Namespace: "checkout",
		Target:    "deploy/api",
		Transport: TransportSSH,
		Host:      "bastion",
		Elevate:   true,
		Follow:    true,
		Tail:      200,
	}
}

// TestKubectlPageStopsAtTheLineAlreadyHeld: kubectl takes no --until, so the
// window is closed by the reading rather than by the tool.
func TestKubectlPageStopsAtTheLineAlreadyHeld(t *testing.T) {
	got := fakePages(t, func(int) string {
		return `printf '%s\n' '2026-08-11T09:59:58Z below' '2026-08-11T09:59:59Z also below' ` +
			`'2026-08-11T10:00:00Z on the screen' '2026-08-11T10:00:01Z on the screen too'`
	})

	lines, err := kubePlace().Page(t.Context(), pageStart(t), 2)
	require.NoError(t, err)
	require.Equal(t, []string{"below", "also below"}, texts(lines),
		"nothing the reader is already holding comes back")

	cmd := got.list()
	require.Len(t, cmd, 1, "a window with something in it is not widened")
	require.Contains(t, cmd[0], "--tail -1",
		"a selector kubectl was not told about would default to ten")
	require.Contains(t, cmd[0], "--since-time="+pageStart(t).Add(-kubePageStep).Format(time.RFC3339))
	require.NotContains(t, cmd[0], " -f")
	require.Contains(t, cmd[0], "sudo -n kubectl", "read the way the stream is read")
}

// TestKubectlPageWidensAnEmptyWindow: a pod quiet for an hour is quiet, not
// finished, so an empty window is widened rather than answered.
func TestKubectlPageWidensAnEmptyWindow(t *testing.T) {
	got := fakePages(t, func(attempt int) string {
		if attempt < 2 {
			return `exit 0`
		}
		return `printf '%s\n' '2026-08-11T09:00:00Z the one line it wrote'`
	})

	lines, err := kubePlace().Page(t.Context(), pageStart(t), 50)
	require.NoError(t, err)
	require.Equal(t, []string{"the one line it wrote"}, texts(lines))

	cmd := got.list()
	require.Len(t, cmd, 3)
	require.Contains(t, cmd[0], "--since-time="+pageStart(t).Add(-kubePageStep).Format(time.RFC3339))
	require.Contains(t, cmd[1], "--since-time="+
		pageStart(t).Add(-kubePageStep-kubePageStep*kubePageGrow).Format(time.RFC3339),
		"each window reaches further back and re-reads the one before it")
}

// TestKubectlPageEndsWithTheWholeLog: the widest window has no start bound, so
// the emptiness that ends the walk is the log's and not the window's.
func TestKubectlPageEndsWithTheWholeLog(t *testing.T) {
	got := fakePages(t, func(int) string { return `exit 0` })

	lines, err := kubePlace().Page(t.Context(), pageStart(t), 50)
	require.NoError(t, err)
	require.Empty(t, lines, "and nothing is not a failure")

	cmd := got.list()
	require.NotContains(t, cmd[len(cmd)-1], "--since-time",
		"the last one reads the log from its beginning")
	for _, c := range cmd[:len(cmd)-1] {
		require.Contains(t, c, "--since-time")
	}
}

// TestKubectlPageWidensToTheStartOfThePlacesWindow: a place that named where its
// window begins has a first line, and reading past it would be reading outside
// what was asked for.
func TestKubectlPageWidensToTheStartOfThePlacesWindow(t *testing.T) {
	got := fakePages(t, func(int) string { return `exit 0` })

	cfg := kubePlace()
	cfg.Range = Range{Spec: "2h", Since: pageStart(t).Add(-2 * time.Hour)}
	lines, err := cfg.Page(t.Context(), pageStart(t), 50)
	require.NoError(t, err)
	require.Empty(t, lines, "and an empty window is empty, not a failure")

	cmd := got.list()
	require.Contains(t, cmd[len(cmd)-1], "--since-time="+cfg.Range.Since.Format(time.RFC3339))
	for _, c := range cmd {
		require.Contains(t, c, "--since-time", "never below what the place asked for")
	}
}

// TestPageOfAPlaceThatIsNotThere: a cluster that does not run what a group named
// contributes nothing, which is what it contributes to a page too — and it is
// not asked again in a wider window, since the answer would not change.
func TestPageOfAPlaceThatIsNotThere(t *testing.T) {
	got := fakePages(t, func(int) string {
		return `echo 'Error from server (NotFound): deployments.apps "api" not found' >&2; exit 1`
	})

	lines, err := kubePlace().Page(t.Context(), pageStart(t), 50)
	require.NoError(t, err)
	require.Empty(t, lines)
	require.Len(t, got.list(), 1)
}

// TestAPageThatCouldNotBeReadIsSaidOutLoud: a collector that failed has not
// answered that there is nothing older, and a page quietly swallowed would read
// as the start of the log.
func TestAPageThatCouldNotBeReadIsSaidOutLoud(t *testing.T) {
	fakePages(t, func(int) string {
		return `echo 'error: You must be logged in to the server' >&2; exit 1`
	})

	_, err := kubePlace().Page(t.Context(), pageStart(t), 50)
	require.Error(t, err)
}

// TestAPageIsReadThroughTheSameIndirection: the page is built from the config the
// stream was opened with, so the ssh, the sudo and the kubeconfig come with it.
func TestAPageIsReadThroughTheSameIndirection(t *testing.T) {
	cfg := kubePlace()
	cfg.KubeConfig = "/etc/telescope/kube.yaml"

	page := cfg.pageConfig(pageStart(t), 50)
	argv := page.Argv()
	require.Equal(t, "ssh", argv[0])
	require.Equal(t, "bastion", argv[len(argv)-2])

	cmd := argv[len(argv)-1]
	require.Contains(t, cmd, "sudo -n kubectl --kubeconfig=/etc/telescope/kube.yaml")
	require.Contains(t, cmd, "-n checkout")
	require.Contains(t, cmd, "deploy/api")
	require.Contains(t, argv, "-T", "a window that has already happened needs no pty")
}
