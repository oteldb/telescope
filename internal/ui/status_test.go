package ui

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/logs"
)

// TestTheStatusBarColorsItsFigures: the counts are what the line is read for,
// and dim numerals in a dim sentence are not read at all.
func TestTheStatusBarColorsItsFigures(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","msg":"started"}`,
		`{"level":"warn","msg":"slow"}`,
	)
	out := m.View()

	require.Contains(t, out, styleStat.Render("2")+" "+styleDim.Render("shown"))
	require.Contains(t, out, styleStat.Render("2")+" "+styleDim.Render("lines"))
	require.Contains(t, ansi.Strip(out), "2 shown · 2 lines")
}

// TestTheStatusBarSaysWhetherTheClampIsOn: a count of what the clamp folded is
// not an answer to whether it is still folding.
func TestTheStatusBarSaysWhetherTheClampIsOn(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","msg":"retrying"}`,
		`{"level":"info","msg":"retrying"}`,
	)
	require.Contains(t, m.View(), styleDim.Render("clamp ")+styleOn.Render("on"))
	require.Contains(t, ansi.Strip(m.View()), "clamp on · 1 clamped")

	m = send(t, m, k("c"))
	require.Contains(t, m.View(), styleDim.Render("clamp ")+styleDim.Render("off"))
	require.NotContains(t, ansi.Strip(m.View()), "clamped", "nothing is being folded")
}

// TestFollowSaysWhetherItIsOn: the same for the tail, which is the toggle a
// reader is most often unsure about.
func TestFollowSaysWhetherItIsOn(t *testing.T) {
	m := logsModel(t, `{"level":"info","msg":"started"}`)
	require.Contains(t, m.View(), styleDim.Render("follow ")+styleOn.Render("on"))

	m = send(t, m, k("f"))
	require.Contains(t, m.View(), styleDim.Render("follow ")+styleDim.Render("off"))
}

// TestTheLevelFilterIsAMinimum: "warn" means warn and above, not warn alone.
func TestTheLevelFilterIsAMinimum(t *testing.T) {
	m := logsModel(t,
		`{"level":"debug","msg":"tick"}`,
		`{"level":"info","msg":"started"}`,
		`{"level":"warn","msg":"slow"}`,
		`{"level":"error","msg":"exploded"}`,
	)
	require.Contains(t, ansi.Strip(m.View()), "level all", "nothing is held back yet")

	// all → info → warn.
	m = send(t, m, k("l"), k("l"))
	out := ansi.Strip(m.View())
	require.Contains(t, out, "2 shown")
	require.Contains(t, out, "slow")
	require.Contains(t, out, "exploded")
	require.NotContains(t, out, "started")

	// error, then round to all again.
	m = send(t, m, k("l"))
	require.Contains(t, ansi.Strip(m.View()), "1 shown")
	m = send(t, m, k("l"))
	require.Contains(t, ansi.Strip(m.View()), "4 shown")
}

// TestTheLevelFilterIsWrittenInItsOwnColor: the level held onto reads in the
// color the list and the gutter give it, so the bar and the rows agree.
func TestTheLevelFilterIsWrittenInItsOwnColor(t *testing.T) {
	m := logsModel(t, `{"level":"warn","msg":"slow"}`)
	require.Contains(t, m.View(), styleDim.Render("level all"), "and off is quiet")

	m = send(t, m, k("l"), k("l"))
	require.Contains(t, m.View(),
		styleDim.Render("level ≥")+levelStyles[zapcore.WarnLevel].Bold(true).Render("WARN"))

	m = send(t, m, k("l"))
	require.Contains(t, m.View(),
		styleDim.Render("level ≥")+levelStyles[zapcore.ErrorLevel].Bold(true).Render("ERROR"))
}

// TestTheRangeReadsForwards: a stream that reads several pods at once hands
// them over interleaved, so the first entry held is not the earliest written.
func TestTheRangeReadsForwards(t *testing.T) {
	at := time.Date(2026, 8, 11, 1, 25, 23, 0, time.Local)
	require.Equal(t, "01:21:23 → 01:26:23", ansi.Strip(timeRange([]*logs.Entry{
		{At: at},
		{At: at.Add(-4 * time.Minute)},
		{At: at.Add(time.Minute)},
	})))
}

// TestTheRangeSkipsWhatIsNotDated: a line nobody stamped says nothing about
// when the log starts.
func TestTheRangeSkipsWhatIsNotDated(t *testing.T) {
	at := time.Date(2026, 8, 11, 1, 25, 23, 0, time.Local)
	require.Equal(t, "01:25:23 → 01:25:24", ansi.Strip(timeRange([]*logs.Entry{
		{},
		{At: at.Add(time.Second)},
		{At: at},
	})))
	require.Empty(t, timeRange(nil))
	require.Empty(t, timeRange([]*logs.Entry{{}, {}}))
}
