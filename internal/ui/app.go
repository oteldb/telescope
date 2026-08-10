// Package ui implements the telescope terminal interface.
package ui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// storeLimit is how many lines are retained in memory.
const storeLimit = 200_000

// readBatch bounds how many lines are folded into a single update.
const readBatch = 2048

var errEmptyHost = errors.New("ssh transport requires a host")

type state int

const (
	stateStart state = iota
	stateLogs
	stateEntry
)

// Model is the root bubbletea model.
type Model struct {
	state state
	w, h  int

	start startModel
	logs  logModel
	entry entryModel

	stream *source.Stream
}

// New returns the root model, opening on the start screen.
func New() Model {
	return Model{start: newStart()}
}

// Init implements [tea.Model].
//
// The ssh host list is preloaded even though the first step opens on the local
// transport, so switching to ssh never waits. The request is built inline
// rather than through the start model because Init cannot record bookkeeping
// on its value receiver; the reply is cached by key when it lands.
func (m Model) Init() tea.Cmd {
	req := complete.Request{Field: complete.FieldHost}
	return tea.Batch(textinputBlink, func() tea.Msg {
		items, err := fetcher(context.Background(), req)
		return candidatesMsg{key: req.Key(), items: items, err: err}
	})
}

// Update implements [tea.Model].
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.start, _ = m.start.Update(msg)
		m.logs.resize(msg.Width, msg.Height)
		m.entry.resize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, m.quit()
		}

	case connectMsg:
		m.logs = newLogs(msg.cfg, logs.NewStore(storeLimit), msg.query)
		m.logs.resize(m.w, m.h)
		m.state = stateLogs
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
			m.logs.store.Append(l)
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
		m.entry = newEntry(msg.entry)
		m.entry.resize(m.w, m.h)
		m.state = stateEntry
		return m, nil

	case backMsg:
		switch m.state {
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
	openEntryMsg struct{ entry *logs.Entry }
	backMsg      struct{}
	quitMsg      struct{}
)

func startStream(cfg source.Config) tea.Cmd {
	return func() tea.Msg {
		s, err := source.Start(context.Background(), cfg)
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
