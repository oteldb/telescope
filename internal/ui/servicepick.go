package ui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/trace"
)

// servicePick is the list of services a trace touched, with the ones filtered
// out marked. It is drawn over the gantt rather than beside it: a terminal has
// no room for a sidebar, and the list is read once and dismissed.
type servicePick struct {
	names  []string
	counts map[string]int
	cursor int
}

func newServicePick(t *trace.Tree) servicePick {
	counts := t.Services()
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	// By how much of the trace each one is, then by name. The service that ran
	// most of the request is the one most likely to be turned off to see the
	// rest, so it is not worth making anybody hunt for it.
	slices.SortFunc(names, func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		return strings.Compare(a, b)
	})
	return servicePick{names: names, counts: counts}
}

func (p *servicePick) move(d int) {
	if len(p.names) == 0 {
		return
	}
	p.cursor = min(max(p.cursor+d, 0), len(p.names)-1)
}

func (p servicePick) at() (string, bool) {
	if p.cursor < 0 || p.cursor >= len(p.names) {
		return "", false
	}
	return p.names[p.cursor], true
}

// View draws the picker. hidden is what the gantt is filtering out, and palette
// colors each name as its spans are drawn, since the color is what the eye is
// actually going to use to find it again on the chart.
func (p servicePick) View(hidden map[string]bool, palette servicePalette, width, height int) string {
	rows := []string{styleTitle.Render("services") + styleDim.Render("  space toggles · a all · esc back")}

	// The count column is as wide as the largest number, so the names line up.
	countWidth := 1
	for _, n := range p.counts {
		countWidth = max(countWidth, len(fmt.Sprint(n)))
	}

	for i, name := range p.names {
		mark := styleOK.Render("✓")
		if hidden[name] {
			mark = styleDim.Render("·")
		}
		label := palette.style(name).Render(logs.Sanitize(name))
		if hidden[name] {
			label = styleDim.Render(logs.Sanitize(name))
		}
		row := fmt.Sprintf("%s %s %s",
			mark,
			padLeft(styleDim.Render(fmt.Sprint(p.counts[name])), countWidth),
			label)
		row = ansi.Truncate(row, width, styleDim.Render("…"))
		if i == p.cursor {
			row = cursorRow(row, width)
		}
		rows = append(rows, row)
	}
	if len(rows) > height {
		rows = rows[:max(height, 1)]
	}
	return strings.Join(rows, "\n")
}
