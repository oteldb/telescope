package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// rowsOf is the body of the list, one string per drawn row, blanks dropped.
func rowsOf(t *testing.T, m interface{ View() string }) []string {
	t.Helper()
	var out []string
	for line := range strings.SplitSeq(ansi.Strip(m.View()), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func countRows(rows []string, word string) int {
	n := 0
	for _, r := range rows {
		if strings.Contains(r, word) {
			n++
		}
	}
	return n
}

// TestARepeatedLineIsOneRow: a source saying one thing four times is saying one
// thing, and the list should spend one row on it.
func TestARepeatedLineIsOneRow(t *testing.T) {
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	m := timedModel(t,
		entryAt(at, "serving"),
		entryAt(at.Add(time.Second), "connection reset by peer"),
		entryAt(at.Add(2*time.Second), "connection reset by peer"),
		entryAt(at.Add(3*time.Second), "connection reset by peer"),
		entryAt(at.Add(4*time.Second), "done"),
	)

	rows := rowsOf(t, m)
	require.Equal(t, 1, countRows(rows, "connection reset by peer"), "one row, not three")
	require.Equal(t, 1, countRows(rows, "×3"), "and it says how many it stands for")
	require.Equal(t, 1, countRows(rows, "serving"))
	require.Equal(t, 1, countRows(rows, "done"))
}

// TestAClampedListSaysWhatItFolded: a row standing for four hundred lines
// without saying so would be lying about the log.
func TestAClampedListSaysWhatItFolded(t *testing.T) {
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	lines := []source.Line{entryAt(at, "serving")}
	for i := 1; i < 5; i++ {
		lines = append(lines, entryAt(at.Add(time.Duration(i)*time.Second), "retrying"))
	}
	m := timedModel(t, lines...)
	require.Contains(t, ansi.Strip(m.View()), "3 clamped", "four lines drawn as one row hid three")
}

// TestClampingIsSomethingTheReaderCanTurnOff: what the list shows is the
// reader's, and a fold is only ever a saving of rows.
func TestClampingIsSomethingTheReaderCanTurnOff(t *testing.T) {
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	m := timedModel(t,
		entryAt(at, "serving"),
		entryAt(at.Add(time.Second), "connection reset by peer"),
		entryAt(at.Add(2*time.Second), "connection reset by peer"),
		entryAt(at.Add(3*time.Second), "connection reset by peer"),
	)
	require.Equal(t, 1, countRows(rowsOf(t, m), "connection reset by peer"))

	m = pressRoot(t, m, "c")
	rows := rowsOf(t, m)
	require.Equal(t, 3, countRows(rows, "connection reset by peer"), "every line is back")
	require.Zero(t, countRows(rows, "×3"), "and none of them stands for the others")

	m = pressRoot(t, m, "c")
	require.Equal(t, 1, countRows(rowsOf(t, m), "connection reset by peer"))
}

// TestClampingLeavesTheCursorOnItsLine: the cursor is on a line, and folding
// the rows under it must not carry the reader somewhere else.
func TestClampingLeavesTheCursorOnItsLine(t *testing.T) {
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	m := timedModel(t,
		entryAt(at, "serving"),
		entryAt(at.Add(time.Second), "retrying"),
		entryAt(at.Add(2*time.Second), "retrying"),
		entryAt(at.Add(3*time.Second), "retrying"),
		entryAt(at.Add(4*time.Second), "done"),
	)

	// Off, then up onto the middle of the run, then on again.
	m = pressRoot(t, m, "c")
	m = pressRoot(t, m, "up")
	m = pressRoot(t, m, "up")
	lg := m.(Model).logs
	entries := lg.view.Entries(lg.store)
	require.Equal(t, "retrying", entries[lg.cursor].Record.Body, "on a line of the run")

	m = pressRoot(t, m, "c")
	lg = m.(Model).logs
	entries = lg.view.Entries(lg.store)
	runs := clampRuns(entries, lg.clamped)
	require.Less(t, lg.cursor, len(runs))
	require.Equal(t, "retrying", entries[runs[lg.cursor].first].Record.Body,
		"and on the row that line is now drawn in")
}

// TestARepetitionAfterASilenceIsNotFolded: a line saying the same thing once an
// hour is a heartbeat, and folding it would take the quiet off the screen.
func TestARepetitionAfterASilenceIsNotFolded(t *testing.T) {
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	m := timedModel(t,
		entryAt(at, "still alive"),
		entryAt(at.Add(time.Hour), "still alive"),
		entryAt(at.Add(2*time.Hour), "still alive"),
	)

	rows := rowsOf(t, m)
	require.Equal(t, 3, countRows(rows, "still alive"), "one row each")
	require.Equal(t, 2, countRows(rows, "of silence"), "and the gaps between them kept")
}

// TestTwoSourcesAreNotOneRepeatingItself: two pods of a deployment saying the
// same thing are two of them saying it, not one saying it twice.
func TestTwoSourcesAreNotOneRepeatingItself(t *testing.T) {
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	line := func(src, body string) *logs.Entry {
		return &logs.Entry{At: at, HasTime: true, Source: src, Record: logs.Record{Body: body}}
	}
	runs := clampRuns([]*logs.Entry{
		line("api-1", "ready"),
		line("api-2", "ready"),
		line("api-2", "ready"),
	}, true)
	require.Len(t, runs, 2)
	require.Equal(t, 1, runs[0].n)
	require.Equal(t, 2, runs[1].n, "the same pod saying it twice is one row")

	// A line with nothing to compare is not a copy of the one before it.
	blank := clampRuns([]*logs.Entry{line("api-1", ""), line("api-1", "")}, true)
	require.Len(t, blank, 2)
}

// TestANoteIsNeverFolded: telescope says a thing for a reason, and a second one
// is a second failure.
func TestANoteIsNeverFolded(t *testing.T) {
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	note := func(d time.Duration) source.Line {
		return source.Line{
			Kind:   source.KindReadFailed,
			Reason: "token too long",
			Stderr: true,
			At:     at.Add(d),
		}
	}
	m := timedModel(t, entryAt(at, "serving"), note(time.Second), note(2*time.Second))
	require.Equal(t, 2, countRows(rowsOf(t, m), "token too long"), "both are said")
}

// TestOneMessageUnderTwoRequestsIsTwoRows: the row draws one line and T opens
// that line's trace, so a fold that spanned trace ids would open the wrong one.
func TestOneMessageUnderTwoRequestsIsTwoRows(t *testing.T) {
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	line := func(trace string) *logs.Entry {
		return &logs.Entry{
			At: at, HasTime: true,
			Record: logs.Record{Body: "checkout failed", TraceID: trace},
		}
	}
	runs := clampRuns([]*logs.Entry{line("aaa"), line("aaa"), line("bbb")}, true)
	require.Len(t, runs, 2)
	require.Equal(t, 2, runs[0].n, "the same request saying it twice is one row")
	require.Equal(t, 1, runs[1].n)

	// A line outside a trace still folds into another one outside it.
	require.Len(t, clampRuns([]*logs.Entry{line(""), line("")}, true), 1)
}

// TestALineWithNoMessageIsStillARow: a database answering with records that
// carry everything in their labels leaves nothing for the body, and a list that
// drew none of them would read as silence over a busy source.
func TestALineWithNoMessageIsStillARow(t *testing.T) {
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	labeled := func(d time.Duration, status string) source.Line {
		return source.Line{At: at.Add(d), Labels: []source.Label{{Key: "status", Value: status}}}
	}
	m := timedModel(t, labeled(0, "200"), labeled(time.Second, "404"), labeled(2*time.Second, "500"))

	rows := rowsOf(t, m)
	require.Equal(t, 3, countRows(rows, "(empty)"), "one row each, not one run of three")
	require.Contains(t, strings.Join(rows, "\n"), "3 lines")
}
