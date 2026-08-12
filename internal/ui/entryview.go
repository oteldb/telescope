package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

type entryModel struct {
	w, h int
	// cfg is the stream the entry was read from, which is what says where its
	// lines come from.
	cfg   source.Config
	entry *logs.Entry
	off   int
	// sel is which of the document's pickable items the cursor is on. It counts
	// items rather than lines so it survives a resize, which rewraps every value
	// but reorders nothing.
	sel int
	// note is what the last copy did, shown until the next key. It is the only
	// evidence a copy leaves: the clipboard is the terminal's, not ours, and
	// cannot be read back to confirm.
	note string
}

func newEntry(cfg source.Config, e *logs.Entry) entryModel {
	return entryModel{cfg: cfg, entry: e}
}

func (m *entryModel) resize(w, h int) { m.w, m.h = w, h }

// bodyHeight is the number of lines that fit inside the frame.
func (m entryModel) bodyHeight() int {
	if h := m.h - 3; h > 0 {
		return h
	}
	return 1
}

// docWidth is the width a value is wrapped to, inside the frame.
func (m entryModel) docWidth() int {
	return max(m.w-2*screenPad-2, 18)
}

func (m entryModel) Update(msg tea.Msg) (entryModel, tea.Cmd) {
	if c, ok := msg.(copiedMsg); ok {
		m.note = c.note()
		return m, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	doc := m.document(m.docWidth())
	sel := picks(doc)
	m.note = ""

	switch km.String() {
	case "q":
		return m, func() tea.Msg { return quitMsg{} }
	case "esc", "enter", "backspace":
		return m, func() tea.Msg { return backMsg{} }
	case "up", "k":
		m.sel = max(0, m.clamp(sel)-1)
	case "down", "j":
		m.sel = min(len(sel)-1, m.clamp(sel)+1)
	case "pgup":
		m.sel = m.page(doc, sel, -m.bodyHeight())
	case "pgdown":
		m.sel = m.page(doc, sel, m.bodyHeight())
	case "home", "g":
		// To the top of the document, not to the first thing on it that can be
		// picked: the heading above it is part of what "the top" means.
		m.sel, m.off = 0, 0
	case "end", "G":
		m.sel = max(len(sel)-1, 0)
	case "y":
		if len(sel) == 0 {
			return m, nil
		}
		it := doc[sel[m.clamp(sel)]]
		return m, copyCmd(it.key, it.value)
	case "Y":
		// The whole entry as it arrived, from wherever the cursor happens to be.
		return m, copyCmd("entry", string(m.entry.Raw))
	default:
		return m, nil
	}
	m.off = m.follow(doc, sel)
	return m, nil
}

// follow scrolls the frame as little as it can to bring the cursor into it.
// The frame remembers where it was because the cursor does not travel far: a
// key that moved it one row should not restack the document around it.
func (m entryModel) follow(doc []item, sel []int) int {
	if len(sel) == 0 || len(doc) == 0 {
		return 0
	}
	line := starts(doc)
	i := sel[m.clamp(sel)]
	first, last := line[i], line[i]+len(doc[i].lines)-1

	height := m.bodyHeight()
	total := line[len(doc)-1] + len(doc[len(doc)-1].lines)
	off := min(m.off, max(total-height, 0))
	switch {
	case first < off:
		off = first
	case last >= off+height:
		// A value taller than the frame shows its head, not its tail.
		off = min(last-height+1, first)
	}
	return max(off, 0)
}

// clamp keeps the cursor on an item that exists. The document is rebuilt on
// every key, and an entry whose fields arrived late is a shorter document than
// the one the cursor was last placed in.
func (m entryModel) clamp(sel []int) int {
	if len(sel) == 0 {
		return 0
	}
	return min(max(m.sel, 0), len(sel)-1)
}

// page moves the cursor a screenful, measured in the lines the items draw as
// rather than in items: a stacktrace is one item and most of a screen, and
// stepping over it by one would skip a page at a time.
func (m entryModel) page(doc []item, sel []int, delta int) int {
	if len(sel) == 0 {
		return 0
	}
	at := m.clamp(sel)
	line := starts(doc)
	target := line[sel[at]] + delta

	best := at
	for i, idx := range sel {
		if abs(line[idx]-target) < abs(line[sel[best]]-target) {
			best = i
		}
	}
	// A page that lands nowhere new still has to move, or the key does nothing
	// at the top and bottom of a document of very tall items.
	if best == at {
		if delta < 0 {
			return max(at-1, 0)
		}
		return min(at+1, len(sel)-1)
	}
	return best
}

func (m entryModel) View() string {
	if m.entry == nil {
		return ""
	}
	width := max(m.w-2*screenPad, 20)
	inner := m.docWidth()

	doc := m.document(inner)
	sel := picks(doc)
	at := m.clamp(sel)

	var lines []string
	for i, it := range doc {
		if len(sel) > 0 && i == sel[at] {
			for _, l := range it.lines {
				lines = append(lines, cursorRow(l, inner))
			}
			continue
		}
		lines = append(lines, it.lines...)
	}

	// The frame is worked out from the cursor the same way here as it is on a
	// key, so that a resize that rewraps a value still shows what is selected.
	height := m.bodyHeight()
	off := min(m.follow(doc, sel), max(len(lines)-height, 0))
	window := lines[off:min(off+height, len(lines))]
	for len(window) < height {
		window = append(window, "")
	}

	return padScreen(styleBoxFocus.Width(width).Render(strings.Join(window, "\n")) + "\n" +
		ansi.Truncate(m.help(), width, ""))
}

func (m entryModel) help() string {
	if m.note != "" {
		return styleOK.Render(m.note)
	}
	return strings.Join([]string{
		key("↑↓", "select"),
		key("y", "copy"),
		key("Y", "entry"),
		key("esc", "back"),
		key("q", "quit"),
	}, styleHint.Render(" · "))
}

// copiedMsg reports what a copy did. It is a message rather than a return value
// because writing to the terminal is not the model's work to do inline.
type copiedMsg struct {
	what string
	err  error
}

func (c copiedMsg) note() string {
	if c.err != nil {
		return fmt.Sprintf("could not copy: %v", c.err)
	}
	return "copied " + c.what
}

func copyCmd(what, value string) tea.Cmd {
	return func() tea.Msg {
		return copiedMsg{what: what, err: copyValue(value)}
	}
}

// Bounds on the label column. It follows the keys an entry actually has, but a
// Kubernetes attribute is long enough to leave no room for its own value, so it
// stops well short of that.
const (
	minLabelColumn = 10
	maxLabelColumn = 28
)

// labelColumn is how wide the keys of an entry are rendered, from the keys
// themselves: aligning on the widest one is what makes a list of attributes
// readable, and a single very long key must not shrink every value beside it.
func labelColumn(labels []string, width int) int {
	widest := 0
	for _, l := range labels {
		widest = max(widest, lipgloss.Width(l))
	}
	// Room for the two spaces between a key and its value.
	return min(max(widest+2, minLabelColumn), maxLabelColumn, max(width/3, minLabelColumn))
}

// wrapField renders a "label  value" row, wrapping the value under the label.
//
// A label too long for the column takes a line of its own rather than being
// folded into a narrow ribbon beside its value: an OTEL resource attribute is
// forty characters of prose, and reading it a syllable at a time is worse than
// reading it whole. One that fits keeps its value beside it even when the gap
// comes to a single space — what makes the rows readable is that the values
// line up, and a label a column short of the widest is not a long label.
func wrapField(label, value string, indent, width int) string {
	wrapped := ansi.Wrap(value, max(width-indent, minLabelColumn), "")
	parts := strings.Split(wrapped, "\n")

	b := &strings.Builder{}
	pad := indent - lipgloss.Width(label)
	b.WriteString(styleLabel.Render(label))
	if pad < 1 {
		b.WriteString("\n")
		b.WriteString(strings.Repeat(" ", indent))
	} else {
		b.WriteString(strings.Repeat(" ", pad))
	}
	b.WriteString(parts[0])
	for _, p := range parts[1:] {
		b.WriteString("\n")
		b.WriteString(strings.Repeat(" ", indent))
		b.WriteString(p)
	}
	return b.String()
}

func prettyJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(raw), "", "  "); err != nil {
		return string(bytes.TrimSpace(raw))
	}
	return buf.String()
}

func humanSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 0:
		return "in the future"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
