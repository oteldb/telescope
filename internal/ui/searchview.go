package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/complete"
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

	free := height - len(rows)
	body := m.list(width, free)
	if names := m.suggestions(); len(names) > 0 {
		// Drawn over the results rather than beside them, for the reason the
		// service picker is drawn over the gantt: a terminal has no room for a
		// second column, and the list is read once and dismissed.
		body = m.suggestionList(names, width, free)
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
	window := m.results[from:to]
	cols := searchColumns(window, width)

	rows := make([]string, 0, to-from)
	for i, r := range window {
		row := ansi.Truncate(m.row(r, cols), width, styleDim.Render("…"))
		if from+i == m.cursor && m.focus == focusResults {
			row = cursorRow(row, width)
		}
		rows = append(rows, row)
	}
	return rows
}

// searchCols is how wide each column of the result table is.
//
// They are measured over the rows on screen rather than over the whole answer,
// so a page of short names is not read across a column sized for one long name
// somewhere below it — and remeasured as the list scrolls, which is what keeps
// the columns tight to what is being read.
type searchCols struct{ service, name, count, id int }

// searchColumns measures the table. Everything but the name has a width the
// content decides; the name takes what is left, since it is the one field of a
// trace that has no length anybody agreed on.
func searchColumns(window []trace.Result, width int) searchCols {
	var c searchCols
	name := 0
	for _, r := range window {
		c.service = max(c.service, ansi.StringWidth(logs.Sanitize(r.Service)))
		c.count = max(c.count, ansi.StringWidth(countText(r)))
		c.id = max(c.id, ansi.StringWidth(r.TraceID))
		name = max(name, ansi.StringWidth(logs.Sanitize(r.Name)))
	}
	fixed := len(searchStamp) + durWidth + 3*gapWidth
	if c.service > 0 {
		fixed += c.service + gapWidth
	}
	if c.count > 0 {
		fixed += c.count + gapWidth
	}
	// The whole id where the row can hold it and still name what it is, and as
	// much of it as tells rows apart where it cannot. An id is carried rather
	// than read — `y` is what it is for — so a column of hex crowding out the
	// operation would be the row spending its width on the thing nobody is
	// scanning.
	if fixed+name+c.id > width {
		c.id = min(c.id, idWidth)
	}
	c.name = max(min(name, width-fixed-c.id), 1)
	return c
}

const (
	// idWidth is how much of a trace id tells one row from another, for a table
	// with no room for all of it, and gapWidth the space between two columns.
	idWidth  = 13
	gapWidth = 2
)

// row draws one result: when it ran, who ran it, what it was, how long it took,
// and how much of it there is.
func (m searchModel) row(r trace.Result, c searchCols) string {
	gap := strings.Repeat(" ", gapWidth)
	cells := []string{styleDim.Render(r.Start.Local().Format(searchStamp))}
	if c.service > 0 {
		cells = append(cells, styleSelected.Render(padRight(logs.Sanitize(r.Service), c.service)))
	}

	name := logs.Sanitize(r.Name)
	if name == "" {
		name = "—"
	}
	cells = append(cells,
		padRight(ansi.Truncate(name, c.name, "…"), c.name),
		styleDim.Render(padLeft(humanDur(r.Duration), durWidth)),
	)
	if c.count > 0 {
		cells = append(cells, padRight(m.countCell(r), c.count))
	}
	return strings.Join(append(cells, styleTrace.Render(shortID(r.TraceID, c.id))), gap)
}

// countText is how much of the trace there is, as plain text for measuring.
//
// The two stores count different things, and the column says which. Jaeger
// answers a search with the traces, so it can say a trace has thirty-eight
// spans and two of them failed; Tempo answers with a summary and can only say
// how many spans its query selected. A column that meant whichever the store
// happened to report would be a number nobody could read.
func countText(r trace.Result) string {
	switch {
	case r.Spans > 0 && r.Errors > 0:
		return fmt.Sprintf("%d spans ✗%d", r.Spans, r.Errors)
	case r.Spans > 0:
		return fmt.Sprintf("%d spans", r.Spans)
	case r.Matched > 0:
		return fmt.Sprintf("%d matched", r.Matched)
	default:
		return ""
	}
}

func (m searchModel) countCell(r trace.Result) string {
	if r.Spans > 0 && r.Errors > 0 {
		return styleDim.Render(fmt.Sprintf("%d spans ", r.Spans)) +
			styleErr.Render(fmt.Sprintf("✗%d", r.Errors))
	}
	return styleDim.Render(countText(r))
}

// shortID is as much of a trace id as the column has room for. `y` copies the
// whole of it either way, which is what an id is for.
func shortID(id string, width int) string {
	if width < 2 || len(id) <= width {
		return id
	}
	return id[:width-1] + "…"
}

// suggestionList draws what the store offers under the field, scrolled to keep
// the highlighted one on screen and cut off with a count of what did not fit
// rather than silently. The list is what says whether the thing being looked
// for exists at all, so a store with four hundred operations must not read as
// one with six.
func (m searchModel) suggestionList(names []complete.Candidate, width, height int) []string {
	if height <= 1 {
		return nil
	}

	shown := min(len(names), height-1)
	if shown < len(names) {
		// A row goes to saying how many were left out.
		shown = max(shown-1, 1)
	}
	from := 0
	if m.sug >= shown {
		from = m.sug - shown + 1
	}

	rows := make([]string, 0, shown+2)
	rows = append(rows, styleDim.Render(m.suggestTitle()))
	for i, c := range names[from : from+shown] {
		selected := from+i == m.sug
		marker := "  "
		if selected {
			marker = styleSelected.Render("▎ ")
		}
		row := marker + highlightMatch(logs.Sanitize(c.Value), c.Matched, selected)
		if selected {
			row = cursorRow(row, width)
		}
		rows = append(rows, ansi.Truncate(row, width, "…"))
	}
	if rest := len(names) - from - shown; rest > 0 {
		rows = append(rows, styleHint.Render("  … "+strconv.Itoa(rest)+" more"))
	}
	return rows
}

// suggestTitle says what is being offered. The tag field is two lists under one
// label — the keys, then what one of them has been — and a heading that said
// "tags" for both would leave the reader working out which arrived.
func (m searchModel) suggestTitle() string {
	if searchField(m.focus) == fieldTags {
		if at := m.tagAt(); at.Key != "" {
			return logs.Sanitize(at.Key) + " values the store knows"
		}
		return "tag keys the store knows"
	}
	return searchField(m.focus).label() + " the store knows"
}

func (m searchModel) footer() string {
	switch {
	case m.focus == focusResults:
		return styleHint.Render(searchResultKeys)
	case m.sug >= 0:
		return styleHint.Render(searchSuggestKeys)
	case len(m.suggestions()) > 0:
		return styleHint.Render(searchOfferKeys)
	default:
		return styleHint.Render(searchFormKeys)
	}
}

const (
	searchFormKeys    = "enter search · ↑↓ field · ctrl+r again · esc back"
	searchOfferKeys   = "ctrl+n suggestions · enter search · ↑↓ field · esc back"
	searchSuggestKeys = "↑↓ suggestions · enter accept · esc back to the field"
	searchResultKeys  = "enter open · ↑↓ move · y copy id · ctrl+r again · esc edit search"
)

// searchTimeout bounds a search. It is longer than a trace fetch because a
// store is being asked to look through a window rather than to hand over one
// document it has by id, and shorter than nothing at all because a search that
// never answers is a screen that never says why.
const searchTimeout = time.Minute
