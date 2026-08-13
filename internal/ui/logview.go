package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
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
	// top is the first entry drawn, kept as real state so the cursor can move
	// within the window without the window moving under it.
	top    int
	hoff   int
	follow bool

	// tags is how each source of a merge is marked in the gutter, keyed by the
	// label its lines carry. Empty when there is only one source.
	tags map[string]string
	// cols are the gutter columns this stream has turned out to need.
	cols columns
	// times is how the time column is written, and origin is what an age in it
	// is measured from: when the view was opened, and so when what it holds was
	// asked for.
	times  timeMode
	origin time.Time

	search    textinput.Model
	searching bool
	// queryErr is why what is being typed is not a query yet. It belongs to the
	// prompt rather than to the view, which is still showing the last one that
	// was.
	queryErr error
	// sel is the highlighted suggestion, counted from the top of what is
	// currently offered.
	sel int
	// fields is what the source said it is labeled with, keyed by the field
	// whose values were asked for and by "" for the names themselves. asked is
	// what has already been requested, answered or not.
	fields map[string][]string
	asked  map[string]bool

	// paging is a page of older lines in flight, atStart is a database that
	// answered there are none, atCap is the store with no room for another and
	// pageErr is why the last one did not arrive. See [logModel.wantPage].
	paging  bool
	atStart bool
	atCap   bool
	pageErr error

	status string
	err    error
}

func newLogs(cfg source.Config, store *logs.Store, query string) logModel {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colorAccent)
	ti.Placeholder = `word, "phrase", /regexp/, pod=api, level>=warn`
	ti.SetValue(query)

	return logModel{
		cfg:    cfg,
		tags:   mergeTags(cfg),
		store:  store,
		view:   logs.NewView(logs.Filter{Query: query}),
		follow: true,
		origin: time.Now(),
		search: ti,
		status: "connecting",
	}
}

// columns are the fields drawn to the left of a line, as opposed to inside it.
//
// A structured line is rendered with its own time and level by the formatter,
// so the columns are only for lines that carry neither: what a log database
// reports beside a bare message. They are turned on by the first line that
// needs them and stay on, so the text does not shift left and right as lines of
// both kinds arrive.
type columns struct {
	time  bool
	level bool
	trace bool
}

// append stores a line and notes what rendering it will need.
func (m *logModel) append(l source.Line) { m.observe(m.store.Append(l)) }

// observe turns on the columns e needs.
func (m *logModel) observe(e *logs.Entry) {
	if e == nil {
		return
	}
	m.cols.time = m.cols.time || e.HasTime
	m.cols.trace = m.cols.trace || e.Record.TraceID != ""
	if e.Record.Structured {
		return
	}
	m.cols.level = m.cols.level || e.Record.HasLevel
}

// gutter renders the columns of one entry, blank where it has nothing to say.
//
// The time is drawn here for every line, structured or not, rather than left to
// the renderer that made the text: how a time is written is the view's to
// change, and a rendering worked out when the line arrived cannot be.
func (m logModel) gutter(e *logs.Entry) string {
	var b strings.Builder
	if m.cols.time {
		switch {
		case e.HasTime:
			b.WriteString(styleDim.Render(m.times.stamp(e.At, m.origin)))
		default:
			b.WriteString(strings.Repeat(" ", m.times.width()))
		}
		b.WriteByte(' ')
	}
	if m.cols.level {
		switch {
		case e.Record.HasLevel && !e.Record.Structured:
			b.WriteString(renderLevel(e.Record.Level))
		default:
			b.WriteString(strings.Repeat(" ", levelWidth))
		}
		b.WriteByte(' ')
	}
	if m.cols.trace {
		b.WriteString(traceMark(e))
		b.WriteByte(' ')
	}
	return b.String()
}

// mergeTags marks each source of a merge, padded to a single column so the
// lines beside them line up whatever they came from.
func mergeTags(cfg source.Config) map[string]string {
	if cfg.Collector != source.CollectorMerge {
		return nil
	}
	labels := cfg.Labels()
	width := 0
	for _, l := range labels {
		width = max(width, lipgloss.Width(l))
	}
	out := make(map[string]string, len(labels))
	for i, l := range labels {
		out[l] = tagStyle(i).Render(l + strings.Repeat(" ", width-lipgloss.Width(l)))
	}
	return out
}

func (m *logModel) resize(w, h int) {
	m.w, m.h = w, h
	// bubbles draws a placeholder only as far as its Width, and a Width of zero
	// draws one rune of it — the prompt would advertise the language it takes
	// and then show a lone "w". It is also what scrolls a long query, so it is
	// the room left beside the chips and not the whole screen.
	//
	// The one column beyond the prompt and the gap is bubbles' own: it pads a
	// placeholder to Width and then adds a column for the cursor sitting past
	// the end of it.
	m.search.Width = max(m.width()-lipgloss.Width(m.chips())-lipgloss.Width(m.search.Prompt)-3, 10)
}

// bodyHeight is the number of log lines that fit in the framed view.
//
// The suggestions come out of it rather than out of the terminal, so the frame
// stays where it is while a filter is typed: a list that jumped every time the
// prompt offered something would be unreadable.
func (m logModel) bodyHeight() int {
	// 4 lines of top bar (2 borders, 2 rows), 2 lines of log frame border,
	// 1 filter bar, 1 help line.
	if h := m.h - 8 - len(m.promptRows()); h > 0 {
		return h
	}
	return 1
}

// suggestions are the rows the prompt is currently offering, at most
// [suggestRows] of them.
func (m logModel) suggestions() []complete.Candidate {
	if !m.searching || m.queryErr != nil {
		return nil
	}
	_, items := m.suggest()
	if len(items) > suggestRows {
		items = items[:suggestRows]
	}
	return items
}

// width is the usable width once the screen padding is taken out.
func (m logModel) width() int { return max(m.w-2*screenPad, 20) }

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
		m.sel = 0
		m.search.Focus()
		m.search.CursorEnd()
		// The names are asked for as the prompt opens rather than when a
		// suggestion is first wanted: a listing is a round trip, and the moment
		// it is wanted is the moment it is too late to start. Sequenced through
		// a variable because wantFields records what it asked on m.
		ask := m.wantFields("")
		return m, tea.Batch(textinput.Blink, ask)
	case "?":
		return m, openHelp
	case "enter":
		if i := m.cursor; i >= 0 && i < len(entries) {
			e := entries[i]
			return m, func() tea.Msg { return openEntryMsg{entry: e} }
		}
	case "f":
		m.follow = !m.follow
		m.syncFollow()
	case "t":
		m.times = m.times.next()
	case "l":
		f := m.view.Filter()
		f.MinLevel = f.MinLevel.Next()
		m.view.SetFilter(f)
		m.cursor, m.top = 0, 0
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
		m.cursor, m.top = 0, 0
	case "end", "G":
		m.follow = true
		m.cursor = max(len(entries)-1, 0)
	case "H":
		// Top and bottom of what is on screen, as in less and vim.
		m.follow = false
		m.cursor = m.top
	case "L":
		m.cursor = m.top + m.bodyHeight() - 1
		m.follow = m.cursor >= len(entries)-1
	case "left":
		m.hoff = max(0, m.hoff-hScrollStep)
	case "right":
		m.hoff += hScrollStep
	case "0":
		m.hoff = 0
	}
	m.clamp()
	return m, m.wantPage()
}

func (m logModel) updateSearch(km tea.KeyMsg) (logModel, tea.Cmd) {
	_, items := m.suggest()

	switch km.String() {
	// A "?" typed into the prompt is a regexp quantifier, so the reference is
	// reached by a key that could not have been part of a query.
	case "f1":
		return m, openHelp
	case "tab":
		return m.accept(items)
	case "down", "ctrl+n":
		m.sel = min(m.sel+1, max(len(items)-1, 0))
		return m, nil
	case "up", "ctrl+p":
		m.sel = max(m.sel-1, 0)
		return m, nil
	case "enter":
		f := m.view.Filter()
		f.Query = strings.TrimSpace(m.search.Value())
		// A query that does not parse leaves the view as it was: the prompt
		// stays open on what was typed, which is where it can be fixed.
		if err := f.Compile().Err(); err != nil {
			m.queryErr = err
			return m, nil
		}
		m.queryErr = nil
		m.searching = false
		m.search.Blur()
		return m.apply(f.Query)
	case "esc":
		m.searching = false
		m.queryErr = nil
		m.search.Blur()
		m.search.SetValue(m.view.Filter().Query)
		return m, nil
	}

	var cmd tea.Cmd
	m.search, cmd = m.search.Update(km)
	// A keystroke moves what is being completed, so the highlight starts over
	// rather than pointing into a list that has changed under it.
	m.sel = 0
	ask := m.askAtCursor()
	return m, tea.Batch(cmd, ask)
}

// accept writes the highlighted suggestion into the prompt.
func (m logModel) accept(items []complete.Candidate) (logModel, tea.Cmd) {
	at, _ := m.suggest()
	if len(items) == 0 || !at.OK {
		return m, nil
	}
	value, pos := at.apply(m.search.Value(), items[min(m.sel, len(items)-1)].Value, at.Key == "")
	m.search.SetValue(value)
	m.search.SetCursor(pos)
	m.sel = 0
	// Accepting a name leaves the cursor after the comparison, where the values
	// under it are what is wanted next.
	ask := m.askAtCursor()
	return m, ask
}

// askAtCursor asks the source about whatever the cursor has moved into, which is
// the values of a field the first time one is named.
func (m *logModel) askAtCursor() tea.Cmd {
	at := completeAt(m.search.Value(), m.search.Position())
	if !at.OK || at.Key == "" {
		return nil
	}
	return m.wantFields(at.Key)
}

// apply puts q in force, as the prompt and as the filter, and asks the sources
// again if it is something they can answer.
func (m logModel) apply(q string) (logModel, tea.Cmd) {
	f := m.view.Filter()
	f.Query = q
	f = f.Compile()
	m.search.SetValue(q)

	// A source that can answer part of the query is asked it again, and the view
	// is rebuilt from what comes back: the same lines, fetched rather than
	// filtered out of everything else.
	if cfg, ok := m.requery(f); ok {
		return m, func() tea.Msg { return requeryMsg{cfg: cfg, query: f.Query} }
	}
	m.view.SetFilter(f)
	m.cursor, m.top = 0, 0
	return m, nil
}

// narrow ands term onto the filter, which is what a jump out of an entry does.
// The query already in force is parenthesized where it has to be, since a term
// picked up by one branch of an or would widen the filter rather than narrow
// it; And.String is what knows when that is.
func (m logModel) narrow(term query.Expr) (logModel, tea.Cmd) {
	next := term
	// The query in force is one that parsed, so a failure here is not a state
	// the view can be in; narrowing by the term alone is still an answer.
	if cur, err := query.Parse(m.view.Filter().Query); err == nil && cur != nil {
		if and, ok := cur.(query.And); ok {
			// Flattened rather than nested, so a filter built by jumping twice
			// reads as the three terms it is and not as brackets around them.
			next = append(slices.Clone(and), term)
		} else {
			next = query.And{cur, term}
		}
	}
	return m.apply(next.String())
}

// requery reports whether f asks the sources themselves something other than
// what they were opened with, and with what config if so.
func (m logModel) requery(f logs.Filter) (source.Config, bool) {
	next := m.cfg.WithFilter(f.Expr())
	if slices.Equal(next.Pushed(), m.cfg.Pushed()) {
		return source.Config{}, false
	}
	return next, true
}

// syncFollow pins the cursor to the newest entry while following. It runs when
// entries arrive rather than at render time, so keys that work from the cursor
// see where the view actually is.
func (m *logModel) syncFollow() {
	if !m.follow {
		return
	}
	if n := len(m.view.Entries(m.store)); n > 0 {
		m.cursor = n - 1
	}
	m.clamp()
}

// move shifts the cursor, disabling follow when scrolling away from the tail.
func (m *logModel) move(d, n int) {
	if n == 0 {
		return
	}
	m.cursor = min(max(m.cursor+d, 0), n-1)
	m.follow = m.cursor == n-1
}

// clamp keeps the cursor inside the list and the window around the cursor,
// scrolling by the least amount needed.
//
// A window is counted in rows and not in entries, since a gap between two of
// them takes a row of its own: a screenful of a log that went quiet twice is
// two lines shorter than a screenful of one that did not.
func (m *logModel) clamp() {
	entries := m.view.Entries(m.store)
	n := len(entries)
	h := m.bodyHeight()

	m.cursor = min(max(m.cursor, 0), max(n-1, 0))
	m.top = min(max(m.top, 0), m.cursor)
	for m.top < m.cursor && m.rows(entries, m.top, m.cursor) > h {
		m.top++
	}
}

// rows is how much of the screen the entries from..to take, the gaps between
// them included.
func (m logModel) rows(entries []*logs.Entry, from, to int) int {
	n := to - from + 1
	for i := from + 1; i <= to && i < len(entries); i++ {
		if _, ok := gap(entries[i-1], entries[i]); ok {
			n++
		}
	}
	return n
}

func (m logModel) View() string {
	entries := m.view.Entries(m.store)
	m.clamp()

	height := m.bodyHeight()

	inner := max(m.width()-2, 10)
	body := make([]string, 0, height)
	for i := m.top; i < len(entries) && len(body) < height; i++ {
		e := entries[i]
		// The silence before a line is drawn above it, and only where the line
		// above is on screen: a gap at the top of the window is the window's
		// own edge and says nothing about the log.
		if i > m.top {
			if d, ok := gap(entries[i-1], e); ok {
				body = append(body, gapRow(d, e.At, inner))
				if len(body) == height {
					break
				}
			}
		}
		row := renderLine(e, m.tags[e.Source], m.gutter(e), i == m.cursor, m.hoff, inner)
		switch {
		case i == m.cursor:
			row = cursorRow(row, inner)
		case e.Note:
			row = noteRow(row, inner)
		case e.Band:
			row = bandRow(row, inner)
		}
		body = append(body, row)
	}
	for len(body) < height {
		body = append(body, "")
	}

	screen := []string{
		m.topBar(entries),
		styleBox.Width(m.width()).Render(strings.Join(body, "\n")),
		m.filterBar(),
	}
	screen = append(screen, m.promptRows()...)
	return padScreen(strings.Join(append(screen, m.footer(entries)), "\n"))
}

// promptRows are what is drawn under the prompt: why what is being typed is not
// a query yet, or, when it could still become one, what it might be finished
// with.
//
// They sit on their own rows rather than beside the prompt because both are as
// long as they need to be — a parse error naming what it expected does not fit
// after a query long enough to have gone wrong.
func (m logModel) promptRows() []string {
	if !m.searching {
		return nil
	}
	if m.queryErr != nil {
		return []string{ansi.Truncate("  "+styleErr.Render(m.queryErr.Error()), m.width(), "…")}
	}
	return m.suggestRows()
}

// suggestRows draws what the prompt is offering. The value is shown as it would
// be typed and the detail says where it came from, since a name the database
// knows and one this stream has already used are worth telling apart: the first
// may match nothing here.
func (m logModel) suggestRows() []string {
	items := m.suggestions()
	if len(items) == 0 {
		return nil
	}
	sel := min(m.sel, len(items)-1)
	rows := make([]string, 0, len(items))
	for i, c := range items {
		marker := "  "
		if i == sel {
			marker = styleSelected.Render("▎ ")
		}
		row := marker + highlightMatch(c.Value, c.Matched, i == sel)
		if c.Detail != "" {
			row += "  " + styleDim.Render(c.Detail)
		}
		rows = append(rows, ansi.Truncate(row, m.width(), "…"))
	}
	return rows
}

// filterBar shows where the stream comes from and what it reads as colored
// chips, followed by the grep filter.
// chips label where the stream comes from and what it reads.
func (m logModel) chips() string {
	if m.cfg.Collector == source.CollectorMerge {
		// The sources are the legend for the tags down the gutter, so they are
		// colored to match rather than by what they are.
		var out strings.Builder
		for i, l := range m.cfg.Labels() {
			out.WriteString(tagStyle(i).Render(" " + l + " "))
		}
		return out.String()
	}
	where := "local"
	if m.cfg.Transport == source.TransportSSH {
		where = strings.TrimSpace(m.cfg.Host)
	}
	return styleChipWhere.Render(where) + styleChipActive.Render(string(m.cfg.Collector))
}

func (m logModel) filterBar() string {
	chips := m.chips()

	var input string
	switch {
	case m.searching:
		input = m.search.View()
	case m.view.Filter().Query != "":
		input = styleFilter.Render(m.view.Filter().Query)
	default:
		input = styleHint.Render("/ to filter")
	}
	return ansi.Truncate(chips+"  "+input, m.width(), "…")
}

// renderLine renders one entry, ANSI colors intact, honoring the horizontal
// offset and available width.
//
// The tag and the gutter are drawn outside the horizontal offset: where a line
// came from and when it was written are the last things that should scroll away
// from it.
func renderLine(e *logs.Entry, tag, gutter string, selected bool, hoff, width int) string {
	marker := "  "
	if selected {
		marker = styleSelected.Render("▎ ")
	}
	if tag != "" {
		marker += tag + " "
	}
	marker += gutter
	text := e.Head
	if e.Stderr {
		text = styleErr.Render("!") + " " + text
	}
	if e.Extra > 0 {
		// A stacktrace would otherwise take over the list; the entry view has it.
		text += styleDim.Render(fmt.Sprintf(" ⏎%d", e.Extra))
	}
	if hoff > 0 {
		text = ansi.TruncateLeft(text, hoff, "")
	}
	return marker + ansi.Truncate(text, max(width-lipgloss.Width(marker), 1), styleDim.Render("→"))
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
	if older := m.olderText(); older != "" {
		stats = append(stats, older)
	}
	stats = append(stats, filter.Describe())
	stats = append(stats, "follow "+onOff(m.follow))
	stats = append(stats, m.statusText())

	inner := max(m.width()-2, 1)
	line := styleDim.Render(strings.Join(stats, " · "))
	return styleBox.Width(m.width()).Render(
		ansi.Truncate(title, inner, "…") + "\n" + ansi.Truncate(line, inner, "…"),
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
		keys := []string{key("enter", "apply")}
		if len(m.suggestions()) > 0 {
			keys = append(keys, key("tab", "complete"), key("↑↓", "pick"))
		}
		keys = append(keys, key("f1", "syntax"), key("esc", "cancel"))
		return ansi.Truncate(strings.Join(keys, styleHint.Render(" · ")), m.width(), "")
	}
	help := strings.Join([]string{
		key("↑↓", "move"),
		key("enter", "entry"),
		key("/", "filter"),
		key("?", "syntax"),
		key("f", "follow"),
		key("l", "level"),
		key("t", "time"),
		key("←→", "scroll"),
		key("home/end", "ends"),
		key("esc", "sources"),
		key("q", "quit"),
	}, styleHint.Render(" · "))
	if len(entries) == 0 && m.store.Len() > 0 {
		why := "no lines match"
		// Which of the two it is, since they are fixed differently: a value
		// nothing has is a value to change, and a field nothing has is a filter
		// that was never going to match however it was spelled.
		if missing := m.missingFields(); len(missing) > 0 {
			why += " · no line carries " + strings.Join(missing, " or ")
		}
		help = styleHint.Render(why+" · ") + help
	}
	return ansi.Truncate(help, m.width(), "")
}

// missingFields are the names the filter compares that no line has carried.
func (m logModel) missingFields() []string {
	var out []string
	for _, key := range filterFields(m.view.Filter().Expr()) {
		if !m.store.HasField(key) && !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}

// filterFields are the names a query compares, in the order it names them.
func filterFields(e query.Expr) []string {
	switch e := e.(type) {
	case query.And:
		return slices.Concat(mapSlice(e, filterFields)...)
	case query.Or:
		return slices.Concat(mapSlice(e, filterFields)...)
	case query.Not:
		return filterFields(e.Expr)
	case query.Field:
		return []string{e.Key}
	default:
		return nil
	}
}

func mapSlice[T, R any](in []T, fn func(T) R) []R {
	out := make([]R, 0, len(in))
	for _, v := range in {
		out = append(out, fn(v))
	}
	return out
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
