package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

func TestVolumeStepIsTheNarrowestThatFits(t *testing.T) {
	for _, tt := range []struct {
		span    time.Duration
		buckets int
		want    time.Duration
	}{
		{0, 96, 10 * time.Millisecond},
		{3 * time.Second, 96, 50 * time.Millisecond},
		{time.Minute, 96, time.Second},
		{time.Hour, 96, time.Minute},
		{24 * time.Hour, 96, 30 * time.Minute},
		{30 * 24 * time.Hour, 96, 12 * time.Hour},
		{365 * 24 * time.Hour, 96, 4 * 24 * time.Hour},
	} {
		step := volStep(tt.span, tt.buckets)
		require.Equal(t, tt.want, step, tt.span.String())
		require.LessOrEqual(t, int(tt.span/step)+1, tt.buckets,
			"the chart has to fit the buckets it was given")
	}
}

// A bar says something only once its bucket holds several lines. Cut at the
// width of a column, nine lines three tenths of a second apart are nine bars of
// one — evenly spaced, all full height, a barcode rather than a shape.
func TestAFewLinesAreNotCutIntoOneBucketEach(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	var entries []*logs.Entry
	for i := range 9 {
		entries = append(entries, volEntry(at.Add(time.Duration(i)*300*time.Millisecond), zapcore.InfoLevel))
	}

	c, ok := bucketVolume(entries, 150, nil)
	require.True(t, ok)
	require.LessOrEqual(t, len(c.bars), 9, "fewer buckets than lines")
	require.Greater(t, c.max, 1, "so a bucket holds more than one of them")

	// The screen is still the other limit: a thousand lines are not cut into a
	// thousand buckets on a screen with room for a hundred and fifty.
	require.LessOrEqual(t, volBuckets(1000, 150), 150)
	require.Equal(t, 4, volBuckets(1, 150), "and one line still draws a chart")

	// Drawn edge to edge, a handful of buckets is one band that changes color
	// rather than a row of bars.
	rows := c.rows(150, volBars)
	require.Contains(t, ansi.Strip(rows[volBars-1]), "█ █", "there is air between two bars")
}

// TestVolumeCountsWhatTheViewShows: the panel is a count of the lines under it,
// bucketed and stacked, and the peak is the scale the bars are drawn to.
func TestVolumeCountsWhatTheViewShows(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	entries := []*logs.Entry{
		volEntry(at, zapcore.InfoLevel),
		volEntry(at.Add(time.Second), zapcore.InfoLevel),
		volEntry(at.Add(2*time.Second), zapcore.ErrorLevel),
		volEntry(at.Add(time.Minute), zapcore.WarnLevel),
	}

	c, ok := bucketVolume(entries, 96, nil)
	require.True(t, ok)
	require.Equal(t, 30*time.Second, c.step)
	require.Equal(t, 4, c.count)
	require.Equal(t, 2, c.totals[volInfo])
	require.Equal(t, 1, c.totals[volError])
	require.Equal(t, 1, c.totals[volWarn])
	require.Equal(t, 3, c.max, "the burst is three lines in one bucket")
	require.Len(t, c.bars, 3)
	require.Equal(t, 3, c.bars[0].total)
	require.Zero(t, c.bars[1].total, "the quiet half minute between them is a bucket too")
	require.Equal(t, 1, c.bars[2].total)
}

// TestVolumeRoundsASingleLineUp: one error in an hour of chatter is what the
// panel is for, and proportion alone would round it away.
func TestVolumeRoundsASingleLineUp(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	entries := []*logs.Entry{volEntry(at.Add(time.Minute), zapcore.ErrorLevel)}
	for range 400 {
		entries = append(entries, volEntry(at, zapcore.InfoLevel))
	}

	c, ok := bucketVolume(entries, 96, nil)
	require.True(t, ok)
	require.Equal(t, 400, c.max)

	quiet := c.segments(c.bars[len(c.bars)-1], volBars)
	require.Equal(t, 1, quiet[volError], "the lone error still reaches an eighth")

	rows := c.rows(96, volBars)
	require.Len(t, rows, volBars)
	require.Contains(t, rows[0], "█", "the busiest bucket reaches the top row")
}

// TestVolumeLeavesUndatedLinesOut: a line telescope only knows the arrival time
// of would draw a spike at the moment the view opened.
func TestVolumeLeavesUndatedLinesOut(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	arrived := volEntry(at, zapcore.InfoLevel)
	arrived.HasTime = false

	_, ok := bucketVolume([]*logs.Entry{arrived}, 96, nil)
	require.False(t, ok, "nothing dated is nothing to draw")

	note := volEntry(at, zapcore.InfoLevel)
	note.Kind = source.KindExited
	c, ok := bucketVolume([]*logs.Entry{volEntry(at, zapcore.InfoLevel), note, arrived}, 96, nil)
	require.True(t, ok)
	require.Equal(t, 1, c.count, "what telescope says about a source is not a log line")
}

// TestVolumeIsDrawnAboveTheList: the shape of a log is the first thing read
// about one, so it is there without being asked for — and folds away when the
// rows are wanted back.
func TestVolumeIsDrawnAboveTheList(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	m := timedModel(t,
		volLine(at, "info", "first"),
		volLine(at.Add(time.Second), "info", "second"),
		volLine(at.Add(2*time.Second), "error", "boom"),
	)

	out := screen(t, m)
	require.Contains(t, out, "peak 1 per 1s", "the scale the bars are drawn to")
	require.Contains(t, out, "info 2")
	require.Contains(t, out, "error 1")

	rows := strings.Split(out, "\n")
	panel := indexOfRow(rows, "peak 1 per 1s")
	first := indexOfRow(rows, "first")
	require.Positive(t, panel)
	require.Less(t, panel, first, "above the lines it counts")

	body := m.(Model).logs.bodyHeight()
	folded := send(t, m, k("v"))
	require.NotContains(t, screen(t, folded), "peak", "and folds away")
	require.Equal(t, body+volumeHeightOf(m), folded.(Model).logs.bodyHeight(),
		"giving the log list back its rows")
}

// TestVolumeMarksWhereTheCursorIs: the panel is above the list it belongs to,
// so it says which of its buckets is being read.
func TestVolumeMarksWhereTheCursorIs(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	var lines []source.Line
	for i := range 20 {
		// Distinct messages: a run of one line repeated is one row, and the
		// cursor would never leave it.
		lines = append(lines, volLine(at.Add(time.Duration(i)*time.Second), "info", fmt.Sprint("line ", i)))
	}
	m := timedModel(t, lines...)
	newest := markColumn(t, screen(t, m))
	require.Positive(t, newest, "following puts the cursor on the newest bucket")

	m = send(t, m, k("g"))
	require.Zero(t, markColumn(t, screen(t, m)), "and the mark walks back with the cursor")
}

// TestVolumeNeedsRoom: a panel on a short terminal would leave nothing to read.
func TestVolumeNeedsRoom(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	m := timedModel(t, volLine(at, "info", "line"))
	require.True(t, m.(Model).logs.volumeShown())

	m = send(t, m, tea.WindowSizeMsg{Width: 100, Height: volMinHeight - 1})
	require.False(t, m.(Model).logs.volumeShown(), "the toggle is remembered, the panel is not drawn")
	require.Zero(t, m.(Model).logs.volumeHeight())
}

func volEntry(at time.Time, level zapcore.Level) *logs.Entry {
	return &logs.Entry{
		At:      at,
		HasTime: true,
		Record:  logs.Record{Level: level, HasLevel: true},
	}
}

func volLine(at time.Time, level, msg string) source.Line {
	return source.Line{Data: []byte(`{"level":"` + level + `","msg":"` + msg + `"}`), At: at}
}

func volumeHeightOf(m tea.Model) int { return m.(Model).logs.volumeHeight() }

func indexOfRow(rows []string, text string) int {
	for i, r := range rows {
		if strings.Contains(r, text) {
			return i
		}
	}
	return -1
}

// markColumn is which bucket the cursor mark is on, counted from the left of
// the chart rather than of the screen.
func markColumn(t *testing.T, out string) int {
	t.Helper()
	for r := range strings.SplitSeq(out, "\n") {
		before, _, ok := strings.Cut(r, "▲")
		if !ok {
			continue
		}
		// Past the screen padding and the frame, in columns and not in bytes.
		return utf8.RuneCountInString(before) - screenPad - 1
	}
	t.Fatal("the cursor is not marked on the chart")
	return -1
}
