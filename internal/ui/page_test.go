package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

func TestLeadingUndatedEntriesDoNotBlockKubernetesPaging(t *testing.T) {
	for _, tt := range []struct {
		name    string
		leading source.Line
	}{
		{"stderr diagnostic", source.Line{Data: []byte("Defaulted container api out of: api, sidecar"), Stderr: true}},
		{"SSH diagnostic", source.Line{Data: []byte("Defaulted container api out of: api, sidecar")}},
		{"source note", source.Line{Kind: source.KindRestarted, Reason: "api restarted"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newLogs(source.Config{Collector: source.CollectorKubectl, Target: "api-0"}, logs.NewStore(100), "")
			m.resize(100, 30)
			m.append(tt.leading)
			m.append(source.Line{Data: []byte("started listening"), At: time.Date(2026, 10, 30, 0, 10, 0, 0, time.UTC)})
			require.False(t, m.store.Entries()[0].HasTime)
			next, cmd := m.Update(k("pgup"))
			require.NotNil(t, cmd, "a dated log entry should provide the history boundary")
			require.Empty(t, next.pageLimit())
			require.Len(t, next.store.Entries(), 2, "diagnostics remain visible")
		})
	}
}

func TestPagingLimitsAreHighlightedWhenReached(t *testing.T) {
	for _, tt := range []struct {
		name    string
		cfg     source.Config
		line    string
		atStart bool
		atCap   bool
		want    string
	}{
		{
			name: "command", cfg: source.Config{Collector: source.CollectorCommand, Args: "tail -F app.log"},
			want: "cannot load older entries: command sources have no backfill",
		},
		{
			name: "events", cfg: source.Config{Collector: source.CollectorKubeWatch, Target: "events"},
			want: "cannot load older entries: Kubernetes watches show current objects and live changes",
		},
		{
			name: "custom resources", cfg: source.Config{Collector: source.CollectorKubeWatch, Target: "clusters.cluster.x-k8s.io"},
			want: "cannot load older entries: Kubernetes watches show current objects and live changes",
		},
		{
			name: "mixed group", cfg: source.Config{Collector: source.CollectorMerge, Merge: []source.Config{
				{Collector: source.CollectorDocker, Container: "api"},
				{Collector: source.CollectorKubeWatch, Target: "events"},
			}},
			want: "cannot load older entries: a merged source has no backfill",
		},
		{
			name: "missing timestamp", cfg: source.Config{Collector: source.CollectorJournal},
			line: `{"msg":"no timestamp"}`,
			want: "cannot load older entries: no log entries have a timestamp",
		},
		{
			name: "buffer full", cfg: source.Config{Collector: source.CollectorDocker, Container: "api"}, atCap: true,
			want: "cannot load older entries: the buffer is full",
		},
		{
			name: "history exhausted", cfg: source.Config{Collector: source.CollectorDocker, Container: "api"}, atStart: true,
			want: "no older entries in this source",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			limit := 10
			if tt.atCap {
				limit = 1
			}
			m := newLogs(tt.cfg, logs.NewStore(limit), "")
			m.resize(140, 30)
			line := tt.line
			if line == "" {
				line = `{"ts":"2026-08-11T10:00:00Z","msg":"first"}`
			}
			m.append(source.Line{Data: []byte(line)})
			m.atStart = tt.atStart
			require.NotContains(t, m.View(), styleWarn.Render(tt.want))
			next, cmd := m.Update(k("pgup"))
			require.Nil(t, cmd)
			require.Contains(t, next.View(), styleWarn.Render(tt.want))
			next, _ = next.Update(k("down"))
			require.NotContains(t, next.View(), styleWarn.Render(tt.want))
		})
	}
}

func TestPagingHintOnlyAppearsAtTheTop(t *testing.T) {
	cfg := source.Config{Collector: source.CollectorCommand, Args: "tail -F app.log"}
	m := send(t, New(), size(), connectMsg{cfg: cfg})
	var lines []source.Line
	for i := range 50 {
		lines = append(lines, source.Line{Data: []byte(vlogsLine("2026-08-11T10:00:00Z", fmt.Sprintf("line %d", i)))})
	}
	m = send(t, m, linesMsg{lines: lines, closed: true})
	m = send(t, m, k("pgup"))
	require.Positive(t, m.(Model).logs.cursor)
	require.NotContains(t, screen(t, m), "cannot load older entries")
	m = send(t, m, k("home"))
	require.Contains(t, m.View(), styleWarn.Render("cannot load older entries: command sources have no backfill"))
	m = send(t, m, k("v"))
	require.NotContains(t, screen(t, m), "cannot load older entries")
}

// vlogsLine is one entry as VictoriaLogs answers it.
func vlogsLine(ts, msg string) string {
	return `{"_time":"` + ts + `","_msg":"` + msg + `"}`
}

// pagingModel is a database place holding two lines, read from a server that
// answers every page with body.
func pagingModel(t *testing.T, body ...string) tea.Model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Join(body, "\n")))
	}))
	t.Cleanup(srv.Close)

	cfg := source.Config{
		Collector: source.CollectorVictoriaLogs,
		Endpoint:  source.Endpoint{Name: "prod", URL: srv.URL},
		Target:    "app:api",
		Tail:      100,
	}
	m := send(t, New(), size(), connectMsg{cfg: cfg})
	return send(t, m, linesMsg{lines: []source.Line{
		{Data: []byte(vlogsLine("2026-08-11T10:00:03Z", "third"))},
		{Data: []byte(vlogsLine("2026-08-11T10:00:04Z", "fourth"))},
	}, closed: true})
}

// ask presses key and runs whatever it asked for, which is how a page is
// fetched: send leaves commands with side effects unrun.
func ask(t *testing.T, m tea.Model, key string) tea.Model {
	t.Helper()
	next, cmd := m.Update(k(key))
	require.NotNil(t, cmd, "reaching the top asks for what came before it")
	msg := cmd()
	require.IsType(t, pageMsg{}, msg)
	return send(t, next, msg)
}

// TestPageAtTheTop: the top of the list is where the lines before it belong, so
// reaching it is what asks a database for them.
func TestPageAtTheTop(t *testing.T) {
	m := pagingModel(t,
		vlogsLine("2026-08-11T10:00:02Z", "second"),
		vlogsLine("2026-08-11T10:00:01Z", "first"),
	)
	require.Equal(t, []string{"third", "fourth"}, held(m))

	m = ask(t, m, "up")
	require.Equal(t, []string{"first", "second", "third", "fourth"}, held(m),
		"the page lands in front, in order")

	lg := m.(Model).logs
	require.Equal(t, "third", lg.view.Entries(lg.store)[lg.cursor].Record.Body,
		"and the reader is left on the line they were reading")
}

// TestPageOnlyWhereItCanBeAsked: a free-form command is somebody else's line and
// telescope has nowhere to put a bound in it.
func TestPageOnlyWhereItCanBeAsked(t *testing.T) {
	cfg := source.Config{Collector: source.CollectorCommand, Args: "tail -F /var/log/app.log"}
	m := send(t, New(), size(), connectMsg{cfg: cfg})
	m = send(t, m, linesMsg{lines: []source.Line{
		{Data: []byte(`{"ts":"2026-08-10T10:00:00Z","msg":"first"}`)},
		{Data: []byte(`{"ts":"2026-08-10T10:00:05Z","msg":"second"}`)},
	}, closed: true})

	_, cmd := m.Update(k("up"))
	require.Nil(t, cmd)
	require.NotContains(t, screen(t, m), "at the start")
}

// TestACollectorIsAskedForOlderLinesToo: the tools telescope writes the command
// for take a window that ends, so reaching the top asks them for one.
func TestACollectorIsAskedForOlderLinesToo(t *testing.T) {
	m := logsModel(t,
		`{"ts":"2026-08-10T10:00:00Z","msg":"first"}`,
		`{"ts":"2026-08-10T10:00:05Z","msg":"second"}`,
	)
	_, cmd := m.Update(k("up"))
	require.NotNil(t, cmd, "docker bounds both ends of what it shows")
}

// TestPageEmptyIsTheStart: a database that answers with nothing has nothing
// older, and is not asked again.
func TestPageEmptyIsTheStart(t *testing.T) {
	m := ask(t, pagingModel(t), "up")
	require.Contains(t, screen(t, m), "at the start")

	_, cmd := m.Update(k("up"))
	require.Nil(t, cmd, "the answer holds until the view is opened again")
}

// TestPageErrorIsNotRepeated: a database that failed will fail the same way on
// the next keystroke, and the reader is at the top of the list where every
// keystroke is one.
func TestPageErrorIsNotRepeated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("down for maintenance"))
	}))
	defer srv.Close()

	m := pagingModel(t)
	mm := m.(Model)
	mm.logs.cfg.Endpoint.URL = srv.URL
	m = mm

	m = ask(t, m, "up")
	require.Contains(t, screen(t, m), "older:")

	_, cmd := m.Update(k("up"))
	require.Nil(t, cmd, "at the top it is not asked again")

	m = send(t, m, k("down"))
	require.NotContains(t, screen(t, m), "older:", "away from the top it is worth another try")
	_, cmd = m.Update(k("up"))
	require.NotNil(t, cmd)
}

// TestPageCapIsSaidOutLoud: reading further back stops at the store's cap, and
// a list that quietly stopped paging would read as a stream that ended.
func TestPageCapIsSaidOutLoud(t *testing.T) {
	m := pagingModel(t)
	mm := m.(Model)
	mm.logs.atCap = true
	require.Contains(t, screen(t, mm), "holding all it can")
}

// held is the bodies of the lines the store is holding, oldest first.
func held(m tea.Model) []string {
	var out []string
	for _, e := range m.(Model).logs.store.Entries() {
		out = append(out, e.Record.Body)
	}
	return out
}
