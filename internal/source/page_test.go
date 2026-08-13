package source

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPageOnlyDatabases: a command has already written what it wrote, so there
// is nothing to ask it for.
func TestPageOnlyDatabases(t *testing.T) {
	for _, c := range []Collector{CollectorJournal, CollectorKubectl, CollectorDocker, CollectorCommand} {
		cfg := Config{Collector: c}
		require.False(t, cfg.CanPage(), c)
		lines, err := cfg.Page(t.Context(), time.Now(), 100)
		require.NoError(t, err, c)
		require.Empty(t, lines, c)
	}
	require.True(t, Config{Collector: CollectorLoki}.CanPage())
	require.True(t, Config{Collector: CollectorVictoriaLogs}.CanPage())
}

// TestLokiPage: the page is the stream's own query against an end just before
// the oldest line held, and it comes back oldest first.
func TestLokiPage(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, lokiQueryRangePath, r.URL.Path)
		got = r.URL.Query()
		_, _ = w.Write([]byte(lokiResult([][2]string{{at(2), "second"}, {at(1), "first"}})))
	}))
	defer srv.Close()

	before := time.Unix(3, 0)
	lines, err := lokiConfig(srv.URL, false).Page(t.Context(), before, 50)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, texts(lines))
	require.Equal(t, `{app=~"(?i)api"}`, got.Get("query"))
	require.Equal(t, "50", got.Get("limit"))
	require.Equal(t, "backward", got.Get("direction"))
	require.Equal(t, lokiNanos(before.Add(-time.Nanosecond)), got.Get("end"),
		"the line already held is not read again")
}

// TestLokiPageWidensAnEmptyWindow: Loki reads a range, and a range with nothing
// in it says the service was quiet, not that it stopped.
func TestLokiPageWidensAnEmptyWindow(t *testing.T) {
	var starts []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ns, err := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		require.NoError(t, err)
		starts = append(starts, time.Unix(0, ns))
		if len(starts) < 3 {
			_, _ = w.Write([]byte(lokiResult()))
			return
		}
		_, _ = w.Write([]byte(lokiResult([][2]string{{at(1), "old"}})))
	}))
	defer srv.Close()

	before := time.Unix(1<<32, 0)
	lines, err := lokiConfig(srv.URL, false).Page(t.Context(), before, 50)
	require.NoError(t, err)
	require.Equal(t, []string{"old"}, texts(lines))

	require.Len(t, starts, 3)
	require.Equal(t, before.Sub(starts[0]), lokiSince+time.Nanosecond)
	for i := 1; i < len(starts); i++ {
		require.True(t, starts[i].Before(starts[i-1]), "each window reaches further back")
	}
}

// TestLokiPageStopsAtTheWindowStart: a place bounded below has a first line,
// and reading past it would be reading outside what was asked for.
func TestLokiPageStopsAtTheWindowStart(t *testing.T) {
	var asked int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		_, _ = w.Write([]byte(lokiResult()))
	}))
	defer srv.Close()

	since := time.Unix(100, 0)
	cfg := lokiConfig(srv.URL, false)
	cfg.Range = Range{Since: since}

	lines, err := cfg.Page(t.Context(), since.Add(time.Minute), 50)
	require.NoError(t, err)
	require.Empty(t, lines)
	require.Equal(t, 1, asked, "the whole of the window fits in one query")

	lines, err = cfg.Page(t.Context(), since, 50)
	require.NoError(t, err)
	require.Empty(t, lines)
	require.Equal(t, 1, asked, "and at its start there is nothing left to ask")
}

// TestVictoriaLogsPage: one query bounded above, read oldest first.
func TestVictoriaLogsPage(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, vlogsQueryPath, r.URL.Path)
		got = r.URL.Query()
		_, _ = w.Write([]byte(
			vlogsEntry("2026-08-11T10:00:02Z", "second") +
				vlogsEntry("2026-08-11T10:00:01Z", "first"),
		))
	}))
	defer srv.Close()

	before := time.Date(2026, 8, 11, 10, 0, 3, 0, time.UTC)
	lines, err := vlogsConfig(srv.URL, false).Page(t.Context(), before, 50)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	require.Contains(t, string(lines[0].Data), "first")
	require.Contains(t, string(lines[1].Data), "second")
	require.Equal(t, "app:api", got.Get("query"))
	require.Equal(t, "50", got.Get("limit"))
	require.Equal(t, before.Add(-time.Nanosecond).Format(time.RFC3339Nano), got.Get("end"))
	require.Empty(t, got.Get("start"), "an unbounded place reads as far back as the data goes")
}

// TestVictoriaLogsPageRetriesUnpushed: a page carries the compiled filter, so a
// server that will not read one costs the optimization here too.
func TestVictoriaLogsPageRetriesUnpushed(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		asked = append(asked, q)
		if q != "app:api" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("cannot parse"))
			return
		}
		_, _ = w.Write([]byte(vlogsEntry("2026-08-11T10:00:01Z", "first")))
	}))
	defer srv.Close()

	cfg := vlogsConfig(srv.URL, false).WithFilter(mustParse(`boom`))
	defer forgetPushdown(cfg.Endpoint)

	lines, err := cfg.Page(t.Context(), time.Date(2026, 8, 11, 10, 0, 3, 0, time.UTC), 50)
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Len(t, asked, 2)
	require.Equal(t, "app:api", asked[1], "the place alone is what is left to ask")
}

func texts(lines []Line) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, string(l.Data))
	}
	return out
}
