package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

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

// TestPageOnlyWhereItCanBeAsked: a command has written what it wrote.
func TestPageOnlyWhereItCanBeAsked(t *testing.T) {
	m := logsModel(t,
		`{"ts":"2026-08-10T10:00:00Z","msg":"first"}`,
		`{"ts":"2026-08-10T10:00:05Z","msg":"second"}`,
	)
	_, cmd := m.Update(k("up"))
	require.Nil(t, cmd)
	require.NotContains(t, screen(t, m), "at the start")
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
