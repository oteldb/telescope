package ui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/oteldb/telescope/internal/source"
	"github.com/oteldb/telescope/internal/trace"
)

// The fields of the search form, in the order they are read down the screen.
//
// It is Jaeger's form rather than a query prompt because a trace is looked for
// by what is known about it — who ran it, what it was called, how slow it was —
// and those are fields. The language underneath is [source.TraceQuery]'s
// business; what a store is actually asked is compiled from these.
type searchField int

const (
	fieldService searchField = iota
	fieldOperation
	fieldTags
	fieldMin
	fieldMax
	fieldRange
	fieldLimit
	searchFields
)

// focusResults is the focus past the last field: the list of what came back.
// It is one more stop on the same cycle rather than a mode of its own, so tab
// walks from the form into the results and round again.
const focusResults = int(searchFields)

// maxSuggestions is how many names are offered under a field at once. The list
// is drawn over the results, so it may not take the screen.
const maxSuggestions = 6

func (f searchField) label() string {
	switch f {
	case fieldService:
		return "service"
	case fieldOperation:
		return "operation"
	case fieldTags:
		return "tags"
	case fieldMin:
		return "min"
	case fieldMax:
		return "max"
	case fieldRange:
		return "range"
	default:
		return "limit"
	}
}

// searchModel is the trace search: a form, and what the store answered.
type searchModel struct {
	w, h int
	// at is the store being searched. It is held rather than resolved on demand
	// because a search screen belongs to one store: the results are its trace
	// ids, and an id means nothing to a different one.
	at source.Endpoint

	inputs [searchFields]textinput.Model
	focus  int

	// services and operations are what the store says it holds, offered under
	// the two fields they belong to. opsFor is the service they were listed
	// for, since Jaeger indexes them per service and the answer goes stale the
	// moment somebody types a different one.
	services, operations []string
	opsFor               string
	// tagKeys are what the store says its spans are labeled with and tagValues
	// what one of those tags has been; valuesFor is which tag they belong to,
	// which goes stale for the reason opsFor does — a list of values under a key
	// nobody is typing any more is a list of the wrong thing.
	tagKeys, tagValues []string
	valuesFor          string
	// sug is which suggestion is highlighted, or -1 for none. Typing clears it:
	// a highlight left standing would accept a name nobody was looking at.
	sug int

	results []trace.Result
	// trees is what a search answered with, when it answered with the traces
	// themselves. Jaeger does, so the gantt for a row here has already been
	// read and asking for it again would be the same bytes twice.
	trees map[string]*trace.Tree

	cursor, off int

	// searching is a request that has not answered yet, and err is why one did
	// not. asked is the query the results on screen answered, so the footer can
	// say what is being looked at rather than what is being typed.
	searching bool
	err       error
	asked     source.TraceQuery
	ran       bool

	note string
}

func newSearch(at source.Endpoint) searchModel {
	m := searchModel{at: at, sug: -1}
	for f := range searchFields {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = f.placeholder(at)
		m.inputs[f] = ti
	}
	m.inputs[fieldService].Focus()
	return m
}

func (f searchField) placeholder(at source.Endpoint) string {
	switch f {
	case fieldService:
		if at.Collector == source.CollectorJaeger {
			// Jaeger indexes per service and will not search without one, so
			// the field says it is required where somebody is about to skip it.
			return "api — required here, this store searches by service"
		}
		return "api — empty searches every service"
	case fieldOperation:
		return "GET /v1/orders"
	case fieldTags:
		return `http.status_code=500 error=true`
	case fieldMin:
		return "100ms — traces at least this slow"
	case fieldMax:
		return "2s"
	case fieldRange:
		return "1h, 6h..1h, today — empty is the last hour"
	default:
		return "20"
	}
}

func (m *searchModel) resize(w, h int) {
	m.w, m.h = w, h
	for f := range searchFields {
		m.inputs[f].Width = max(m.bodyWidth()-labelWidth-2, 10)
	}
}

// labelWidth is the column the field names are drawn in, wide enough for the
// longest of them.
const labelWidth = 9

func (m searchModel) bodyWidth() int { return max(m.w-2*screenPad-2, 20) }

func (m searchModel) bodyHeight() int { return max(m.h-4, 1) }

// listHeight is what is left for the results once the form has taken its rows:
// one per field, the blank line under them, and the line that says what the
// search did.
func (m searchModel) listHeight() int {
	return max(m.bodyHeight()-int(searchFields)-2, 1)
}

func (m searchModel) Update(msg tea.Msg) (searchModel, tea.Cmd) {
	switch msg := msg.(type) {
	case noteMsg:
		m.note = msg.text
		return m, nil
	case searchLoadedMsg:
		m.searching, m.err = false, nil
		m.results, m.trees = msg.results, msg.trees
		m.cursor, m.off = 0, 0
		if len(m.results) > 0 {
			m.focus = focusResults
			m.blurAll()
		}
		return m, nil
	case searchErrMsg:
		m.searching, m.err = false, msg.err
		m.results, m.trees = nil, nil
		return m, nil
	case searchNamesMsg:
		switch msg.field {
		case fieldService:
			m.services = msg.names
		case fieldTags:
			if msg.of == "" {
				m.tagKeys = msg.names
			} else {
				m.tagValues, m.valuesFor = msg.names, msg.of
			}
		default:
			m.operations, m.opsFor = msg.names, msg.of
		}
		return m, nil
	}

	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	m.note = ""
	if m.focus == focusResults {
		return m.updateResults(km)
	}
	return m.updateForm(km)
}

func (m searchModel) updateForm(km tea.KeyMsg) (searchModel, tea.Cmd) {
	names := m.suggestions()

	switch km.String() {
	case "q":
		// A form is being typed into, so q is a letter here and nowhere else on
		// this screen. Only ctrl+c quits from a prompt.
	case "esc":
		if m.sug >= 0 {
			m.sug = -1
			return m, nil
		}
		return m, func() tea.Msg { return backMsg{} }

	case "tab":
		return m.move(1)
	case "shift+tab":
		return m.move(-1)

	case "down", "ctrl+n":
		if len(names) > 0 {
			m.sug = min(m.sug+1, len(names)-1)
			return m, nil
		}
		return m.move(1)
	case "up", "ctrl+p":
		if m.sug >= 0 {
			m.sug--
			return m, nil
		}
		if len(names) == 0 {
			return m.move(-1)
		}
		return m, nil

	case "enter":
		if m.sug >= 0 && m.sug < len(names) {
			m.take(names[m.sug])
			m.sug = -1
			return m, m.listNames()
		}
		return m.run()

	case "ctrl+r":
		return m.run()
	}

	before := m.inputs[m.focus].Value()
	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(km)
	if m.inputs[m.focus].Value() != before {
		m.sug = -1
		// The operations a service was called to do go stale as soon as the
		// service does, and a keystroke in the tag field moves between a key and
		// its values, which are two different lists to have asked for.
		switch searchField(m.focus) {
		case fieldService, fieldTags:
			return m, tea.Batch(cmd, m.listNames())
		}
	}
	return m, cmd
}

// take writes an offered name into the field it was offered under.
//
// The tag field takes it in place of the term the cursor is in rather than as
// the whole value, since it holds several pairs and finishing one must not
// throw away the rest.
func (m *searchModel) take(name string) {
	if searchField(m.focus) == fieldTags {
		at := m.tagAt()
		value, pos := at.apply(m.value(fieldTags), name, at.Key == "")
		m.inputs[fieldTags].SetValue(value)
		m.inputs[fieldTags].SetCursor(pos)
		return
	}
	m.inputs[m.focus].SetValue(name)
	m.inputs[m.focus].CursorEnd()
}

func (m searchModel) updateResults(km tea.KeyMsg) (searchModel, tea.Cmd) {
	switch km.String() {
	case "q":
		return m, func() tea.Msg { return quitMsg{} }
	case "esc":
		// Back to the form rather than off the screen: a search that found the
		// wrong thing is edited, and only a search nobody wants is left.
		m.focus = 0
		m.inputs[0].Focus()
		return m, nil
	case "tab":
		return m.move(1)
	case "shift+tab":
		return m.move(-1)
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.results)-1, 0))
	case "pgup":
		m.cursor = max(m.cursor-m.listHeight(), 0)
	case "pgdown":
		m.cursor = min(m.cursor+m.listHeight(), max(len(m.results)-1, 0))
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(len(m.results)-1, 0)
	case "ctrl+r":
		return m.run()
	case "enter":
		r, ok := m.selected()
		if !ok {
			return m, nil
		}
		at := m.at
		return m, func() tea.Msg {
			return openTraceMsg{id: r.TraceID, at: &at, tree: m.trees[r.TraceID]}
		}
	case "y":
		if r, ok := m.selected(); ok {
			return m, copyCmd("trace_id", r.TraceID)
		}
	}
	m.clamp()
	return m, nil
}

// selected is the result under the cursor.
func (m searchModel) selected() (trace.Result, bool) {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return trace.Result{}, false
	}
	return m.results[m.cursor], true
}

// clamp keeps the cursor inside the window drawn.
func (m *searchModel) clamp() {
	height := m.listHeight()
	m.off = min(m.off, m.cursor)
	if m.cursor >= m.off+height {
		m.off = m.cursor - height + 1
	}
	m.off = max(m.off, 0)
}

// move walks the focus over the fields and the results, skipping the results
// when there are none to walk.
func (m searchModel) move(d int) (searchModel, tea.Cmd) {
	m.sug = -1
	stops := focusResults + 1
	if len(m.results) == 0 {
		stops = focusResults
	}
	m.focus = ((m.focus+d)%stops + stops) % stops
	m.blurAll()
	var cmd tea.Cmd
	if m.focus != focusResults {
		cmd = m.inputs[m.focus].Focus()
	}
	return m, tea.Batch(cmd, m.listNames())
}

func (m *searchModel) blurAll() {
	for f := range searchFields {
		m.inputs[f].Blur()
	}
}

// run compiles the form and asks the store.
func (m searchModel) run() (searchModel, tea.Cmd) {
	q, err := m.query()
	if err == nil {
		err = q.Validate(m.at.Collector)
	}
	if err != nil {
		// Said where the form is rather than as a failed search: nothing was
		// asked, and clearing the results would throw away a good answer over a
		// typing mistake in the next question.
		m.err, m.searching = err, false
		return m, nil
	}
	// The window is resolved before it is asked for, so the line under the form
	// says the interval the results actually came out of rather than the one
	// that was typed — which, for a blank range, was nothing at all.
	q.Range = q.Window(time.Now())
	m.err, m.searching, m.ran, m.asked = nil, true, true, q
	m.sug = -1
	return m, searchTraces(m.at, q)
}

// query reads the form as a query, saying which field it could not read rather
// than which parser complained.
func (m searchModel) query() (source.TraceQuery, error) {
	q := source.TraceQuery{
		Service:   strings.TrimSpace(m.value(fieldService)),
		Operation: strings.TrimSpace(m.value(fieldOperation)),
	}
	var err error
	if q.Tags, err = source.ParseTags(m.value(fieldTags)); err != nil {
		return q, errField(fieldTags, err)
	}
	if q.MinDuration, err = parseSearchDur(m.value(fieldMin)); err != nil {
		return q, errField(fieldMin, err)
	}
	if q.MaxDuration, err = parseSearchDur(m.value(fieldMax)); err != nil {
		return q, errField(fieldMax, err)
	}
	if q.Range, err = source.ParseRange(m.value(fieldRange), time.Now()); err != nil {
		return q, errField(fieldRange, err)
	}
	if spec := strings.TrimSpace(m.value(fieldLimit)); spec != "" {
		n, convErr := strconv.Atoi(spec)
		if convErr != nil || n <= 0 {
			return q, errField(fieldLimit, errNotACount)
		}
		q.Limit = n
	}
	return q, nil
}

func (m searchModel) value(f searchField) string { return m.inputs[f].Value() }

var errNotACount = errSearch("is not a number of traces")

// errSearch is a complaint about a field, worded as the field itself so the
// message reads as one sentence with the label in front of it.
type errSearch string

func (e errSearch) Error() string { return string(e) }

func errField(f searchField, err error) error {
	return errSearch(f.label() + ": " + err.Error())
}

// parseSearchDur reads a duration bound. Empty is no bound, which is not the
// same as zero.
func parseSearchDur(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, errSearch("is not a duration, as in 100ms or 2s")
	}
	if d < 0 {
		return 0, errSearch("is not a duration, as in 100ms or 2s")
	}
	return d, nil
}

// suggestions are the names offered under the focused field, narrowed by what
// has been typed into it.
func (m searchModel) suggestions() []string {
	switch searchField(m.focus) {
	case fieldService:
		return narrowNames(m.services, m.value(fieldService))
	case fieldOperation:
		if m.opsFor != m.operationsFor() {
			// The list belongs to a different service. Offering it would name
			// operations this one was never called to do, which is worse than
			// offering nothing while the new list is on its way.
			return nil
		}
		return narrowNames(m.operations, m.value(fieldOperation))
	case fieldTags:
		at := m.tagAt()
		switch {
		case !at.OK:
			return nil
		case at.Key == "":
			return narrowNames(m.tagKeys, at.Prefix)
		case m.valuesFor != at.Key:
			// Values listed under another key, for the reason above: they are
			// what some other tag has been.
			return nil
		default:
			return narrowNames(m.tagValues, at.Prefix)
		}
	default:
		return nil
	}
}

// tagAt is what the tag field is being asked to finish, read off the text
// around the cursor rather than off the whole field: it holds a run of
// key=value pairs, and which half of one the cursor is in is what says whether
// a name or a value is wanted. That is the same reading the filter prompt makes
// of a query, so it is the same function that makes it.
func (m searchModel) tagAt() completion {
	return completeAt(m.value(fieldTags), m.inputs[fieldTags].Position())
}

// narrowNames keeps the names that hold what has been typed of one, at most a
// screenful of them.
func narrowNames(all []string, typed string) []string {
	typed = strings.ToLower(strings.TrimSpace(typed))
	var out []string
	for _, name := range all {
		if typed == "" || strings.Contains(strings.ToLower(name), typed) {
			out = append(out, name)
		}
		if len(out) == maxSuggestions {
			break
		}
	}
	// A single suggestion that is already what was typed is not a suggestion.
	if len(out) == 1 && strings.EqualFold(out[0], typed) {
		return nil
	}
	return out
}

// listNames asks the store what it holds, for the field being typed into. What
// has already been answered is not asked again: the two lists change about as
// often as a deploy, and a request per keystroke would be a request per
// keystroke.
func (m searchModel) listNames() tea.Cmd {
	switch searchField(m.focus) {
	case fieldService:
		if m.services != nil {
			return nil
		}
		return listTraceNames(m.at, fieldService, "")
	case fieldOperation:
		service := m.operationsFor()
		if m.operations != nil && m.opsFor == service {
			return nil
		}
		return listTraceNames(m.at, fieldOperation, service)
	case fieldTags:
		at := m.tagAt()
		switch {
		case !at.OK:
			return nil
		case at.Key == "":
			if m.tagKeys != nil {
				return nil
			}
			return listTraceNames(m.at, fieldTags, "")
		case m.tagValues != nil && m.valuesFor == at.Key:
			return nil
		default:
			return listTraceNames(m.at, fieldTags, at.Key)
		}
	default:
		return nil
	}
}

// operationsFor is the service the operation list belongs to, which is the one
// typed for a store that indexes them per service and nobody for a store that
// does not. Tempo has no such index and answers with every span name it holds,
// so its list is the same list whatever the service field says — and must not
// be thrown away every time somebody types a letter into it.
func (m searchModel) operationsFor() string {
	if m.at.Collector != source.CollectorJaeger {
		return ""
	}
	return strings.TrimSpace(m.value(fieldService))
}
