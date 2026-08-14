package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/trace"
)

// Zoom is by halves rather than by a fixed span of time: a trace may be a
// millisecond or a minute, and the only thing a keystroke can mean across both
// is "half as much as I was looking at".
const (
	zoomIn  = 0.5
	zoomOut = 2
	// panStep is a fraction of the window, so a keystroke moves the same
	// visible distance however far in somebody has zoomed.
	panStep = 0.15
)

// What the trace screen is showing. The gantt is the view; the other two are
// read over it and dismissed, which is why they are a mode here rather than
// screens of their own in the root model.
type traceMode int

const (
	traceGantt traceMode = iota
	traceSpan
	traceServices
)

// traceModel is one trace on screen.
//
// Everything it knows how to draw is in [gantt]; this is the keys, the frame,
// and the line under it that says what the cursor is on. The split is the one
// the log list makes with its store: what a thing looks like is worked out
// where the data is, and the model is what a reader does to it.
type traceModel struct {
	w, h int
	g    *gantt
	mode traceMode

	// pick is the service filter, built once since a trace's services do not
	// change while it is being read.
	pick servicePick
	// sel is which row of the span detail the cursor is on, and off is where
	// that document is scrolled to.
	sel, off int

	// loading is a fetch that has not answered yet, and err is why one did not.
	// A trace asked for by id is a request over the network, so both are states
	// the screen has to have.
	loading string
	err     error

	// note is what the last copy did, shown until the next key. It is the only
	// evidence a copy leaves: the clipboard is the terminal's and cannot be
	// read back.
	note string
}

func newTrace(t *trace.Tree) traceModel {
	return traceModel{g: newGantt(t), pick: newServicePick(t)}
}

// loadingTrace is the screen while a trace is being fetched, so that pressing
// a key over a log line answers immediately with something rather than
// appearing to do nothing until the network does.
func loadingTrace(id string) traceModel {
	return traceModel{loading: id}
}

func (m *traceModel) resize(w, h int) { m.w, m.h = w, h }

// bodyHeight is what is left inside the frame, after the border and the key
// line under it.
func (m traceModel) bodyHeight() int { return max(m.h-4, 1) }

func (m traceModel) bodyWidth() int { return max(m.w-2*screenPad-2, 20) }

func (m traceModel) Update(msg tea.Msg) (traceModel, tea.Cmd) {
	if msg, ok := msg.(noteMsg); ok {
		m.note = msg.text
		return m, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	m.note = ""

	// Nothing to steer until there is a trace: any key leaves.
	if m.g == nil {
		switch km.String() {
		case "q", "ctrl+c":
			return m, func() tea.Msg { return quitMsg{} }
		case "esc", "backspace", "enter":
			return m, func() tea.Msg { return backMsg{} }
		}
		return m, nil
	}

	switch m.mode {
	case traceServices:
		return m.updateServices(km)
	case traceSpan:
		return m.updateSpan(km)
	default:
		return m.updateGantt(km)
	}
}

func (m traceModel) updateGantt(km tea.KeyMsg) (traceModel, tea.Cmd) {
	switch km.String() {
	case "q":
		return m, func() tea.Msg { return quitMsg{} }
	case "esc", "backspace":
		return m, func() tea.Msg { return backMsg{} }

	case "up", "k":
		m.g.move(-1)
	case "down", "j":
		m.g.move(1)
	case "pgup":
		m.g.move(-m.bodyHeight())
	case "pgdown":
		m.g.move(m.bodyHeight())
	case "home", "g":
		m.g.move(-len(m.g.rows))
	case "end", "G":
		m.g.move(len(m.g.rows))

	case " ", "tab":
		m.g.toggle()
	case "C":
		m.g.collapseAll()
	case "E":
		m.g.expandAll()

	case "enter":
		if m.g.at() != nil {
			m.mode, m.sel, m.off = traceSpan, 0, 0
		}
	case "s":
		m.mode = traceServices

	case "+", "=":
		m.g.zoom(zoomIn)
	case "-", "_":
		m.g.zoom(zoomOut)
	case "left", "h":
		m.g.pan(-panStep)
	case "right", "l":
		m.g.pan(panStep)
	case "z":
		m.g.focus()
	case "0":
		m.g.fit()

	case "f":
		// The reverse of the jump that got here: every line written anywhere
		// inside this request. The trace and not the span under the cursor,
		// since that is the question a chart is being read to ask — the span
		// view narrows by whichever row is selected.
		return m, narrowLogs(query.Field{
			Key: "trace_id", Op: query.OpEq, Value: m.g.tree.ID,
		})

	case "r":
		// What is on screen is what the trace store said when it was asked, and a
		// request still being served will have grown since. The reader is the only
		// one who knows whether it has.
		return m, func() tea.Msg { return reloadTraceMsg{} }

	case "y":
		if n := m.g.at(); n != nil {
			return m, copyCmd("span_id", n.SpanID)
		}
	case "Y":
		return m, copyCmd("trace_id", m.g.tree.ID)
	}
	return m, nil
}

func (m traceModel) updateServices(km tea.KeyMsg) (traceModel, tea.Cmd) {
	switch km.String() {
	case "q":
		return m, func() tea.Msg { return quitMsg{} }
	case "esc", "backspace", "enter", "s":
		m.mode = traceGantt
	case "up", "k":
		m.pick.move(-1)
	case "down", "j":
		m.pick.move(1)
	case "home", "g":
		m.pick.move(-len(m.pick.names))
	case "end", "G":
		m.pick.move(len(m.pick.names))
	case " ", "tab":
		if name, ok := m.pick.at(); ok {
			m.g.toggleService(name)
		}
	case "a":
		m.g.showAllServices()
	}
	return m, nil
}

func (m traceModel) updateSpan(km tea.KeyMsg) (traceModel, tea.Cmd) {
	doc := m.spanDoc()
	sel := picks(doc)

	switch km.String() {
	case "q":
		return m, func() tea.Msg { return quitMsg{} }
	case "esc", "backspace", "enter":
		m.mode = traceGantt
	case "up", "k":
		m.sel = max(0, m.clampSel(sel)-1)
	case "down", "j":
		m.sel = min(len(sel)-1, m.clampSel(sel)+1)
	case "home", "g":
		m.sel, m.off = 0, 0
	case "end", "G":
		m.sel = max(len(sel)-1, 0)
	case "y":
		if len(sel) == 0 {
			return m, nil
		}
		it := doc[sel[m.clampSel(sel)]]
		return m, copyCmd(it.key, it.value)
	case "f":
		if len(sel) == 0 {
			return m, nil
		}
		it := doc[sel[m.clampSel(sel)]]
		term := it.term()
		if term == nil || !narrowsLogs(it.key) {
			m.note = "nothing to narrow the log by on this row"
			return m, nil
		}
		return m, narrowLogs(term)
	case "o":
		if len(sel) == 0 {
			return m, nil
		}
		// A span attribute is as likely to name a file or a URL as a log field
		// is, and the machinery that opens one does not care which it came
		// from. There is no entry behind it — what an entry would add is the
		// context that lets a bare line number find its file, and a span has
		// no such pair of fields.
		return m, openCmd(nil, doc[sel[m.clampSel(sel)]])
	}
	return m, nil
}

func (m traceModel) clampSel(sel []int) int {
	if len(sel) == 0 {
		return 0
	}
	return min(max(m.sel, 0), len(sel)-1)
}

func (m traceModel) spanDoc() []item {
	n := m.g.at()
	if n == nil {
		return nil
	}
	return spanDocument(n, m.g.bounds.Start, m.g.palette, m.bodyWidth())
}

func (m traceModel) View() string {
	width, height := m.bodyWidth(), m.bodyHeight()

	var body string
	switch {
	case m.err != nil:
		body = styleErr.Render("could not read that trace") + "\n\n" +
			ansi.Truncate(logs.Sanitize(m.err.Error()), width, "…")
	case m.g == nil:
		body = styleDim.Render("fetching trace ") + styleTrace.Render(m.loading) + styleDim.Render(" …")
	case m.mode == traceServices:
		body = m.pick.View(m.g.hidden, m.g.palette, width, height)
	case m.mode == traceSpan:
		body = m.spanView(width, height)
	default:
		body = m.g.View(width, height)
	}
	frame := styleBox.Width(width).Render(body)
	return padScreen(frame + "\n" + ansi.Truncate(m.footer(), m.w, ""))
}

// spanView draws the span document, scrolled so the selected row is in frame.
func (m traceModel) spanView(width, height int) string {
	doc := m.spanDoc()
	sel := picks(doc)
	at := m.clampSel(sel)

	var lines []string
	for i, it := range doc {
		if len(sel) > 0 && i == sel[at] {
			for _, l := range it.lines {
				lines = append(lines, cursorRow(l, width))
			}
			continue
		}
		lines = append(lines, it.lines...)
	}

	off := 0
	if len(sel) > 0 {
		// Keep the selected row in frame, counted in screen lines rather than
		// rows: one attribute may wrap to several.
		start := starts(doc)[sel[at]]
		if start >= height {
			off = start - height + 1
		}
	}
	off = min(off, max(len(lines)-height, 0))
	window := lines[off:min(off+height, len(lines))]
	for len(window) < height {
		window = append(window, "")
	}
	return strings.Join(window, "\n")
}

// footer is the span under the cursor said in full, since the row it is on has
// been truncated to a column and what was cut off is usually the point. It
// gives way to whatever a copy last reported, which is news.
func (m traceModel) footer() string {
	if m.note != "" {
		return styleOK.Render(m.note)
	}
	if m.g == nil {
		return styleHint.Render("esc back")
	}

	switch m.mode {
	case traceServices:
		return styleHint.Render(serviceKeys)
	case traceSpan:
		return styleHint.Render(spanKeys)
	}

	n := m.g.at()
	if n == nil {
		return styleHint.Render(traceKeys)
	}
	parts := []string{styleTrace.Render(n.SpanID)}
	if n.StatusMessage != "" {
		parts = append(parts, styleErr.Render(logs.Sanitize(n.StatusMessage)))
	} else if n.Failed() {
		parts = append(parts, styleErr.Render("failed"))
	}
	if n.Detached {
		parts = append(parts, styleDim.Render("parent "+n.ParentID+" not in this trace"))
	}
	if n.Skew != 0 {
		parts = append(parts, styleDim.Render("shifted "+humanDur(n.Skew)+" to fit its parent"))
	}
	parts = append(parts, styleHint.Render(traceKeys))
	return strings.Join(parts, styleDim.Render(" · "))
}

const (
	traceKeys   = "enter span · space fold · C/E all · s services · f logs · +/- zoom · h/l pan · z focus · r reload · esc back"
	spanKeys    = "↑↓ select · f logs · y copy · o open · esc back"
	serviceKeys = "space toggle · a all · esc back"
)

// openTrace asks for the trace a line was written inside.
//
// The merge tag travels with it because a trace id read off one place means
// something to that place's trace store and nothing to a sibling's: four
// clusters read as one list are still four systems.
func openTrace(e *logs.Entry) tea.Cmd {
	return func() tea.Msg {
		return openTraceMsg{id: e.Record.TraceID, from: e.Source}
	}
}

// narrowLogs takes a term back to the log list, which is where narrowing lands
// for the same reason it does from an entry: reading one thing is how you find
// what is worth narrowing by, and staying on it would hide what the narrowing
// did.
func narrowLogs(term query.Expr) tea.Cmd {
	return func() tea.Msg { return filterMsg{term: term} }
}
