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

	// cursor and top count rows and not lines: a run of one line repeated is
	// one row, and a cursor that could land inside a folded run would be
	// somewhere the reader cannot see.
	cursor int
	// top is the first row drawn, kept as real state so the cursor can move
	// within the window without the window moving under it.
	top    int
	hoff   int
	follow bool
	// clamped folds a line repeated straight after itself into the row above.
	// On by default: a log that says the same thing four hundred times is
	// saying one thing, and the list should read as one.
	clamped bool

	// volume draws the log volume panel above the list. On by default: how much
	// of a log there was and when is the first thing read about one, and it
	// costs rows only where there are rows to spare — see [logModel.volumeShown].
	volume bool

	// cols are the gutter columns this stream has turned out to need, and
	// origins what tells its streams apart where it turned out to be several.
	cols    columns
	origins logs.Origins
	// palette is which color each stream reads in, kept on the view because a
	// color is only worth anything against the other streams of the same list.
	palette *originPalette
	// resolved is how many lines origins was worked out from, so that a view
	// drawn from a store somebody else appended to still names them.
	resolved int
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

	// paging is a page of older lines in flight, atStart is a source that
	// answered there are none, atCap is the store with no room for another and
	// pageErr is why the last one did not arrive. See [logModel.wantPage].
	paging  bool
	atStart bool
	atCap   bool
	pageErr error

	status string
	err    error
	// note is what the last key did, or why it could not, shown until the next
	// one. A key that opens nothing has to leave some evidence that it was
	// pressed at all.
	note string
}

func newLogs(cfg source.Config, store *logs.Store, query string) logModel {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colorAccent)
	ti.Placeholder = `word, "phrase", /regexp/, pod=api, level>=warn`
	ti.SetValue(query)

	return logModel{
		cfg:     cfg,
		store:   store,
		view:    logs.NewView(logs.Filter{Query: query}),
		follow:  true,
		clamped: true,
		volume:  true,
		origin:  time.Now(),
		palette: newOriginPalette(cfg.Labels()),
		search:  ti,
		status:  "connecting",
	}
}

// columns are the fields drawn to the left of a line, as opposed to inside it.
//
// Everything a line says about itself rather than about what happened belongs
// here, where it lines up down the screen: a severity written wherever the
// message left off is a severity nobody can scan. They are turned on by the
// first line that needs them and stay on, so the text does not shift left and
// right as lines that have them and lines that do not arrive together.
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
	m.cols.level = m.cols.level || e.Record.HasLevel
}

// resolve works out again what tells the streams apart, which can only change
// when a line arrives: a place turns out to be several streams as they speak,
// and which label separates them is not named anywhere before that.
func (m *logModel) resolve() {
	if seen := m.store.Len() + m.store.Dropped(); seen != m.resolved {
		m.origins, m.resolved = m.store.Origins(), seen
	}
}

// runs are the rows the list draws, which is not one per line: see [clampRuns].
func (m logModel) runs(entries []*logs.Entry) []run {
	return clampRuns(entries, m.clamped, m.origins)
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
		case e.Record.HasLevel:
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
	if h := m.h - 8 - m.volumeHeight() - len(m.promptRows()); h > 0 {
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
	if msg, ok := msg.(noteMsg); ok {
		m.note = msg.text
		return m, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.searching {
		return m.updateSearch(km)
	}
	m.note = ""
	entries := m.view.Entries(m.store)
	runs := m.runs(entries)
	// at is the line a row draws, which is what every key that works from the
	// cursor wants: the row is how the list is counted and never what it is
	// about.
	at := func(i int) *logs.Entry {
		if i < 0 || i >= len(runs) {
			return nil
		}
		return entries[runs[i].first]
	}

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
		if e := at(m.cursor); e != nil {
			return m, func() tea.Msg { return openEntryMsg{entry: e} }
		}
	case "f":
		m.follow = !m.follow
		m.syncFollow()
	case "t":
		m.times = m.times.next()
	case "v":
		m.volume = !m.volume
	case "c":
		// The cursor is on a line and not on a row, so it stays on the line it
		// was on: folding the rows under it must not move the reader.
		line := 0
		if m.cursor >= 0 && m.cursor < len(runs) {
			line = runs[m.cursor].first
		}
		m.clamped = !m.clamped
		m.cursor = runAt(m.runs(entries), line)
	case "y":
		return m.copyLink()
	case "l":
		f := m.view.Filter()
		f.MinLevel = f.MinLevel.Next()
		m.view.SetFilter(f)
		m.cursor, m.top = 0, 0
	case "up", "k":
		m.move(-1, len(runs))
	case "down", "j":
		m.move(1, len(runs))
	case "pgup":
		m.move(-m.bodyHeight(), len(runs))
	case "pgdown":
		m.move(m.bodyHeight(), len(runs))
	case "home", "g":
		m.follow = false
		m.cursor, m.top = 0, 0
	case "end", "G":
		m.follow = true
		m.cursor = max(len(runs)-1, 0)
	case "H":
		// Top and bottom of what is on screen, as in less and vim.
		m.follow = false
		m.cursor = m.top
	case "L":
		m.cursor = m.top + m.bodyHeight() - 1
		m.follow = m.cursor >= len(runs)-1
	case "T":
		// The trace this line was written inside. A line that was not in one
		// says so rather than opening a screen with nothing on it.
		if e := at(m.cursor); e != nil && e.Record.TraceID != "" {
			return m, openTrace(e)
		}
		return m, note("this line is not in a trace")
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

// narrowed reports whether what is on screen is less than what was read, by
// either of the two things that narrow it: the query typed at the prompt and
// the level the view cycles.
func (m logModel) narrowed() bool {
	f := m.view.Filter()
	return f.Query != "" || f.MinLevel != logs.LevelAll
}

// syncFollow pins the cursor to the newest entry while following. It runs when
// entries arrive rather than at render time, so keys that work from the cursor
// see where the view actually is.
func (m *logModel) syncFollow() {
	if !m.follow {
		return
	}
	if n := len(m.runs(m.view.Entries(m.store))); n > 0 {
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
	runs := m.runs(entries)
	n := len(runs)
	h := m.bodyHeight()

	m.cursor = min(max(m.cursor, 0), max(n-1, 0))
	m.top = min(max(m.top, 0), m.cursor)
	for m.top < m.cursor && m.rows(entries, runs, m.top, m.cursor) > h {
		m.top++
	}
}

// rows is how much of the screen the rows from..to take, the gaps between them
// included. A run is one row however many lines it stands for, and the silence
// before it is measured from the last of the run above and not the first.
func (m logModel) rows(entries []*logs.Entry, runs []run, from, to int) int {
	n := to - from + 1
	for i := from + 1; i <= to && i < len(runs); i++ {
		if _, ok := gap(entries[runs[i-1].last()], entries[runs[i].first]); ok {
			n++
		}
	}
	return n
}

func (m logModel) View() string {
	m.resolve()
	entries := m.view.Entries(m.store)
	runs := m.runs(entries)
	m.clamp()

	height := m.bodyHeight()

	inner := max(m.width()-2, 10)
	body := make([]string, 0, height)
	for i := m.top; i < len(runs) && len(body) < height; i++ {
		r := runs[i]
		e := entries[r.first]
		// The silence before a line is drawn above it, and only where the line
		// above is on screen: a gap at the top of the window is the window's
		// own edge and says nothing about the log.
		if i > m.top {
			if d, ok := gap(entries[runs[i-1].last()], e); ok {
				body = append(body, gapRow(d, e.At, inner, m.narrowed()))
				if len(body) == height {
					break
				}
			}
		}
		row := renderLine(e, originRow(m.origins, m.store.Row(e)), originCell(m.palette, m.origins, e), m.gutter(e), r.n, i == m.cursor, m.hoff, inner)
		switch {
		case i == m.cursor:
			row = cursorRow(row, inner)
		case e.Kind.IsNote():
			row = noteRow(row, inner)
		case e.Band:
			row = bandRow(row, inner)
		}
		body = append(body, row)
	}
	for len(body) < height {
		body = append(body, "")
	}

	screen := []string{m.topBar(entries, len(entries)-len(runs))}
	if m.volumeShown() {
		// The cursor is passed in so the chart can mark where in the log the
		// reader is: the panel is above the list it belongs to, not beside it.
		var at *logs.Entry
		if m.cursor >= 0 && m.cursor < len(runs) {
			at = entries[runs[m.cursor].first]
		}
		screen = append(screen, m.volumePanel(entries, at))
	}
	screen = append(screen,
		styleBox.Width(m.width()).Render(strings.Join(body, "\n")),
		m.filterBar(),
	)
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
		// The places are the legend for the column down the gutter, so they are
		// colored the way it colors them rather than by what they are.
		var out strings.Builder
		for _, l := range m.cfg.Labels() {
			out.WriteString(m.palette.style(l).Render(" " + l + " "))
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
//
// What the fields are and what order they come in was decided in logs; how many
// of them there is room for is decided here, on the width the terminal is now.
// A rendering that spent the width when the line arrived could not survive the
// window being dragged wider.
func renderLine(e *logs.Entry, fields []logs.RowField, tag, gutter string, repeat int, selected bool, hoff, width int) string {
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
	// The counts are written before the fields are laid out, since they say how
	// much of the line the row is not showing and a field crowding them off the
	// screen would be the row hiding that it hid something.
	var tail string
	if e.Extra > 0 {
		// A stacktrace would otherwise take over the list; the entry view has it.
		tail += styleDim.Render(fmt.Sprintf(" ⏎%d", e.Extra))
	}
	if repeat > 1 {
		// What the line said, and how many times running it said it. The count
		// is the row's and not the line's, which is why it is drawn here and
		// not folded into the rendering when the line arrived.
		tail += styleDim.Render(fmt.Sprintf(" ×%d", repeat))
	}
	room := width - lipgloss.Width(marker) - lipgloss.Width(text) - lipgloss.Width(tail)
	text += spendFields(fields, room) + tail
	if hoff > 0 {
		text = ansi.TruncateLeft(text, hoff, "")
	}
	return marker + ansi.Truncate(text, max(width-lipgloss.Width(marker), 1), styleDim.Render("→"))
}

// spendFields writes as many of a row's fields as the room left will take, and
// counts what it could not.
//
// The count is not decoration: a row that quietly stopped after two fields
// would read as a line with two fields, and the reader would have no reason to
// open it. It is the same promise the ⏎ and × marks make about the lines this
// row stands for.
func spendFields(fields []logs.RowField, room int) string {
	var (
		out   strings.Builder
		spent int
	)
	for i, f := range fields {
		text := "  " + f.Render()
		// Whatever is written must leave room to say what is not, unless this
		// is the last one and there will be nothing left to say.
		reserve := 0
		if i < len(fields)-1 {
			reserve = lipgloss.Width(moreFields(len(fields) - i - 1))
		}
		if spent+lipgloss.Width(text)+reserve > room {
			return out.String() + styleDim.Render(moreFields(len(fields)-i))
		}
		out.WriteString(text)
		spent += lipgloss.Width(text)
	}
	return out.String()
}

// moreFields is how the row says it is carrying more than it showed.
func moreFields(n int) string { return fmt.Sprintf("  +%d", n) }

func (m logModel) footer(entries []*logs.Entry) string {
	if m.note != "" && !m.searching {
		return ansi.Truncate(styleOK.Render(m.note), m.width(), "")
	}
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
		key("c", "clamp"),
		key("t", "time"),
		key("v", "volume"),
		key("y", "link"),
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

// textinputBlink is the initial cursor blink for the start screen.
var textinputBlink = textinput.Blink
