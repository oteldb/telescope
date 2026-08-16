package ui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/trace"
)

// spanRecord is one span as a query sees it.
//
// It exists so the chart is narrowed by the language the log list already
// takes, rather than by a second matcher that would have to be taught the same
// operators and would spell a field differently on the way. `internal/query`
// evaluates against an interface for exactly this: what a record is belongs to
// whoever holds one.
type spanRecord struct{ n *trace.Node }

var _ query.Record = spanRecord{}

// Haystack is what a bare word matches: what the span is called, who ran it,
// why it failed, and what its attributes say. The keys are left out for the
// reason a log line's are — searching for "http" should not select every span
// that happens to carry an http attribute, only the ones that mention it.
func (r spanRecord) Haystack() [][]byte {
	out := make([][]byte, 0, len(r.n.Attrs)+3)
	out = append(out, []byte(r.n.Name), []byte(r.n.Service), []byte(r.n.StatusMessage))
	for _, f := range r.n.Attrs {
		out = append(out, []byte(f.String()))
	}
	return out
}

// Level is the one severity a span can report: OTLP says it ended in an error
// or it says nothing. A span that said nothing is not silently an info span, so
// it reports none and passes no comparison — the rule a log line with no level
// already follows.
func (r spanRecord) Level() (zapcore.Level, bool) {
	if r.n.Failed() {
		return zapcore.ErrorLevel, true
	}
	return 0, false
}

// Field resolves a name a query compares against: what the span called its
// attribute, then the names a span is read under.
//
// The names are the rows of the span document that are worth filtering by, and
// for the same reason [narrowsLogs] gives: a duration is not something a span
// carries as a label, and comparing one as text would answer a question nobody
// meant to ask.
func (r spanRecord) Field(key string) (string, bool) {
	if v, ok := r.attr(key); ok {
		return v, true
	}
	// Failing that, the name a shipper would have kept it under: service.name
	// and service_name are one attribute written twice, which is the rule the
	// log list resolves a field by.
	if under := strings.ReplaceAll(key, ".", "_"); under != key {
		if v, ok := r.attr(under); ok {
			return v, true
		}
	}
	switch strings.ToLower(key) {
	case "service", "service.name", "service_name", "resource.service.name":
		return r.n.Service, r.n.Service != ""
	case "name", "span.name", "span_name", "operation":
		return r.n.Name, r.n.Name != ""
	case "span_id", "spanid":
		return r.n.SpanID, r.n.SpanID != ""
	case "parent_id", "parentid":
		return r.n.ParentID, r.n.ParentID != ""
	case "trace_id", "traceid":
		return r.n.TraceID, r.n.TraceID != ""
	case "status":
		switch {
		case r.n.Failed():
			return "error", true
		case r.n.Status == trace.StatusOK:
			return "ok", true
		}
	}
	return "", false
}

func (r spanRecord) attr(key string) (string, bool) {
	for _, f := range r.n.Attrs {
		if f.Key == key {
			return f.String(), true
		}
	}
	return "", false
}

// spanFields indexes what the spans of one trace are labeled with, keyed by the
// attribute whose values these are and by "" for the names themselves.
//
// It is read off the trace and never asked of the store, unlike either log
// prompt: everything the filter can select is already on this screen, so a
// listing would be a round trip to be told what the spans in hand already say —
// and worse, it would offer names no span here carries.
func spanFields(t *trace.Tree) map[string][]string {
	names := map[string]bool{}
	values := map[string]map[string]bool{}
	put := func(key, value string) {
		if key == "" || value == "" {
			return
		}
		names[key] = true
		if values[key] == nil {
			values[key] = map[string]bool{}
		}
		values[key][value] = true
	}
	t.Walk(func(n *trace.Node) bool {
		// Spelled as the span document spells them, so a name read off a row is
		// a name the prompt offers back.
		put("service.name", n.Service)
		put("name", n.Name)
		switch {
		case n.Failed():
			put("status", "error")
		case n.Status == trace.StatusOK:
			put("status", "ok")
		}
		for _, f := range n.Attrs {
			put(f.Key, f.String())
		}
		return true
	})

	out := map[string][]string{"": sortedSet(names)}
	for key, set := range values {
		out[key] = sortedSet(set)
	}
	return out
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// newFilterPrompt is the prompt the chart is narrowed with. It takes the same
// language as the log list's and says so, since a reader who has typed one
// there should not have to find out whether this is the same thing.
func newFilterPrompt() textinput.Model {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colorAccent)
	ti.Placeholder = `word, service.name=api, http.status_code=500, level=error`
	return ti
}

func (m traceModel) updateFilter(km tea.KeyMsg) (traceModel, tea.Cmd) {
	_, items := m.suggest()

	switch km.String() {
	// A "?" typed into the prompt is a regexp quantifier, so the reference is
	// reached by a key that could not have been part of a query.
	case "f1":
		return m, openHelp
	case "tab":
		return m.acceptFilter(items), nil
	case "down", "ctrl+n":
		m.sug = min(m.sug+1, max(len(items)-1, 0))
		return m, nil
	case "up", "ctrl+p":
		m.sug = max(m.sug-1, 0)
		return m, nil
	case "enter":
		spec := strings.TrimSpace(m.filter.Value())
		expr, err := query.Parse(spec)
		if err != nil {
			// A query that does not parse leaves the chart as it was: the prompt
			// stays open on what was typed, which is where it can be fixed.
			m.filterErr = err
			return m, nil
		}
		m.filterErr = nil
		m.mode = traceGantt
		m.filter.Blur()
		m.g.setFilter(expr, spec)
		return m, nil
	case "esc":
		m.mode = traceGantt
		m.filterErr = nil
		m.filter.Blur()
		m.filter.SetValue(m.g.spec)
		return m, nil
	}

	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(km)
	// A keystroke moves what is being completed, so the highlight starts over
	// rather than pointing into a list that has changed under it.
	m.sug = 0
	return m, cmd
}

// suggest is what the prompt is being offered to finish it with, ranked against
// what has been typed.
func (m traceModel) suggest() (completion, []complete.Candidate) {
	at := completeAt(m.filter.Value(), m.filter.Position())
	if !at.OK {
		return at, nil
	}
	// An empty prompt is a word about to be typed far more often than a field
	// name, and a list that took rows off the chart the moment "/" was pressed
	// would be in the way. Naming a field is the exception: "service.name=" was
	// typed precisely to be finished.
	if at.Prefix == "" && at.Key == "" {
		return at, nil
	}
	return at, labelCandidates(m.fields[at.Key], at.Prefix)
}

// acceptFilter writes the highlighted suggestion into the prompt.
func (m traceModel) acceptFilter(items []complete.Candidate) traceModel {
	at, _ := m.suggest()
	if len(items) == 0 || !at.OK {
		return m
	}
	value, pos := at.apply(m.filter.Value(), items[min(m.sug, len(items)-1)].Value, at.Key == "")
	m.filter.SetValue(value)
	m.filter.SetCursor(pos)
	m.sug = 0
	return m
}

// filterRows are what is drawn under the chart: the prompt, and either why what
// is being typed is not a query yet or what it might be finished with.
func (m traceModel) filterRows() []string {
	width := m.w - 2*screenPad
	switch {
	case m.mode == traceFilter:
	case m.g != nil && m.g.spec != "":
		// Not being typed, but in force: what the chart is showing is a
		// selection, and a chart that did not say so would read as the trace.
		return []string{ansi.Truncate(styleFilter.Render(m.g.spec), width, "…")}
	default:
		return nil
	}

	rows := []string{ansi.Truncate(m.filter.View(), width, "…")}
	if m.filterErr != nil {
		return append(rows, ansi.Truncate("  "+styleErr.Render(m.filterErr.Error()), width, "…"))
	}

	_, items := m.suggest()
	if len(items) > suggestRows {
		items = items[:suggestRows]
	}
	if len(items) == 0 {
		return rows
	}
	sel := min(m.sug, len(items)-1)
	for i, c := range items {
		marker := "  "
		if i == sel {
			marker = styleSelected.Render("▎ ")
		}
		rows = append(rows, ansi.Truncate(marker+highlightMatch(c.Value, c.Matched, i == sel), width, "…"))
	}
	return rows
}
