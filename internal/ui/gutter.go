package ui

import (
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/oteldb/telescope/internal/logs"
)

// timeMode is how the time column is written. It is the view's and not the
// entry's: the same line is worth reading as a clock time while following a
// burst and as a date once the list covers days, and pressing "t" is cheaper
// than deciding up front.
type timeMode int

const (
	// timeClock is the time within the day, which is what a list of log lines
	// is usually read for: the date is in the header and the order of things
	// inside a second is not.
	timeClock timeMode = iota
	// timeFull is the whole instant, date and offset included, which is what
	// gets pasted into a ticket or another tool's query.
	timeFull
	// timeAge is how long before the view opened, so a line reads as "eight
	// minutes before I looked" without arithmetic.
	timeAge
)

func (m timeMode) next() timeMode {
	if m == timeAge {
		return timeClock
	}
	return m + 1
}

// Time layouts, and the widths the column is padded to so the text beside it
// lines up whatever each line turned out to be.
const (
	clockLayout = "15:04:05.000"
	// The offset is written out rather than left as Z, since a stamp that is
	// copied elsewhere has to say which zone it was read in.
	fullLayout = "2006-01-02T15:04:05.000-07:00"
	ageWidth   = 8
)

func (m timeMode) width() int {
	switch m {
	case timeFull:
		return len(fullLayout)
	case timeAge:
		return ageWidth
	default:
		return len(clockLayout)
	}
}

// stamp writes at the way the mode says, padded to the mode's width. origin is
// when the view opened, which is what an age is measured against: it is fixed
// when the lines are fetched rather than at every redraw, so a list that is
// being read does not renumber itself under the reader.
func (m timeMode) stamp(at, origin time.Time) string {
	var out string
	switch m {
	case timeFull:
		out = at.Local().Format(fullLayout)
	case timeAge:
		out = humanAge(at.Sub(origin))
	default:
		out = at.Local().Format(clockLayout)
	}
	return fmt.Sprintf("%-*s", m.width(), out)
}

// humanAge writes a distance from when the view opened, signed: a line older
// than the query is behind it and one that arrived since is ahead.
func humanAge(d time.Duration) string {
	sign := "-"
	if d >= 0 {
		sign = "+"
		if d < time.Second {
			// Everything the query itself returned is "now" to within the round
			// trip that fetched it, and a column of +0.0s says nothing.
			return "now"
		}
	}
	d = d.Abs()
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%s%.1fs", sign, d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%s%dm%02ds", sign, int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%s%dh%02dm", sign, int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%s%dd%02dh", sign, int(d.Hours())/24, int(d.Hours())%24)
	}
}

// traceGlyph marks a line that was written inside a trace. A trace id is
// thirty-two characters nobody reads, so what the list shows is that there is
// one: the id itself is a keystroke away in the entry, and the color is what
// says two lines belong to the same request.
const traceGlyph = "◆"

// traceMark is the trace column of one entry, blank for a line outside a trace.
func traceMark(e *logs.Entry) string {
	if e.Record.TraceID == "" {
		return " "
	}
	return traceStyle(e.Record.TraceID).Render(traceGlyph)
}

// traceStyle colors a trace by its id, so the lines of one request carry the
// same mark wherever they are in the list and whichever source they came from.
//
// The palette is small, so two traces on screen can share a color. That is the
// trade: a mark that is right about "these are the same" often enough to be
// worth following, and never asked to be proof — narrowing by the id is.
func traceStyle(id string) lipgloss.Style {
	h := fnv.New32a()
	_, _ = io.WriteString(h, id)
	return tagStyle(int(h.Sum32() % uint32(len(tagColors))))
}

// gapAfter is how far apart two lines have to be for the seam between them to
// be worth drawing as more than a shading change. A minute of nothing in a log
// that was writing every second is the thing a reader is looking for, and a
// list that only shaded it makes them work out how long it lasted.
const gapAfter = time.Minute

// gap reports the silence between two entries, if there was one worth marking.
func gap(prev, next *logs.Entry) (time.Duration, bool) {
	if prev == nil || next == nil || !prev.HasTime || !next.HasTime {
		return 0, false
	}
	d := next.At.Sub(prev.At)
	return d, d >= gapAfter
}

// gapRow draws the silence between two lines: how long it lasted, and the
// instant the log picks up again, which is the one thing the clock column
// cannot say once a list covers more than a day.
func gapRow(d time.Duration, at time.Time, width int) string {
	label := fmt.Sprintf("%s of silence · %s ", humanGap(d), at.Local().Format(fullLayout))
	rule := strings.Repeat("─", max(width-len(label)-4, 0))
	return styleDim.Render("  ── " + label + rule)
}

// humanGap writes how long a silence lasted, at the resolution it is worth
// reading at: the seconds of an eight-hour gap are noise.
func humanGap(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
