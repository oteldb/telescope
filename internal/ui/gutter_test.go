package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// TestTheTimeIsDrawnByTheView: a structured line used to be rendered with its
// own time inside the text, which is worked out once and cannot be changed by
// looking at it.
func TestTheTimeIsDrawnByTheView(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","ts":"2026-08-10T10:00:00Z","msg":"started"}`,
		`{"level":"error","ts":"2026-08-10T10:00:05Z","msg":"exploded"}`,
	)
	lg := m.(Model).logs
	for _, e := range lg.store.Entries() {
		require.NotContains(t, e.Text, "10:00:0", "the rendering carries no time")
	}
	require.True(t, lg.cols.time, "the column is on for a structured line too")

	out := screen(t, m)
	require.Contains(t, out, stampOf(m, 0), "and the view writes it")
	require.Contains(t, out, "started")
}

// TestCycleTheTimeColumn: the same line is read as a clock while following a
// burst and as a date once the list covers days.
func TestCycleTheTimeColumn(t *testing.T) {
	m := logsModel(t, `{"level":"info","ts":"2026-08-10T10:00:00Z","msg":"started"}`)
	at := m.(Model).logs.store.Entries()[0].At

	require.Contains(t, screen(t, m), at.Local().Format(clockLayout))

	m = send(t, m, k("t"))
	require.Equal(t, timeFull, m.(Model).logs.times)
	require.Contains(t, screen(t, m), at.Local().Format(fullLayout))

	m = send(t, m, k("t"))
	require.Equal(t, timeAge, m.(Model).logs.times)
	require.Contains(t, screen(t, m), humanAge(at.Sub(m.(Model).logs.origin)))

	m = send(t, m, k("t"))
	require.Equal(t, timeClock, m.(Model).logs.times, "and back round")
}

// TestTheAgeIsMeasuredFromWhenTheViewOpened: it is not "how long ago" recomputed
// on every redraw, which would renumber the list under the reader.
func TestTheAgeIsMeasuredFromWhenTheViewOpened(t *testing.T) {
	m := send(t, logsModel(t, `{"ts":"2026-08-10T10:00:00Z","msg":"started"}`), k("t"), k("t"))
	first := screen(t, m)
	require.Equal(t, first, screen(t, m), "the same lines read the same twice")

	lg := m.(Model).logs
	stamp := func(d time.Duration) string {
		return strings.TrimSpace(timeAge.stamp(lg.origin.Add(d), lg.origin))
	}
	require.Equal(t, "-1m00s", stamp(-time.Minute), "before the query")
	require.Equal(t, "now", stamp(0))
	require.Equal(t, "+2.0s", stamp(2*time.Second), "and after it")
}

func TestHumanAge(t *testing.T) {
	for _, tt := range []struct {
		d    time.Duration
		want string
	}{
		{0, "now"},
		{500 * time.Millisecond, "now"},
		{-500 * time.Millisecond, "-0.5s"},
		{-90 * time.Second, "-1m30s"},
		{-time.Hour - 2*time.Minute, "-1h02m"},
		{-49 * time.Hour, "-2d01h"},
		{3 * time.Second, "+3.0s"},
	} {
		require.Equal(t, tt.want, strings.TrimSpace(humanAge(tt.d)), tt.d.String())
	}
}

// TestAGapIsDrawnBetweenTheLines: an eight-minute hole in a log that was
// writing every second is the thing a reader is looking for, and shading alone
// does not say how long it lasted.
func TestAGapIsDrawnBetweenTheLines(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	m := timedModel(t,
		entryAt(at, "before"),
		entryAt(at.Add(gapAfter/2), "still going"),
		entryAt(at.Add(8*time.Minute), "after the silence"),
	)

	out := screen(t, m)
	require.Contains(t, out, "7m30s of silence", "measured from the line above it")
	require.Contains(t, out, at.Add(8*time.Minute).Local().Format(fullLayout),
		"the instant it picks up again, which the clock column cannot say")
	require.Equal(t, 1, strings.Count(out, "of silence"), "a lull inside the minute is not a gap")

	rows := strings.Split(out, "\n")
	silence := slices.IndexFunc(rows, func(r string) bool { return strings.Contains(r, "of silence") })
	require.Positive(t, silence)
	require.Contains(t, rows[silence-1], "still going", "drawn between the two lines it is between")
	require.Contains(t, rows[silence+1], "after the silence")
}

// TestAGapCostsARow: the window is counted in rows, since a gap takes one.
func TestAGapCostsARow(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	lines := []source.Line{entryAt(at, "first")}
	for i := 1; i < 40; i++ {
		lines = append(lines, entryAt(at.Add(time.Duration(i)*time.Hour), "line"))
	}
	m := timedModel(t, lines...)

	lg := m.(Model).logs
	entries := lg.view.Entries(lg.store)
	require.Equal(t, lg.cursor, len(entries)-1, "following leaves the cursor on the last line")
	require.LessOrEqual(t, lg.rows(entries, lg.top, lg.cursor), lg.bodyHeight(),
		"the window is counted in rows, gaps included")
	require.Greater(t, lg.rows(entries, lg.top-1, lg.cursor), lg.bodyHeight(),
		"and filled: one line further up would not fit")
	require.Less(t, lg.cursor-lg.top+1, lg.bodyHeight(), "which is fewer lines than rows")
}

func TestGapNeedsTwoTimes(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	timed := &logs.Entry{At: at, HasTime: true}
	arrived := &logs.Entry{At: at.Add(time.Hour)}

	_, ok := gap(timed, arrived)
	require.False(t, ok, "an arrival time says when the view was running, not when the line was written")
	_, ok = gap(nil, timed)
	require.False(t, ok)
	d, ok := gap(timed, &logs.Entry{At: at.Add(time.Hour), HasTime: true})
	require.True(t, ok)
	require.Equal(t, time.Hour, d)
}

// timedModel is a log view over lines a source reported the time of, which is
// what a database does.
func timedModel(t *testing.T, lines ...source.Line) tea.Model {
	t.Helper()
	cfg := source.Config{Collector: source.CollectorDocker, Container: "app", Follow: true}
	m := send(t, New(), size(), connectMsg{cfg: cfg})
	return send(t, m, linesMsg{lines: lines, closed: true})
}

func entryAt(at time.Time, msg string) source.Line {
	return source.Line{Data: []byte(`{"msg":"` + msg + `"}`), At: at}
}

// stampOf is the time column of one entry as the view is currently writing it.
func stampOf(m tea.Model, i int) string {
	lg := m.(Model).logs
	return strings.TrimSpace(lg.times.stamp(lg.store.Entries()[i].At, lg.origin))
}
