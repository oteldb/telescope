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
)

// Model is the root bubbletea model.
type Model struct {
	state state
	w, h  int
	// back is the screen the help was opened over, since it can be read from the
	// list or from the prompt and either is where it should return to.
	back state

	start startModel
	logs  logModel
	entry entryModel
	help  helpModel
	trace traceModel
	// traceBack is where leaving the trace returns to. A trace telescope was
	// started on has nothing under it, and a start screen that cannot reopen it
	// would be a trapdoor rather than a way back — so that one quits.
	traceBack state

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
		// A note belongs to the screen that asked for it, and the trace screen
		// is reached from two of them.
		switch m.state {
		case stateLogs:
			m.logs, _ = m.logs.Update(msg)
		case stateEntry:
			m.entry, _ = m.entry.Update(msg)
		case stateTrace:
			m.trace, _ = m.trace.Update(msg)
		}
		return m, nil

	case openTraceMsg:
		endpoint, ok := m.logs.cfg.TraceEndpoint(msg.from)
		if !ok {
			// Said where the reader is, rather than by opening a screen with
			// nothing on it: this is something the config does not have, not a
			// trace that could not be read. Set rather than sent, since a
			// command is for work that happens off the loop and this is
			// already known.
			m.logs.note = "no trace store here: give the place a traces: url"
			return m, nil
		}
		m.traceBack = m.state
		m.trace = loadingTrace(msg.id)
		m.trace.resize(m.w, m.h)
		m.state = stateTrace
		return m, fetchTrace(endpoint, msg.id)

	case traceLoadedMsg:
		m.trace = newTrace(msg.tree)
		m.trace.resize(m.w, m.h)
		return m, nil

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
	openTraceMsg struct{ id, from string }
	// traceLoadedMsg and traceErrMsg answer it. A trace is a request over the
	// network, so the screen is opened first and filled in when it lands.
	traceLoadedMsg struct{ tree *trace.Tree }
	traceErrMsg    struct{ err error }
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
		found, err := trace.DecodeOTLP(data)
		if err != nil {
			return traceErrMsg{err: err}
		}
		for _, t := range found {
			if t.Len() > 0 {
				return traceLoadedMsg{tree: t}
			}
		}
		return traceErrMsg{err: errors.Errorf("trace %s has no spans", id)}
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
