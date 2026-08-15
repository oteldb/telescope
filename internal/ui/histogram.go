package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/logs"
)

// The volume panel counts the lines the view is showing into buckets of time
// and stacks each bucket by severity. A list says what happened; it cannot say
// that ten times as much of it happened at twenty past, which is usually the
// thing being looked for.
//
// It counts what is held here rather than asking the source, so it covers the
// lines that have been read and no more: for a stream that is everything since
// the view opened, and for a database everything the query answered with, which
// is what the list is showing anyway. A source that could aggregate over the
// whole range would draw a different and larger picture; this one never
// disagrees with the rows underneath it.
const (
	// volBars is how many rows of bar the panel gets. Each row is eight steps of
	// block, so three rows resolve to a twenty-fourth of the peak — more than a
	// shape needs, and the rows come out of the log list.
	volBars = 3
	// volFrame is what the panel costs beyond its bars: the axis, the legend and
	// two rows of border.
	volFrame = 4
	// volMinHeight is the terminal below which the panel is not worth its rows.
	volMinHeight = 20
)

// volLevel is the severity band a line is counted in. Bands rather than levels:
// a bar eight steps tall cannot show seven severities apart, and panic and fatal
// are read for the same reason error is.
type volLevel int

const (
	volNone volLevel = iota
	volDebug
	volInfo
	volWarn
	volError
	volLevels
)

// volBandOf is the band a line is counted in. Unleveled lines are their own
// band rather than folded into info: a stream with no severities at all should
// say so, not claim to be entirely informational.
func volBandOf(e *logs.Entry) volLevel {
	if !e.Record.HasLevel {
		return volNone
	}
	switch l := e.Record.Level; {
	case l <= zapcore.DebugLevel:
		return volDebug
	case l == zapcore.InfoLevel:
		return volInfo
	case l == zapcore.WarnLevel:
		return volWarn
	default:
		return volError
	}
}

var volNames = [volLevels]string{"no level", "debug", "info", "warn", "error"}

// volStyleNone is dimmer than debug, which is already the dimmest thing the
// list draws: the two bands sit on top of each other in a bar and have to be
// told apart.
var volStyleNone = lipgloss.NewStyle().Foreground(colorBorder)

func volStyle(b volLevel) lipgloss.Style {
	switch b {
	case volDebug:
		return levelStyles[zapcore.DebugLevel]
	case volInfo:
		return levelStyles[zapcore.InfoLevel]
	case volWarn:
		return levelStyles[zapcore.WarnLevel]
	case volError:
		return levelStyles[zapcore.ErrorLevel]
	default:
		return volStyleNone
	}
}

// volBlocks fill a cell from empty to full in eighths, so a bar rises smoothly
// rather than a row at a time.
var volBlocks = []rune(" ▁▂▃▄▅▆▇█")

// volSteps are the bucket widths a chart is allowed to use: the ones a reader
// can do arithmetic in. Anything longer than a day is worked out as whole days.
var volSteps = []time.Duration{
	10 * time.Millisecond,
	20 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	15 * time.Second,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
	time.Hour,
	2 * time.Hour,
	3 * time.Hour,
	6 * time.Hour,
	12 * time.Hour,
	24 * time.Hour,
}

// volStep is the narrowest bucket that fits the span into cols columns. Narrow
// rather than round: the chart is drawn at the resolution the screen has, and a
// step chosen to be tidy would throw half the columns away.
func volStep(span time.Duration, cols int) time.Duration {
	cols = max(cols, 1)
	for _, s := range volSteps {
		if int(span/s)+1 <= cols {
			return s
		}
	}
	if cols == 1 {
		return span + 24*time.Hour
	}
	days := span/(24*time.Hour)/time.Duration(cols-1) + 1
	return days * 24 * time.Hour
}

// volBar is one bucket: when it starts and what was written during it.
type volBar struct {
	at    time.Time
	n     [volLevels]int
	total int
}

// volumeChart is the counted lines, ready to draw.
type volumeChart struct {
	step   time.Duration
	bars   []volBar
	max    int
	totals [volLevels]int
	count  int
	// cursor is the bucket the list's cursor is in, or -1 when it is on a line
	// the chart did not count.
	cursor int
}

// bucketVolume counts entries into at most cols buckets.
//
// Lines the source never dated are left out: their time is when telescope read
// them, so counting them would draw a spike at the moment the view opened
// rather than anything the log did. Same reason [gap] wants two real times.
// Telescope's own notes are left out for the plainer reason that they are not
// log lines.
func bucketVolume(entries []*logs.Entry, cols int, cursor *logs.Entry) (volumeChart, bool) {
	var first, last time.Time
	for _, e := range entries {
		if !volCounts(e) {
			continue
		}
		if first.IsZero() || e.At.Before(first) {
			first = e.At
		}
		if e.At.After(last) {
			last = e.At
		}
	}
	if first.IsZero() {
		return volumeChart{}, false
	}

	step := volStep(last.Sub(first), cols)
	from := volTruncate(first, step)
	c := volumeChart{
		step:   step,
		bars:   make([]volBar, int(last.Sub(from)/step)+1),
		cursor: -1,
	}
	for i := range c.bars {
		c.bars[i].at = from.Add(time.Duration(i) * step)
	}
	at := func(e *logs.Entry) int {
		return min(max(int(e.At.Sub(from)/step), 0), len(c.bars)-1)
	}
	for _, e := range entries {
		if !volCounts(e) {
			continue
		}
		b := &c.bars[at(e)]
		band := volBandOf(e)
		b.n[band]++
		b.total++
		c.totals[band]++
		c.count++
	}
	for _, b := range c.bars {
		c.max = max(c.max, b.total)
	}
	if cursor != nil && volCounts(cursor) {
		c.cursor = at(cursor)
	}
	return c, true
}

func volCounts(e *logs.Entry) bool { return e.HasTime && !e.Kind.IsNote() }

// volTruncate rounds a time down to a whole step of the local clock.
// [time.Time.Truncate] counts from the epoch, which is midnight UTC: it would
// put the boundary of an hour bucket at half past in India, and the axis under
// the chart is read in local time.
func volTruncate(t time.Time, step time.Duration) time.Time {
	local := t.Local()
	_, offset := local.Zone()
	shift := time.Duration(offset) * time.Second
	return local.Add(shift).Truncate(step).Add(-shift)
}

// segments splits one bar into the eighths each band gets.
//
// A bucket that caught anything at all is rounded up to one eighth: a single
// error in an hour of chatter is exactly what the panel is for, and it would
// round to nothing.
func (c volumeChart) segments(b volBar, height int) [volLevels]int {
	var out [volLevels]int
	if b.total == 0 || c.max == 0 {
		return out
	}
	full := height * 8
	filled := min((b.total*full+c.max-1)/c.max, full)

	// Each band gets the difference between the running count rounded to
	// eighths and the one before it, so the parts add up to the whole without
	// any of them being rounded twice.
	prev, run := 0, 0
	for l := range out {
		run += b.n[l]
		at := run * filled / b.total
		out[l] = at - prev
		prev = at
	}
	return out
}

// cell is how many columns one bucket is drawn in. A chart of twenty buckets
// across a wide screen is twenty bars and not twenty needles: the step is
// chosen to be a number a reader can do arithmetic in, and what is left over is
// spent making the bars wide enough to see.
func (c volumeChart) cell(width int) int {
	return max(width/max(len(c.bars), 1), 1)
}

// rows draws the bars, tallest bucket reaching the top row.
func (c volumeChart) rows(width, height int) []string {
	segs := make([][volLevels]int, len(c.bars))
	for i, b := range c.bars {
		segs[i] = c.segments(b, height)
	}

	cell := c.cell(width)
	rows := make([]string, height)
	for r := range height {
		// r counts down from the top row, base up from the bottom of the chart.
		base := (height - 1 - r) * 8
		var out strings.Builder
		for i := range c.bars {
			if (i+1)*cell > width {
				break
			}
			filled := 0
			for _, n := range segs[i] {
				filled += n
			}
			if filled <= base {
				out.WriteString(strings.Repeat(" ", cell))
				continue
			}
			fill := min(filled-base, 8)
			out.WriteString(volTint(segs[i], base, fill, i == c.cursor).
				Render(strings.Repeat(string(volBlocks[fill]), cell)))
		}
		rows[r] = out.String()
	}
	return rows
}

// volTint colors a cell by the band filling most of it, since a cell can only
// be one color and the band that owns most of it is the one that is true.
func volTint(seg [volLevels]int, from, n int, cursor bool) lipgloss.Style {
	band, most := volNone, 0
	at := 0
	for l, size := range seg {
		lo, hi := at, at+size
		at = hi
		// Ties go to the more severe band, which is the one worth seeing.
		if o := min(hi, from+n) - max(lo, from); o > 0 && o >= most {
			band, most = volLevel(l), o
		}
	}
	style := volStyle(band)
	if cursor {
		return style.Bold(true).Background(volCursorWash)
	}
	return style
}

// volCursorWash marks the bucket the cursor is reading inside, which is what
// ties the shape of the log to the line in front of it.
var volCursorWash = lipgloss.AdaptiveColor{Light: "#e1d8ff", Dark: "#382a5c"}

// axis writes the times along the bottom, and marks where the cursor is
// reading.
//
// Labels are spaced by whichever is wider: the room one needs, and the distance
// at which the next one reads differently. A chart of tenths of a second would
// otherwise write the same second out four times.
func (c volumeChart) axis(width int) string {
	layout, tick := "15:04", time.Minute
	switch {
	case c.step < time.Minute:
		layout, tick = "15:04:05", time.Second
	case c.step >= 24*time.Hour:
		layout, tick = "01-02", 24*time.Hour
	}
	cell := c.cell(width)
	every := max((len(layout)+2+cell-1)/cell, int((tick+c.step-1)/c.step))

	mark := -1
	if c.cursor >= 0 && c.cursor*cell < width {
		mark = c.cursor * cell
	}

	row := []byte(strings.Repeat(" ", width))
	for i := 0; i < len(c.bars); i += every {
		at := i * cell
		label := c.bars[i].at.Local().Format(layout)
		if at+len(label) > width {
			break
		}
		// A time with the mark struck through it reads as a typo, and the
		// bucket being read is worth more than any one of them.
		if mark >= at && mark < at+len(label) {
			continue
		}
		copy(row[at:], label)
	}
	if mark < 0 {
		return styleDim.Render(string(row))
	}
	return styleDim.Render(string(row[:mark])) +
		styleSelected.Render("▲") +
		styleDim.Render(string(row[mark+1:]))
}

// legend says what the colors are and how tall the peak is, which is the scale
// the bars are drawn to: without it a full-height bar could be four lines or
// four thousand.
func (c volumeChart) legend(width int) string {
	var parts []string
	for l := volLevels - 1; l >= volNone; l-- {
		if c.totals[l] == 0 {
			continue
		}
		parts = append(parts, volStyle(l).Render("▇ ")+
			styleDim.Render(fmt.Sprintf("%s %d", volNames[l], c.totals[l])))
	}
	parts = append(parts, styleDim.Render(fmt.Sprintf("peak %d per %s", c.max, volStepText(c.step))))
	return ansi.Truncate(strings.Join(parts, styleDim.Render(" · ")), width, "…")
}

// volStepText writes a bucket width the way it would be said out loud.
func volStepText(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

// volumeHeight is what the panel takes off the log list, and is worked out from
// the terminal alone: a height that depended on what has arrived would move the
// list every time a line did.
func (m logModel) volumeHeight() int {
	if !m.volumeShown() {
		return 0
	}
	return volBars + volFrame
}

// volumeShown reports whether the panel is drawn. It needs lines that carry
// their own time for the same reason the time column does — see [bucketVolume]
// — so it is off for a command that writes bare text, whatever the toggle says.
func (m logModel) volumeShown() bool {
	return m.volume && m.cols.time && m.h >= volMinHeight
}

// volumePanel draws the panel over the entries the view is showing, so it
// answers for the filter in force rather than for everything read.
func (m logModel) volumePanel(entries []*logs.Entry, cursor *logs.Entry) string {
	inner := max(m.width()-2, 10)
	rows := make([]string, 0, volBars+2)

	c, ok := bucketVolume(entries, inner, cursor)
	switch {
	case !ok:
		for range volBars {
			rows = append(rows, "")
		}
		rows = append(rows, "", styleDim.Render("no dated lines to count"))
	default:
		rows = append(rows, c.rows(inner, volBars)...)
		rows = append(rows, c.axis(inner), c.legend(inner))
	}
	return styleBox.Width(m.width()).Render(strings.Join(rows, "\n"))
}
