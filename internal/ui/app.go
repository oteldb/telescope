// Package ui implements the telescope terminal interface.
package ui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/source"
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

	stream *source.Stream
}

// New returns the root model, opening on the start screen.
func New() Model {
	return Model{start: newStart()}
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

	case fieldsMsg:
		m.logs.takeFields(msg)
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
