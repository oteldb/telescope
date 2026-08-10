package ui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/source"
)

// TestMain stubs out completion so no test shells out to systemctl, kubectl or
// docker. Suggestions are injected explicitly with candidates.
func TestMain(m *testing.M) {
	fetcher = func(context.Context, complete.Request) ([]complete.Candidate, error) {
		return nil, nil
	}
	os.Exit(m.Run())
}

// screen renders m at a fixed size with colors stripped.
func screen(t *testing.T, m tea.Model) string {
	t.Helper()
	return ansi.Strip(m.View())
}

// send delivers msgs, following the navigation commands the views return.
// Commands with side effects (spawning a stream) are deliberately not run.
func send(t *testing.T, m tea.Model, msgs ...tea.Msg) tea.Model {
	t.Helper()
	for _, msg := range msgs {
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		if cmd == nil || !navigates(msg) {
			continue
		}
		switch out := cmd().(type) {
		case openEntryMsg:
			m, _ = m.Update(out)
		case backMsg:
			m, _ = m.Update(out)
		}
	}
	return m
}

// navigates reports whether a message may produce a view switch. Other
// commands are left unrun: connecting spawns a process, and a blinking cursor
// only resolves after a real delay.
func navigates(msg tea.Msg) bool {
	km, ok := msg.(tea.KeyMsg)
	return ok && (km.Type == tea.KeyEnter || km.Type == tea.KeyEsc)
}

func k(s string) tea.Msg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	return tea.KeyMsg{Type: map[string]tea.KeyType{
		"enter": tea.KeyEnter,
		"tab":   tea.KeyTab,
		"esc":   tea.KeyEsc,
		"down":  tea.KeyDown,
		"up":    tea.KeyUp,
	}[s]}
}

func size() tea.Msg { return tea.WindowSizeMsg{Width: 100, Height: 30} }

func TestStartScreen(t *testing.T) {
	m := send(t, New(), size())
	out := screen(t, m)

	require.Contains(t, out, "telemetry viewer")
	require.Contains(t, out, "local")
	require.Contains(t, out, "ssh")
	require.Contains(t, out, "tab")
	require.Equal(t, 30, strings.Count(out, "\n")+1, "start screen fills the window")
}

func TestStartFlowReachesQuery(t *testing.T) {
	// local ▸ tab to kubectl ▸ target ▸ query.
	m := send(t, New(), size(), k("enter"), k("tab"))
	out := screen(t, m)
	require.Contains(t, out, "journalctl")
	require.Contains(t, out, "kubectl")

	m = send(t, m, k("o"), k("enter"))
	out = screen(t, m)
	require.Contains(t, out, "kubectl logs o", "breadcrumb shows the built command")
	require.Contains(t, out, "no filter", "query step")
}

// candidates fakes a completion reply for whatever the model last asked for.
func candidates(m tea.Model, values ...string) tea.Msg {
	items := make([]complete.Candidate, 0, len(values))
	for _, v := range values {
		items = append(items, complete.Candidate{Value: v, Detail: "running"})
	}
	return candidatesMsg{key: m.(Model).start.candKey, items: items}
}

func TestCompletionListsAndFilters(t *testing.T) {
	m := send(t, New(), size(), k("enter"), k("tab"), k("tab")) // docker
	m = send(t, m, candidates(m, "oteldb", "clickhouse", "otel-collector"))

	out := screen(t, m)
	require.Contains(t, out, "oteldb")
	require.Contains(t, out, "clickhouse")
	require.Contains(t, out, "running", "detail column")
	require.Contains(t, out, "↑↓ suggestions")

	// Typing narrows the list without another fetch.
	m = send(t, m, k("o"), k("t"), k("e"), k("l"))
	out = screen(t, m)
	require.Contains(t, out, "oteldb")
	require.NotContains(t, out, "clickhouse")
}

func TestCompletionAccept(t *testing.T) {
	m := send(t, New(), size(), k("enter"), k("tab"), k("tab"))
	m = send(t, m, candidates(m, "oteldb", "clickhouse"))

	// Down highlights the first suggestion, tab inserts it.
	m = send(t, m, k("down"), k("tab"))
	require.Equal(t, "oteldb", m.(Model).start.target.Value())

	// Accepting clears the highlight, so enter advances instead of re-accepting.
	m = send(t, m, k("enter"))
	require.Contains(t, screen(t, m), "docker logs")
}

func TestCompletionEnterAcceptsHighlighted(t *testing.T) {
	m := send(t, New(), size(), k("enter"), k("tab"), k("tab"))
	m = send(t, m, candidates(m, "oteldb", "clickhouse"))

	m = send(t, m, k("down"), k("down"), k("enter"))
	require.Equal(t, "clickhouse", m.(Model).start.target.Value())
	require.Equal(t, stepCollector, m.(Model).start.step, "enter accepted, it did not advance")
}

// TestCompletionIgnoresStaleReply guards against a slow listing from a
// previously selected collector overwriting the current one.
func TestCompletionIgnoresStaleReply(t *testing.T) {
	m := send(t, New(), size(), k("enter"))
	stale := candidatesMsg{key: m.(Model).start.candKey, items: []complete.Candidate{{Value: "stale-unit"}}}

	m = send(t, m, k("tab")) // move to kubectl, invalidating the request
	m = send(t, m, stale)
	require.NotContains(t, screen(t, m), "stale-unit")
}

func TestCompletionTabCyclesChipsWhenNothingHighlighted(t *testing.T) {
	m := send(t, New(), size(), k("enter"))
	m = send(t, m, candidates(m, "kubelet"))

	// Nothing is highlighted yet, so tab still switches the collector.
	m = send(t, m, k("tab"))
	require.Equal(t, source.CollectorKubectl, collectors[m.(Model).start.collector])
}

func TestCompletionErrorIsShown(t *testing.T) {
	m := send(t, New(), size(), k("enter"), k("tab"), k("tab"))
	m = send(t, m, candidatesMsg{
		key: m.(Model).start.candKey,
		err: errors.New("docker: command not found"),
	})
	require.Contains(t, screen(t, m), "docker: command not found")
}

func TestHostCompletionOnlyForSSH(t *testing.T) {
	m := send(t, New(), size())
	require.Empty(t, m.(Model).start.candKey, "local has no host to complete")

	m = send(t, m, k("tab"))
	require.Equal(t, "host", m.(Model).start.candKey)
}

// countingFetcher records how many times each request key was looked up.
func countingFetcher(t *testing.T, items ...string) *map[string]int {
	t.Helper()
	calls := map[string]int{}
	prev := fetcher
	fetcher = func(_ context.Context, r complete.Request) ([]complete.Candidate, error) {
		calls[r.Key()]++
		out := make([]complete.Candidate, 0, len(items))
		for _, v := range items {
			out = append(out, complete.Candidate{Value: v})
		}
		return out, nil
	}
	t.Cleanup(func() { fetcher = prev })
	return &calls
}

// run executes the commands a model returns, so preloads actually happen.
func runCmds(m tea.Model, cmd tea.Cmd) tea.Model {
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = runCmds(m, c)
		}
	case candidatesMsg:
		m, _ = m.Update(msg)
	}
	return m
}

func TestCompletionPreloadsAndCaches(t *testing.T) {
	calls := countingFetcher(t, "alpha")

	m, cmd := New().Update(size())
	m, cmd = m.Update(k("enter")) // into the collector step
	m = runCmds(m, cmd)

	// Every collector was warmed, not just the selected one.
	require.Positive(t, (*calls)["local||journalctl"])
	require.Positive(t, (*calls)["local||docker"])
	require.Positive(t, (*calls)["local||kubectl"])

	// Walking the chips and stepping back and forth must hit the cache. Going
	// back to the transport step additionally warms the host list.
	for range 3 {
		m, cmd = m.Update(k("tab"))
		m = runCmds(m, cmd)
	}
	m, cmd = m.Update(k("esc"))
	m = runCmds(m, cmd)
	m, cmd = m.Update(k("enter"))
	m = runCmds(m, cmd)

	require.ElementsMatch(t,
		[]string{"host", "local||journalctl", "local||kubectl", "local||docker", "local||command"},
		keysOf(*calls))
	for key, n := range *calls {
		require.Equal(t, 1, n, "%s was looked up more than once", key)
	}
	require.Contains(t, screen(t, m), "alpha", "cached suggestions render immediately")
}

func TestCompletionRefreshDropsCache(t *testing.T) {
	calls := countingFetcher(t, "alpha")

	m, cmd := New().Update(size())
	m, cmd = m.Update(k("enter"))
	m = runCmds(m, cmd)
	require.Equal(t, 1, (*calls)["local||journalctl"])

	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = runCmds(m, cmd)
	require.Equal(t, 2, (*calls)["local||journalctl"], "refresh re-runs the listing")
}

func TestHostsPreloadedAtInit(t *testing.T) {
	calls := countingFetcher(t, "node1")

	m := New()
	m2 := runCmds(m, m.Init())
	m2, cmd := m2.Update(size())
	m2 = runCmds(m2, cmd)

	require.Equal(t, 1, (*calls)["host"])

	// Switching to ssh shows them without another lookup.
	m2, cmd = m2.Update(k("tab"))
	m2 = runCmds(m2, cmd)
	require.Equal(t, 1, (*calls)["host"])
	require.Contains(t, screen(t, m2), "node1")
}

func keysOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestUserUnitReachesCommand checks that the user/ prefix survives the whole
// way from a suggestion to the journalctl invocation.
func TestUserUnitReachesCommand(t *testing.T) {
	m := send(t, New(), size(), k("enter")) // journalctl
	m = send(t, m, candidates(m, "sshd", "user/syncthing"))

	m = send(t, m, k("down"), k("down"), k("tab"))
	require.Equal(t, "user/syncthing", m.(Model).start.target.Value())

	m = send(t, m, k("enter"))
	require.Contains(t, screen(t, m), "journalctl --user --no-pager -o cat -u syncthing")
}

func TestStartRejectsEmptySSHHost(t *testing.T) {
	m := send(t, New(), size(), k("tab"), k("enter"))
	require.Contains(t, screen(t, m), "ssh transport requires a host")
}

func logsModel(t *testing.T, lines ...string) tea.Model {
	t.Helper()
	cfg := source.Config{Collector: source.CollectorDocker, Container: "app", Follow: true}
	m := send(t, New(), size(), connectMsg{cfg: cfg})

	batch := make([]source.Line, 0, len(lines))
	for _, l := range lines {
		batch = append(batch, source.Line{Data: []byte(l)})
	}
	return send(t, m, linesMsg{lines: batch, closed: true})
}

func TestLogView(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","ts":"2026-08-10T10:00:00Z","msg":"started"}`,
		`{"level":"error","ts":"2026-08-10T10:00:05Z","msg":"exploded"}`,
		`unstructured tail`,
	)
	out := screen(t, m)

	require.Contains(t, out, "docker logs -f app")
	require.Contains(t, out, "3 shown")
	require.Contains(t, out, "started")
	require.Contains(t, out, "exploded")
	require.Contains(t, out, "unstructured tail")
	require.Contains(t, out, "follow on")
	require.Equal(t, 30, strings.Count(out, "\n")+1, "log view fills the window")
}

func TestLogViewFilter(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","msg":"alpha"}`,
		`{"level":"info","msg":"beta"}`,
	)
	m = send(t, m, k("/"))
	for _, r := range "beta" {
		m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = send(t, m, k("enter"))

	out := screen(t, m)
	require.Contains(t, out, "1 shown")
	require.Contains(t, out, "beta")
	require.NotContains(t, out, "alpha")
}

func TestEntryView(t *testing.T) {
	m := logsModel(t, `{"level":"warn","ts":"2026-08-10T10:00:00Z","msg":"slow","dur":"1s"}`)
	m = send(t, m, k("enter"))

	out := screen(t, m)
	require.Contains(t, out, "entry #0")
	require.Contains(t, out, "WARN")
	require.Contains(t, out, "slow")
	require.Contains(t, out, "fields")
	require.Contains(t, out, "dur")
	require.Contains(t, out, "raw")

	// esc returns to the logs.
	require.Contains(t, screen(t, send(t, m, k("esc"))), "docker logs")
}

func TestLogViewStreamError(t *testing.T) {
	cfg := source.Config{Collector: source.CollectorDocker, Container: "app"}
	m := send(t, New(), size(), connectMsg{cfg: cfg},
		streamErrMsg{err: errEmptyHost})
	require.Contains(t, screen(t, m), "failed")
}

func TestLogViewEmpty(t *testing.T) {
	require.NotPanics(t, func() {
		m := logsModel(t)
		_ = screen(t, m)
		_ = screen(t, send(t, m, k("enter"), k("down"), k("up")))
	})
}

func TestTinyWindowDoesNotPanic(t *testing.T) {
	require.NotPanics(t, func() {
		m := send(t, New(), tea.WindowSizeMsg{Width: 4, Height: 2},
			connectMsg{cfg: source.Config{Collector: source.CollectorJournal}},
			linesMsg{lines: []source.Line{{Data: []byte("x")}}, closed: true})
		_ = m.View()
	})
}
