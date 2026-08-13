package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
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

// traceModel is one trace on screen.
//
// Everything it knows how to draw is in [gantt]; this is the keys, the frame
// and the one line under it that says what the cursor is on. The split is the
// same one the log list makes with its store: what a thing looks like is worked
// out where the data is, and the model is what a reader does to it.
type traceModel struct {
	w, h int
	g    *gantt
	// note is what the last copy did, shown until the next key. It is the only
	// evidence a copy leaves: the clipboard is the terminal's and cannot be
	// read back.
	note string
}

func newTrace(t *trace.Tree) traceModel {
	return traceModel{g: newGantt(t)}
}

func (m *traceModel) resize(w, h int) { m.w, m.h = w, h }

// bodyHeight is what is left for the gantt inside the frame, after the border
// and the key line under it.
func (m traceModel) bodyHeight() int {
	return max(m.h-4, 1)
}

func (m traceModel) bodyWidth() int {
	return max(m.w-2*screenPad-2, 20)
}

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

	case " ", "enter", "tab":
		m.g.toggle()

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

	case "y":
		if n := m.g.at(); n != nil {
			return m, copyCmd("span_id", n.SpanID)
		}
	case "Y":
		return m, copyCmd("trace_id", m.g.tree.ID)
	}
	return m, nil
}

func (m traceModel) View() string {
	body := m.g.View(m.bodyWidth(), m.bodyHeight())
	frame := styleBox.Width(m.bodyWidth()).Render(body)
	return padScreen(frame + "\n" + ansi.Truncate(m.footer(), m.w, ""))
}

// footer is the span under the cursor said in full, since the row it is on has
// been truncated to a column and the thing worth reading is usually what was
// cut off. It gives way to whatever a copy last reported, which is news.
func (m traceModel) footer() string {
	if m.note != "" {
		return styleHint.Render(m.note)
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
	// Only worth saying where it was moved: a skew of zero is every span in a
	// trace whose clocks agreed, which is most of them.
	if n.Skew != 0 {
		parts = append(parts, styleDim.Render("shifted "+humanDur(n.Skew)+" to fit its parent"))
	}
	parts = append(parts, styleHint.Render(traceKeys))
	return strings.Join(parts, styleDim.Render(" · "))
}

const traceKeys = "space fold · +/- zoom · h/l pan · z focus · 0 fit · y copy id · esc back"
