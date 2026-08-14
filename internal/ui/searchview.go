package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
	"github.com/oteldb/telescope/internal/trace"
)

// searchStamp is how a result's start is written. It is absolute rather than an
// age, unlike the log list's: a search is a window somebody chose, and "2m ago"
// against a window that ended yesterday says nothing.
const searchStamp = "2006-01-02 15:04:05"

func (m searchModel) View() string {
	width, height := m.bodyWidth(), m.bodyHeight()

	rows := m.formRows(width)
	rows = append(rows, "")
	rows = append(rows, m.status(width))

	body := m.list(width, height-len(rows))
	if names := m.suggestions(); len(names) > 0 {
		// Drawn over the results rather than beside them, for the reason the
		// service picker is drawn over the gantt: a terminal has no room for a
		// second column, and the list is read once and dismissed.
		body = m.suggestionList(names, width)
	}
	rows = append(rows, body...)

	for len(rows) < height {
		rows = append(rows, "")
	}
	frame := styleBox.Width(width).Render(strings.Join(rows[:height], "\n"))
	return padScreen(m.head() + "\n" + frame + "\n" + ansi.Truncate(m.footer(), m.w, ""))
}

// head names the store being searched and which API answers there. Where a
// trace comes from is half of what its id means, so it is said where the search
// is typed rather than only in the config.
func (m searchModel) head() string {
	api := string(source.CollectorTempo)
	if m.at.Collector == source.CollectorJaeger {
		api = string(source.CollectorJaeger)
	}
	return styleTitle.Render("search traces") + " " +
		styleChipWhere.Render(logs.Sanitize(m.at.Label())) + styleChip.Render(api)
}

func (m searchModel) formRows(width int) []string {
	rows := make([]string, 0, searchFields)
	for f := range searchFields {
		label := styleLabel.Render(padRight(f.label(), labelWidth))
		if m.focus == int(f) {
			label = styleSelected.Render(padRight(f.label(), labelWidth))
		}
		rows = append(rows, ansi.Truncate(label+" "+m.inputs[f].View(), width, "…"))
	}
	return rows
}

// status is the line between the form and the results: what the last search
// did, or what is wrong with the one being typed.
func (m searchModel) status(width int) string {
	var line string
	switch {
	case m.note != "":
		line = styleOK.Render(m.note)
	case m.err != nil:
		line = styleErr.Render(logs.Sanitize(m.err.Error()))
	case m.searching:
		line = styleDim.Render("searching …")
	case !m.ran:
		line = styleDim.Render("fill in what you know and press enter")
	case len(m.results) == 0:
		// What was asked is repeated back, since the answer is that nothing
		// matched it and the question is the only thing left to doubt.
		line = styleDim.Render("no traces matched ") + styleHint.Render(m.askedText())
	default:
		line = styleDim.Render(fmt.Sprintf("%d traces ", len(m.results))) +
			styleHint.Render(m.askedText())
	}
	return ansi.Truncate(line, width, "…")
}

// askedText says what the results answered, in the store's own language: the
// window, and the query it was narrowed by.
func (m searchModel) askedText() string {
	parts := []string{"in " + m.asked.Range.Label()}
	if !m.asked.IsZero() && m.at.Collector != source.CollectorJaeger {
		parts = append(parts, m.asked.TraceQL())
	}
	return "· " + strings.Join(parts, " · ")
}

func (m searchModel) list(width, height int) []string {
	height = max(height, 1)
	if len(m.results) == 0 {
		return nil
	}

	from := min(m.off, max(len(m.results)-1, 0))
	to := min(from+height, len(m.results))
	rows := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		row := ansi.Truncate(m.row(m.results[i]), width, styleDim.Render("…"))
		if i == m.cursor && m.focus == focusResults {
			row = cursorRow(row, width)
		}
		rows = append(rows, row)
	}
	return rows
}

// row draws one result: when it ran, who ran it, what it was, how long it took,
// and how much of it there is.
//
// The two counts are drawn as the different claims they are. Jaeger answers a
// search with the traces, so it can say a trace has thirty-eight spans and two
// of them failed; Tempo answers with a summary and can only say how many spans
// its query selected. A column that meant whichever the store happened to
// report would be a number nobody could read.
func (m searchModel) row(r trace.Result) string {
	parts := []string{styleDim.Render(r.Start.Local().Format(searchStamp))}

	name := logs.Sanitize(r.Name)
	if name == "" {
		name = "—"
	}
	if service := logs.Sanitize(r.Service); service != "" {
		parts = append(parts, styleSelected.Render(service))
	}
	parts = append(parts, name, styleDim.Render(padLeft(humanDur(r.Duration), 8)))

	switch {
	case r.Spans > 0:
		spans := fmt.Sprintf("%d spans", r.Spans)
		if r.Errors > 0 {
			spans += " " + styleErr.Render(fmt.Sprintf("✗%d", r.Errors))
		}
		parts = append(parts, styleDim.Render(spans))
	case r.Matched > 0:
		parts = append(parts, styleDim.Render(fmt.Sprintf("%d matched", r.Matched)))
	}
	parts = append(parts, styleTrace.Render(shortID(r.TraceID)))
	return strings.Join(parts, "  ")
}

// shortID is as much of a trace id as tells one row from another. The whole of
// it is what `y` copies: an id is carried rather than read, and sixteen bytes
// of hex across a row is a column of noise.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12] + "…"
	}
	return id
}

func (m searchModel) suggestionList(names []string, width int) []string {
	rows := make([]string, 0, len(names)+1)
	rows = append(rows, styleDim.Render(searchField(m.focus).label()+" the store knows"))
	for i, name := range names {
		row := "  " + logs.Sanitize(name)
		if i == m.sug {
			row = cursorRow(row, width)
		}
		rows = append(rows, ansi.Truncate(row, width, "…"))
	}
	return rows
}

func (m searchModel) footer() string {
	if m.focus == focusResults {
		return styleHint.Render(searchResultKeys)
	}
	if len(m.suggestions()) > 0 {
		return styleHint.Render(searchSuggestKeys)
	}
	return styleHint.Render(searchFormKeys)
}

const (
	searchFormKeys    = "enter search · tab field · ctrl+r again · esc back"
	searchSuggestKeys = "↑↓ suggestions · enter accept · esc dismiss · tab field"
	searchResultKeys  = "enter open · ↑↓ move · y copy id · ctrl+r again · esc edit search"
)

// searchTimeout bounds a search. It is longer than a trace fetch because a
// store is being asked to look through a window rather than to hand over one
// document it has by id, and shorter than nothing at all because a search that
// never answers is a screen that never says why.
const searchTimeout = time.Minute
