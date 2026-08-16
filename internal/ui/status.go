package ui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
)

// topBar carries what the list is not showing as well as what it is: folded is
// how many lines the clamp took out of it, and a list that quietly stood for
// four hundred lines with one row would be lying about the log.
//
// The segments are ordered by what a reader would rather keep when the terminal
// is narrow, since the line is truncated from the right: what is holding lines
// back — the clamp, the level, the query — comes first, and the span goes last
// because it is the widest segment and the one the rows below already say.
func (m logModel) topBar(entries []*logs.Entry, folded int) string {
	title := styleTitle.Render(m.cfg.Title())

	stats := []string{
		stat(len(entries), "shown"),
		stat(m.store.Len(), "lines"),
	}
	if d := m.store.Dropped(); d > 0 {
		stats = append(stats, stat(d, "dropped"))
	}
	stats = append(stats, toggle("clamp", m.clamped))
	if m.clamped && folded > 0 {
		stats = append(stats, stat(folded, "clamped"))
	}
	stats = append(stats, levelStat(m.view.Filter().MinLevel), m.filterStat())
	stats = append(stats, toggle("follow", m.follow))
	if older := m.olderText(); older != "" {
		stats = append(stats, older)
	}
	stats = append(stats, m.statusText())
	if r := timeRange(entries); r != "" {
		stats = append(stats, r)
	}

	inner := max(m.width()-2, 1)
	line := strings.Join(stats, styleDim.Render(" · "))
	return styleBox.Width(m.width()).Render(
		ansi.Truncate(title, inner, "…") + "\n" + ansi.Truncate(line, inner, "…"),
	)
}

// stat writes a count and the word for it.
func stat(n int, word string) string {
	return styleStat.Render(strconv.Itoa(n)) + " " + styleDim.Render(word)
}

// toggle writes whether something is on, which is what the reader is asking of
// it — a clamp that folded nothing yet is still a clamp that is on.
func toggle(name string, on bool) string {
	state := styleDim.Render("off")
	if on {
		state = styleOn.Render("on")
	}
	return styleDim.Render(name+" ") + state
}

// levelStat writes the minimum severity the view admits, in that severity's own
// color: the same color it reads in down the gutter, so "warn and above" and the
// warnings it left on screen are recognizably about the same thing.
func levelStat(l logs.MinLevel) string {
	if l == logs.LevelAll {
		return styleDim.Render("level all")
	}
	style, ok := levelStyles[l.Level()]
	if !ok {
		style = styleDim
	}
	return styleDim.Render("level ≥") + style.Bold(true).Render(l.String())
}

// filterStat writes the query, or says there is none. The level is not part of
// it: it stands beside this as its own segment, in its own color.
func (m logModel) filterStat() string {
	f := m.view.Filter()
	switch {
	case f.Err() != nil:
		return styleErr.Render(f.Describe())
	case f.Expr() == nil:
		return styleDim.Render("no filter")
	default:
		return styleDim.Render("filter ") + styleFilter.Render(f.Describe())
	}
}

func (m logModel) statusText() string {
	switch {
	case m.err != nil:
		return styleErr.Render(m.status + ": " + m.err.Error())
	case m.status == "streaming":
		return styleOK.Render(m.status)
	default:
		return styleDim.Render(m.status)
	}
}

// timeRange summarizes the time span the visible entries cover. It is the least
// and the greatest of their times rather than the first and the last: a stream
// that reads several pods at once hands them over in whatever order they were
// written, and a range printed in that order runs backwards.
func timeRange(entries []*logs.Entry) string {
	var from, to time.Time
	for _, e := range entries {
		switch {
		case e.At.IsZero():
		case from.IsZero():
			from, to = e.At, e.At
		case e.At.Before(from):
			from = e.At
		case e.At.After(to):
			to = e.At
		}
	}
	if from.IsZero() {
		return ""
	}
	const layout = "15:04:05"
	format := layout
	if !from.Truncate(24 * time.Hour).Equal(to.Truncate(24 * time.Hour)) {
		format = "01-02 " + layout
	}
	return styleStat.Render(from.Local().Format(format)) +
		styleDim.Render(" → ") +
		styleStat.Render(to.Local().Format(format))
}
