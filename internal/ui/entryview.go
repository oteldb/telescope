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
	case "end", "G":
		// Clamped against the rendered length in View.
		m.off = len(m.lines(max(m.w-2*screenPad-2, 18)))
	}
	return m, nil
}

func (m entryModel) View() string {
	if m.entry == nil {
		return ""
	}
	width := max(m.w-2*screenPad, 20)
	lines := m.lines(max(width-2, 18))

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

	return padScreen(styleBoxFocus.Width(width).Render(strings.Join(window, "\n")) + "\n" +
		ansi.Truncate(help, width, ""))
}

// lines renders the entry as a scrollable document.
func (m entryModel) lines(width int) []string {
	e := m.entry
	var out []string

	// The header labels are fixed; the fields bring their own, and the widest
	// of all of them is what everything lines up on.
	labels := []string{"received", "source", "level", "trace_id", "span_id", "body"}
	for _, f := range e.Record.Fields {
		labels = append(labels, f.Key)
	}
	origin := m.cfg.SourceLabels(e.Source)
	for _, l := range origin {
		labels = append(labels, l.Key)
	}
	for _, l := range e.Labels {
		labels = append(labels, l.Key)
	}
	indent := labelColumn(labels, width)

	add := func(label, value string) {
		if value == "" {
			return
		}
		out = append(out, wrapField(label, value, indent, width))
	}
	// A key says what its value is, and a value that is somebody else's bytes
	// says nothing about how it should be drawn.
	field := func(key, value string) {
		add(key, renderValue(key, value))
	}

	stream := "stdout"
	if e.Stderr {
		stream = "stderr"
	}
	out = append(out, styleTitle.Render(fmt.Sprintf("entry #%d", e.Seq))+
		styleDim.Render("  "+stream))
	out = append(out, "")

	// A time the line was written with, whether it said so itself or the source
	// said it for the line; failing both, when it turned up here.
	if e.HasTime {
		t := e.At.Local()
		add("time", t.Format(time.RFC3339Nano)+styleDim.Render("  "+humanSince(t)))
	} else {
		add("received", e.At.Local().Format(time.RFC3339Nano))
	}
	// Which source a line came from, for a merge of several.
	add("source", e.Source)
	if e.Record.HasLevel {
		add("level", renderLevelWord(e.Record.Level))
	}
	field("trace_id", e.Record.TraceID)
	field("span_id", e.Record.SpanID)
	add("body", logs.Escape(e.Record.Body))

	// Where the whole stream comes from, then what this line brought with it.
	// A log database says more about a line than the line does, and none of it
	// fits in the list.
	section := func(title string, labels []source.Label) {
		if len(labels) == 0 {
			return
		}
		out = append(out, "", styleTitle.Render(title))
		for _, l := range labels {
			out = append(out, wrapField(logs.Escape(l.Key), renderValue(l.Key, l.Value), indent, width))
		}
	}
	section("source", origin)
	section("labels", e.Labels)

	out = append(out, "")
	out = append(out, styleTitle.Render("rendered"))
	// The full rendering, stacktrace and all: this is where it belongs.
	for l := range strings.SplitSeq(e.Text, "\n") {
		out = append(out, "  "+ansi.Truncate(l, max(width-2, 1), "…"))
	}

	if len(e.Record.Fields) > 0 {
		out = append(out, "", styleTitle.Render("fields"))
		for _, f := range e.Record.Fields {
			out = append(out, wrapField(logs.Escape(f.Key), renderValue(f.Key, f.String()), indent, width))
		}
	}

	out = append(out, "", styleTitle.Render("raw"))
	for l := range strings.SplitSeq(prettyJSON(e.Raw), "\n") {
		// Escaping brings its own color, and dimming it over would put the
		// escapes back at the mercy of what they escaped.
		if escaped := logs.Escape(l); escaped != l {
			out = append(out, "  "+escaped)
			continue
		}
		out = append(out, "  "+styleDim.Render(l))
	}
	return out
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
// reading it whole.
func wrapField(label, value string, indent, width int) string {
	wrapped := ansi.Wrap(value, max(width-indent, minLabelColumn), "")
	parts := strings.Split(wrapped, "\n")

	b := &strings.Builder{}
	pad := indent - lipgloss.Width(label)
	b.WriteString(styleLabel.Render(label))
	if pad < 2 {
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
