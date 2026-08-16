// Package ui implements the telescope terminal interface.
package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/source"
	"github.com/oteldb/telescope/internal/trace"
)

// storeLimit is how many lines are retained in memory.
const storeLimit = 200_000

// readBatch bounds how many lines are folded into a single update.
const readBatch = 2048

type state int

const (
	stateStart state = iota
	stateLogs
	stateEntry
	stateHelp
	stateTrace
	stateSearch
)

// Model is the root bubbletea model.
type Model struct {
	state state
	w, h  int
	// back is the screen the help was opened over, since it can be read from the
	// list or from the prompt and either is where it should return to.
	back state

	start  startModel
	logs   logModel
	entry  entryModel
	help   helpModel
	trace  traceModel
	search searchModel
	// traceBack is where leaving the trace returns to. A trace telescope was
	// started on has nothing under it, and a start screen that cannot reopen it
	// would be a trapdoor rather than a way back — so that one quits.
	traceBack state
	// searchBack is the same for the search screen, which is reached from the
	// start screen and from the command line both.
	searchBack state
	// traces is what has already been fetched, and asked is the last fetch, kept
	// so a reload knows what to ask again. A trace read from a file leaves asked
	// empty: there is nowhere to ask.
	traces traceCache
	asked  traceAsk

	stream *source.Stream
}

// New returns the root model, opening on the start screen.
func New() Model {
	return Model{start: newStart()}
}

// NewTrace returns the root model opened on one trace, which is what reading a
// trace out of a file does. There is no stream behind it and nothing to go back
// to: the start screen picks a log source, and a trace did not come from one.
func NewTrace(t *trace.Tree) Model {
	return Model{state: stateTrace, traceBack: stateTrace, start: newStart(), trace: newTrace(t)}
}

// NewSearch returns the root model opened on one store's trace search, which is
// what naming a place and no trace id does. As with a trace read from a file
// there is no stream behind it, so leaving is quitting.
func NewSearch(at source.Endpoint) Model {
	return Model{
		state: stateSearch, searchBack: stateSearch,
		start: newStart(), search: newSearch(at),
	}
}

// Init implements [tea.Model].
//
// The listings the first screen will want are warmed through a message rather
// than started here: Init cannot record what it asked for on its value
// receiver, and a request nobody remembers is a request that runs twice.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textinputBlink, func() tea.Msg { return initMsg{} })
}

// Update implements [tea.Model].
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case initMsg:
		return m, m.start.fetch()

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.start, _ = m.start.Update(msg)
		m.logs.resize(msg.Width, msg.Height)
		m.entry.resize(msg.Width, msg.Height)
		m.help.resize(msg.Width, msg.Height)
		m.trace.resize(msg.Width, msg.Height)
		m.search.resize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, m.quit()
		}

	case connectMsg:
		// The filter the view opens with is part of what the sources are asked,
		// not something they learn about on the first keystroke: a place opened
		// on a query has already been narrowed by it.
		cfg := msg.cfg.WithFilter(logs.Filter{Query: msg.query}.Compile().Expr())
		m.logs = newLogs(cfg, logs.NewStore(storeLimit), msg.query)
		m.logs.resize(m.w, m.h)
		m.state = stateLogs
		m.start.history.Remember(msg.cfg)
		return m, tea.Batch(startStream(cfg), saveHistory(m.start.history))

	case requeryMsg:
		// The sources are asked the new query and the view is rebuilt from what
		// they answer. Nothing is remembered again: it is the same place, read
		// through a different filter.
		m.stopStream()
		m.logs = newLogs(msg.cfg, logs.NewStore(storeLimit), msg.query)
		m.logs.resize(m.w, m.h)
		m.logs.status = "requerying"
		return m, startStream(msg.cfg)

	case streamStartedMsg:
		m.stream = msg.stream
		m.logs.status = "streaming"
		return m, tea.Batch(readLines(msg.stream), waitDone(msg.stream))

	case streamErrMsg:
		m.logs.err = msg.err
		m.logs.status = "failed"
		return m, nil

	case linesMsg:
		for _, l := range msg.lines {
			m.logs.append(l)
		}
		m.logs.resolve()
		m.logs.syncFollow()
		if msg.closed {
			return m, nil
		}
		return m, readLines(m.stream)

	case streamDoneMsg:
		m.logs.status = "exited"
		m.logs.err = msg.err
		return m, nil

	case openEntryMsg:
		m.entry = newEntry(m.logs.cfg, msg.entry)
		m.entry.resize(m.w, m.h)
		m.state = stateEntry
		return m, nil

	case filterMsg:
		// A trace that was not opened out of a log list has none under it to
		// narrow, and dropping into an empty one would be worse than saying so.
		// What decides is whether a stream was ever opened, not which screen
		// asked: a trace read from a file and one picked out of a search are the
		// same want of a list.
		if m.logs.cfg.Collector == "" {
			m.say("no logs here: this trace was not opened out of a list")
			return m, nil
		}
		// Narrowing is done to the list, so the list is where it lands: reading
		// one entry is how you find the thing worth narrowing by, and staying on
		// it would hide what the narrowing did.
		m.state = stateLogs
		var cmd tea.Cmd
		m.logs, cmd = m.logs.narrow(msg.term)
		return m, cmd

	case pageMsg:
		m.logs.takePage(msg)
		return m, nil

	case fieldsMsg:
		// Both prompts complete by what a database says it is labeled with, and
		// the one in front of the user is the one that asked.
		if m.state == stateStart {
			m.start.takeFields(msg)
			return m, nil
		}
		m.logs.takeFields(msg)
		return m, nil

	case noteMsg:
		m.say(msg.text)
		return m, nil

	case openSearchMsg:
		m.searchBack = m.state
		m.search = newSearch(msg.at)
		m.search.resize(m.w, m.h)
		m.state = stateSearch
		// What the store holds is asked for now rather than on the first
		// keystroke: the service field is where somebody starts, and a list that
		// arrives while they are still reading the form has cost nothing.
		return m, listTraceNames(msg.at, fieldService, "")

	case searchLoadedMsg, searchErrMsg, searchNamesMsg:
		// Routed by hand rather than by which screen is up: a search answers
		// after somebody has already opened one of its results, and an answer
		// dropped for that would leave the list behind them empty.
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return m, cmd

	case openTraceMsg:
		if msg.at != nil {
			// The store was named by whoever asked, which is what a search does:
			// its results are that store's ids and there is no log line behind
			// them to resolve one from.
			m.traceBack = m.state
			m.asked = traceAsk{endpoint: *msg.at, id: msg.id}
			m.state = stateTrace
			if msg.tree != nil {
				// A Jaeger search answers with the traces themselves, so the
				// list on screen was built out of the very bytes the gantt
				// needs. Asking for them again would be the same document.
				m.traces.put(msg.at.URL, msg.id, msg.tree)
			}
			if t, ok := m.traces.get(msg.at.URL, msg.id); ok {
				m.trace = newTrace(t)
				m.trace.resize(m.w, m.h)
				return m, nil
			}
			m.trace = loadingTrace(msg.id)
			m.trace.resize(m.w, m.h)
			return m, fetchTrace(*msg.at, msg.id)
		}
		endpoint, ok := m.logs.cfg.TraceEndpoint(msg.from)
		if !ok {
			// Said where the reader is, rather than by opening a screen with
			// nothing on it: this is something the config does not have, not a
			// trace that could not be read. Set rather than sent, since a
			// command is for work that happens off the loop and this is
			// already known.
			m.say("no trace store here: give the place a traces: url")
			return m, nil
		}
		m.traceBack = m.state
		m.asked = traceAsk{endpoint: endpoint, id: msg.id}
		m.state = stateTrace

		// The walk between a request and the lines it explains goes both ways and
		// usually more than once, and the trace at the end of it is the one that
		// was just read. Asking for it again would be the same bytes, decoded
		// again, off a screen that already drew them.
		if t, ok := m.traces.get(endpoint.URL, msg.id); ok {
			m.trace = newTrace(t)
			m.trace.resize(m.w, m.h)
			return m, nil
		}
		m.trace = loadingTrace(msg.id)
		m.trace.resize(m.w, m.h)
		return m, fetchTrace(endpoint, msg.id)

	case traceLoadedMsg:
		m.traces.put(m.asked.endpoint.URL, msg.tree.ID, msg.tree)
		m.trace = newTrace(msg.tree)
		m.trace.resize(m.w, m.h)
		return m, nil

	case reloadTraceMsg:
		// A trace still being written gains spans after it was first read, so what
		// the cache holds has to be droppable. A trace that came from a file has
		// nowhere to ask.
		if m.asked.id == "" {
			m.trace.note = "nothing to reload: this trace did not come from a trace store"
			return m, nil
		}
		m.traces.drop(m.asked.endpoint.URL, m.asked.id)
		m.trace = loadingTrace(m.asked.id)
		m.trace.resize(m.w, m.h)
		return m, fetchTrace(m.asked.endpoint, m.asked.id)

	case traceErrMsg:
		m.trace.err = msg.err
		return m, nil

	case openHelpMsg:
		m.help = newHelp(m.w, m.h)
		m.back = m.state
		m.state = stateHelp
		return m, nil

	case backMsg:
		switch m.state {
		case stateHelp:
			m.state = m.back
		case stateEntry:
			m.state = stateLogs
		case stateTrace:
			if m.traceBack == stateTrace {
				// Started on this trace: there is nothing underneath.
				return m, m.quit()
			}
			m.state = m.traceBack
		case stateSearch:
			if m.searchBack == stateSearch {
				return m, m.quit()
			}
			m.state = m.searchBack
		case stateLogs:
			m.stopStream()
			m.state = stateStart
		}
		return m, nil

	case quitMsg:
		return m, m.quit()
	}

	var cmd tea.Cmd
	switch m.state {
	case stateStart:
		m.start, cmd = m.start.Update(msg)
	case stateLogs:
		m.logs, cmd = m.logs.Update(msg)
	case stateEntry:
		m.entry, cmd = m.entry.Update(msg)
	case stateHelp:
		m.help, cmd = m.help.Update(msg)
	case stateTrace:
		m.trace, cmd = m.trace.Update(msg)
	case stateSearch:
		m.search, cmd = m.search.Update(msg)
	}
	return m, cmd
}

// View implements [tea.Model].
func (m Model) View() string {
	switch m.state {
	case stateLogs:
		return m.logs.View()
	case stateEntry:
		return m.entry.View()
	case stateHelp:
		return m.help.View()
	case stateTrace:
		return m.trace.View()
	case stateSearch:
		return m.search.View()
	default:
		return m.start.View()
	}
}

func (m *Model) stopStream() {
	if m.stream != nil {
		m.stream.Close()
		m.stream = nil
	}
}

func (m *Model) quit() tea.Cmd {
	m.stopStream()
	return tea.Quit
}

// Messages exchanged between the views and the stream.
// say puts a remark on the screen that is being read.
//
// A note is the evidence that a key was pressed, so it has to land where the
// reader is. The keys that produce one are on more than one screen — T opens a
// trace from the list and from an entry both, and the trace screen is reached
// from either — and a note left on the list while an entry is open is read by
// nobody and cleared by the next key.
func (m *Model) say(text string) {
	switch m.state {
	case stateEntry:
		m.entry.note = text
	case stateTrace:
		m.trace.note = text
	case stateSearch:
		m.search.note = text
	default:
		m.logs.note = text
	}
}

type (
	streamStartedMsg struct{ stream *source.Stream }
	streamErrMsg     struct{ err error }
	streamDoneMsg    struct{ err error }
	linesMsg         struct {
		lines  []source.Line
		closed bool
	}
	// requeryMsg asks the sources the filter again, for the part of it they can
	// answer themselves.
	requeryMsg struct {
		cfg   source.Config
		query string
	}
	// filterMsg narrows the list by what an entry had under the cursor.
	filterMsg struct{ term query.Expr }

	initMsg      struct{}
	openEntryMsg struct{ entry *logs.Entry }
	// openTraceMsg asks for the trace a line belongs to. from is the merge tag,
	// which is what says whose trace store to ask.
	openTraceMsg struct {
		id, from string
		// at names the store instead, for a reader who is not standing in a log
		// list: a search result is an id belonging to the store that answered
		// and there is no line behind it to resolve one from.
		at *source.Endpoint
		// tree is the trace itself, when whoever asked already holds it. A
		// Jaeger search answers with the traces rather than with a summary of
		// them, so the row that was picked was drawn from the whole thing.
		tree *trace.Tree
	}
	// openSearchMsg opens the trace search over one store.
	openSearchMsg struct{ at source.Endpoint }
	// searchLoadedMsg and searchErrMsg answer it. trees is what the store sent
	// when it sent the traces themselves, keyed by id; it is empty for a store
	// that answered with a summary.
	searchLoadedMsg struct {
		results []trace.Result
		trees   map[string]*trace.Tree
	}
	searchErrMsg struct{ err error }
	// searchNamesMsg is what the store says it holds, for one field of the
	// form. of is what the list hangs off where it is not a list of everything:
	// the service whose operations these are, when the store indexes them that
	// way, or the tag whose values they are.
	searchNamesMsg struct {
		field searchField
		of    string
		names []string
	}
	// traceLoadedMsg and traceErrMsg answer it. A trace is a request over the
	// network, so the screen is opened first and filled in when it lands.
	traceLoadedMsg struct{ tree *trace.Tree }
	traceErrMsg    struct{ err error }
	// reloadTraceMsg asks for the trace on screen again, past whatever was
	// remembered of it.
	reloadTraceMsg struct{}
	// openHelpMsg opens the filter reference over whatever asked for it.
	openHelpMsg struct{}
	backMsg     struct{}
	quitMsg     struct{}
)

// saveHistory records what was opened. A failure to write is not worth
// interrupting the view for; the next run simply offers less.
func saveHistory(h config.History) tea.Cmd {
	return func() tea.Msg {
		_ = h.Save()
		return nil
	}
}

// traceTimeout bounds a fetch. A trace has an end, unlike the streams the rest
// of telescope opens, so waiting forever for one is only ever a hang.
const traceTimeout = 30 * time.Second

// fetchTrace asks an endpoint for a trace, off the update loop.
func fetchTrace(endpoint source.Endpoint, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), traceTimeout)
		defer cancel()

		data, err := endpoint.Trace(ctx, id)
		if err != nil {
			return traceErrMsg{err: err}
		}
		// Read as whichever format came back rather than as the one the store
		// declared: the declaration says what to ask, and a proxy or a gateway
		// in front of it is free to answer in the other encoding.
		tree, err := trace.Decode(data)
		if err != nil {
			return traceErrMsg{err: err}
		}
		if tree.Len() == 0 {
			return traceErrMsg{err: errors.Errorf("trace %s has no spans", id)}
		}
		return traceLoadedMsg{tree: tree}
	}
}

// searchTraces asks a store which traces match, off the update loop.
//
// Which decoder reads the answer follows from what the store said it is, since
// the two APIs answer with different documents: Tempo with a summary of each
// trace, Jaeger with the traces themselves. That is why this is not
// [trace.Decode]'s "read whatever came out" — both answers are valid JSON to
// the other's decoder and one of them would quietly come out empty.
func searchTraces(at source.Endpoint, q source.TraceQuery) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
		defer cancel()

		data, err := at.SearchTraces(ctx, q)
		if err != nil {
			return searchErrMsg{err: err}
		}
		if at.Collector == source.CollectorJaeger {
			found, err := trace.DecodeJaegerSearch(data)
			if err != nil {
				return searchErrMsg{err: err}
			}
			msg := searchLoadedMsg{trees: make(map[string]*trace.Tree, len(found))}
			for _, t := range found {
				r := trace.Summary(t)
				msg.results = append(msg.results, r)
				msg.trees[r.TraceID] = t
			}
			trace.SortResults(msg.results)
			return msg
		}
		results, err := trace.DecodeSearch(data)
		if err != nil {
			return searchErrMsg{err: err}
		}
		trace.SortResults(results)
		return searchLoadedMsg{results: results}
	}
}

// listTraceNames asks a store what it holds, for one field of the search form.
//
// A store that will not say costs the suggestions and never the search, so a
// failure here answers with nothing rather than with an error: the field is
// typed into either way, and a role that may search but not list is ordinary.
func listTraceNames(at source.Endpoint, f searchField, of string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		var (
			names []string
			err   error
		)
		switch {
		case f == fieldService:
			names, err = at.TraceServices(ctx)
		case f == fieldTags && of == "":
			names, err = at.TraceTagKeys(ctx)
		case f == fieldTags:
			names, err = at.TraceTagValues(ctx, of)
		default:
			names, err = at.TraceOperations(ctx, of)
		}
		if err != nil {
			names = nil
		}
		// Answered even when empty, so the form remembers it asked and does not
		// ask again on every keystroke.
		if names == nil {
			names = []string{}
		}
		return searchNamesMsg{field: f, of: of, names: names}
	}
}

func startStream(cfg source.Config) tea.Cmd {
	return func() tea.Msg {
		// A merge orders lines by time, and for a source that does not report
		// one, the time is inside the line: reading it is the parser's job.
		s, err := source.Start(context.Background(), cfg, source.WithTimeFunc(logs.LineTime))
		if err != nil {
			return streamErrMsg{err: err}
		}
		return streamStartedMsg{stream: s}
	}
}

// readLines blocks for one line, then drains whatever else is already buffered
// so bursts cost a single render.
func readLines(s *source.Stream) tea.Cmd {
	return func() tea.Msg {
		if s == nil {
			return nil
		}
		l, ok := <-s.Lines()
		if !ok {
			return linesMsg{closed: true}
		}
		batch := []source.Line{l}
		for len(batch) < readBatch {
			select {
			case l, ok := <-s.Lines():
				if !ok {
					return linesMsg{lines: batch, closed: true}
				}
				batch = append(batch, l)
			default:
				return linesMsg{lines: batch}
			}
		}
		return linesMsg{lines: batch}
	}
}

func waitDone(s *source.Stream) tea.Cmd {
	return func() tea.Msg {
		return streamDoneMsg{err: <-s.Done()}
	}
}
