package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
)

type entryModel struct {
	w, h  int
	entry *logs.Entry
	off   int
}

func newEntry(e *logs.Entry) entryModel { return entryModel{entry: e} }

func (m *entryModel) resize(w, h int) { m.w, m.h = w, h }

// bodyHeight is the number of lines that fit inside the frame.
func (m entryModel) bodyHeight() int {
	if h := m.h - 3; h > 0 {
		return h
	}
	return 1
}

func (m entryModel) Update(msg tea.Msg) (entryModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "q":
		return m, func() tea.Msg { return quitMsg{} }
	case "esc", "enter", "backspace":
		return m, func() tea.Msg { return backMsg{} }
	case "up", "k":
		m.off = max(0, m.off-1)
	case "down", "j":
		m.off++
	case "pgup":
		m.off = max(0, m.off-m.bodyHeight())
	case "pgdown":
		m.off += m.bodyHeight()
	case "home", "g":
		m.off = 0
	}
	return m, nil
}

func (m entryModel) View() string {
	if m.entry == nil {
		return ""
	}
	inner := max(m.w-4, 20)
	lines := m.lines(inner)

	height := m.bodyHeight()
	off := min(m.off, max(len(lines)-height, 0))
	window := lines[off:min(off+height, len(lines))]
	for len(window) < height {
		window = append(window, "")
	}

	help := strings.Join([]string{
		key("↑↓", "scroll"),
		key("esc", "back"),
		key("q", "quit"),
	}, styleHint.Render(" · "))

	return styleBoxFocus.Width(m.w-2).Render(strings.Join(window, "\n")) + "\n" +
		ansi.Truncate(help, max(m.w-1, 1), "")
}

// lines renders the entry as a scrollable document.
func (m entryModel) lines(width int) []string {
	e := m.entry
	var out []string

	add := func(label, value string) {
		if value == "" {
			return
		}
		out = append(out, wrapField(label, value, width))
	}

	stream := "stdout"
	if e.Stderr {
		stream = "stderr"
	}
	out = append(out, styleTitle.Render(fmt.Sprintf("entry #%d", e.Seq))+
		styleDim.Render("  "+stream))
	out = append(out, "")

	if e.Record.HasTime() {
		t := e.Record.Time.Local()
		add("time", t.Format(time.RFC3339Nano)+styleDim.Render("  "+humanSince(t)))
	} else {
		add("received", e.At.Local().Format(time.RFC3339Nano))
	}
	if e.Record.Structured {
		add("level", e.Record.Level.CapitalString())
	}
	add("trace", e.Record.TraceID)
	add("span", e.Record.SpanID)
	add("body", e.Record.Body)

	out = append(out, "")
	out = append(out, styleTitle.Render("rendered"))
	out = append(out, "  "+ansi.Truncate(e.Text, max(width-2, 1), "…"))

	if len(e.Record.Fields) > 0 {
		out = append(out, "", styleTitle.Render("fields"))
		for _, f := range e.Record.Fields {
			out = append(out, wrapField(f.Key, f.String(), width))
		}
	}

	out = append(out, "", styleTitle.Render("raw"))
	for l := range strings.SplitSeq(prettyJSON(e.Raw), "\n") {
		out = append(out, "  "+styleDim.Render(l))
	}
	return out
}

// wrapField renders a "label  value" row, wrapping the value under the label.
func wrapField(label, value string, width int) string {
	const indent = 12
	wrapped := ansi.Wrap(value, max(width-indent, 10), "")
	parts := strings.Split(wrapped, "\n")
	b := &strings.Builder{}
	b.WriteString(styleLabel.Render(label))
	b.WriteString("  ")
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
