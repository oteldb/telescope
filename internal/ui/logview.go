package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// hScrollStep is how far ←/→ shift the viewport.
const hScrollStep = 8

type logModel struct {
	w, h int

	cfg   source.Config
	store *logs.Store
	view  *logs.View

	cursor int
	hoff   int
	follow bool

	search    textinput.Model
	searching bool

	status string
	err    error
}

func newLogs(cfg source.Config, store *logs.Store, query string) logModel {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colorAccent)
	ti.Placeholder = "grep term or regexp"
	ti.SetValue(query)

	return logModel{
		cfg:    cfg,
		store:  store,
		view:   logs.NewView(logs.Filter{Query: query}),
		follow: true,
		search: ti,
		status: "connecting",
	}
}

func (m *logModel) resize(w, h int) { m.w, m.h = w, h }

// bodyHeight is the number of log lines that fit in the framed view.
func (m logModel) bodyHeight() int {
	// 4 lines of top bar (2 borders, 2 rows), 2 lines of log frame border,
	// 1 footer line.
	if h := m.h - 7; h > 0 {
		return h
	}
	return 1
}

func (m logModel) Update(msg tea.Msg) (logModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.searching {
		return m.updateSearch(km)
	}
	entries := m.view.Entries(m.store)

	switch km.String() {
	case "q":
		return m, func() tea.Msg { return quitMsg{} }
	case "esc":
		return m, func() tea.Msg { return backMsg{} }
	case "/":
		m.searching = true
		m.search.Focus()
		m.search.CursorEnd()
		return m, textinput.Blink
	case "enter":
		if i := m.cursor; i >= 0 && i < len(entries) {
			e := entries[i]
			return m, func() tea.Msg { return openEntryMsg{entry: e} }
		}
	case "f":
		m.follow = !m.follow
	case "l":
		f := m.view.Filter()
		f.MinLevel = f.MinLevel.Next()
		m.view.SetFilter(f)
		m.cursor = 0
	case "up", "k":
		m.move(-1, len(entries))
	case "down", "j":
		m.move(1, len(entries))
	case "pgup":
		m.move(-m.bodyHeight(), len(entries))
	case "pgdown":
		m.move(m.bodyHeight(), len(entries))
	case "home", "g":
		m.follow = false
		m.cursor = 0
	case "end", "G":
		m.follow = true
		m.cursor = len(entries) - 1
	case "left":
		m.hoff = max(0, m.hoff-hScrollStep)
	case "right":
		m.hoff += hScrollStep
	case "0":
		m.hoff = 0
	}
	return m, nil
}

func (m logModel) updateSearch(km tea.KeyMsg) (logModel, tea.Cmd) {
	switch km.String() {
	case "enter":
		f := m.view.Filter()
		f.Query = strings.TrimSpace(m.search.Value())
		m.view.SetFilter(f)
		m.searching = false
		m.search.Blur()
		m.cursor = 0
		return m, nil
	case "esc":
		m.searching = false
		m.search.Blur()
		m.search.SetValue(m.view.Filter().Query)
		return m, nil
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(km)
	return m, cmd
}

// move shifts the cursor, disabling follow when scrolling away from the tail.
func (m *logModel) move(d, n int) {
	if n == 0 {
		return
	}
	m.cursor = min(max(m.cursor+d, 0), n-1)
	m.follow = m.cursor == n-1
}

func (m logModel) View() string {
	entries := m.view.Entries(m.store)
	if m.follow && len(entries) > 0 {
		m.cursor = len(entries) - 1
	}
	m.cursor = min(max(m.cursor, 0), max(len(entries)-1, 0))

	height := m.bodyHeight()
	top := 0
	if m.cursor >= height {
		top = m.cursor - height + 1
	}

	inner := max(m.w-4, 10)
	body := make([]string, 0, height)
	for i := top; i < len(entries) && i < top+height; i++ {
		body = append(body, renderLine(entries[i], i == m.cursor, m.hoff, inner))
	}
	for len(body) < height {
		body = append(body, "")
	}

	return strings.Join([]string{
		m.topBar(entries),
		styleBox.Width(m.w - 2).Render(strings.Join(body, "\n")),
		m.footer(entries),
	}, "\n")
}

// renderLine renders one entry, ANSI colors intact, honoring the horizontal
// offset and available width.
func renderLine(e *logs.Entry, selected bool, hoff, width int) string {
	marker := "  "
	if selected {
		marker = styleSelected.Render("▎ ")
	}
	text := e.Text
	if e.Stderr {
		text = styleErr.Render("!") + " " + text
	}
	if hoff > 0 {
		text = ansi.TruncateLeft(text, hoff, "")
	}
	return marker + ansi.Truncate(text, max(width-2, 1), styleDim.Render("→"))
}

func (m logModel) topBar(entries []*logs.Entry) string {
	title := styleTitle.Render(m.cfg.Title())

	var (
		stats  []string
		filter = m.view.Filter()
	)
	stats = append(stats, fmt.Sprintf("%d shown", len(entries)))
	stats = append(stats, fmt.Sprintf("%d lines", m.store.Len()))
	if d := m.store.Dropped(); d > 0 {
		stats = append(stats, fmt.Sprintf("%d dropped", d))
	}
	if r := timeRange(entries); r != "" {
		stats = append(stats, r)
	}
	stats = append(stats, filter.Describe())
	stats = append(stats, "follow "+onOff(m.follow))
	stats = append(stats, m.statusText())

	line := styleDim.Render(strings.Join(stats, " · "))
	return styleBox.Width(m.w - 2).Render(
		ansi.Truncate(title, max(m.w-4, 1), "…") + "\n" +
			ansi.Truncate(line, max(m.w-4, 1), "…"),
	)
}

func (m logModel) statusText() string {
	switch {
	case m.err != nil:
		return styleErr.Render(m.status + ": " + m.err.Error())
	case m.status == "streaming":
		return styleOK.Render(m.status)
	default:
		return m.status
	}
}

func (m logModel) footer(entries []*logs.Entry) string {
	if m.searching {
		return m.search.View()
	}
	help := strings.Join([]string{
		key("↑↓", "move"),
		key("enter", "entry"),
		key("/", "filter"),
		key("f", "follow"),
		key("l", "level"),
		key("←→", "scroll"),
		key("esc", "sources"),
		key("q", "quit"),
	}, styleHint.Render(" · "))
	if len(entries) == 0 && m.store.Len() > 0 {
		help = styleHint.Render("no lines match · ") + help
	}
	return ansi.Truncate(help, max(m.w-1, 1), "")
}

// timeRange summarizes the time span covered by the visible entries.
func timeRange(entries []*logs.Entry) string {
	if len(entries) == 0 {
		return ""
	}
	from, to := entries[0].At, entries[len(entries)-1].At
	if from.IsZero() || to.IsZero() {
		return ""
	}
	const layout = "15:04:05"
	if from.Truncate(24 * time.Hour).Equal(to.Truncate(24 * time.Hour)) {
		return from.Local().Format(layout) + " → " + to.Local().Format(layout)
	}
	return from.Local().Format("01-02 "+layout) + " → " + to.Local().Format("01-02 "+layout)
}

// textinputBlink is the initial cursor blink for the start screen.
var textinputBlink = textinput.Blink
