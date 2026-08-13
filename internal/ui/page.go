package ui

import (
	"context"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// pageSize is how many lines one page asks for where the place named no tail of
// its own. It is a screenful many times over: a page is a round trip, and the
// reading that asked for it is scrolling.
const pageSize = 1000

// pageMsg carries what a database answered about what came before the oldest
// line held.
type pageMsg struct {
	// cfg identifies the stream that was asked, so a page arriving after the
	// view moved on is dropped rather than prepended to somebody else's lines.
	cfg   string
	lines []source.Line
	err   error
}

// wantPage asks for the lines before the oldest one held, once the reader has
// reached it.
//
// Reaching the top is the whole of the trigger: a tail is how much to read
// before anyone is looking, and paging is what makes that number a starting
// point rather than a limit. What cannot be asked twice is not paged at all —
// see [source.Config.CanPage].
func (m *logModel) wantPage() tea.Cmd {
	if m.cursor > 0 || m.top > 0 {
		// Away from the top, a failed page is worth trying again: the reader
		// will have to come back here to want one.
		m.pageErr = nil
		return nil
	}
	if m.paging || m.atStart || m.pageErr != nil || !m.cfg.CanPage() {
		return nil
	}
	held := m.store.Entries()
	if len(held) == 0 {
		return nil
	}
	// An arrival time says when the view was running, not where in the database
	// to read from, and asking by one would read the wrong lines back.
	first := held[0]
	if !first.HasTime {
		return nil
	}
	room := m.store.Room()
	if room <= 0 {
		m.atCap = true
		return nil
	}
	limit := pageSize
	if m.cfg.Tail > 0 {
		limit = m.cfg.Tail
	}
	m.paging = true
	return pageCmd(m.cfg, first.At, min(limit, room))
}

func pageCmd(cfg source.Config, before time.Time, limit int) tea.Cmd {
	return func() tea.Msg {
		lines, err := cfg.Page(context.Background(), before, limit)
		return pageMsg{cfg: cfg.Title(), lines: lines, err: err}
	}
}

// takePage folds a page in under the lines already held, leaving the reader on
// the entry they were reading.
//
// The cursor is carried by the entry it is on and not by its index: a page
// arrives in front of everything, so every index it had is one line further
// down than it was.
func (m *logModel) takePage(msg pageMsg) {
	if msg.cfg != m.cfg.Title() {
		return
	}
	m.paging = false
	switch {
	case msg.err != nil:
		m.pageErr = msg.err
		return
	case len(msg.lines) == 0:
		m.atStart = true
		return
	}
	m.pageErr = nil

	entries := m.view.Entries(m.store)
	var anchor *logs.Entry
	if m.cursor < len(entries) {
		anchor = entries[m.cursor]
	}

	page := m.store.Prepend(msg.lines)
	for _, e := range page {
		m.observe(e)
	}
	// A page shorter than what was asked for is the far end of the store's room
	// rather than the far end of the database.
	m.atCap = len(page) < len(msg.lines)

	entries = m.view.Entries(m.store)
	if anchor != nil {
		if i := slices.Index(entries, anchor); i >= 0 {
			m.top += i - m.cursor
			m.cursor = i
		}
	}
	m.clamp()
}

// olderText says what is at the top of the list, where the lines before the
// first one would be. It is silent while there is nothing to say, which is
// every stream that cannot be paged and every page that has not been asked for.
func (m logModel) olderText() string {
	switch {
	case !m.cfg.CanPage():
		return ""
	case m.pageErr != nil:
		return styleErr.Render("older: " + m.pageErr.Error())
	case m.paging:
		return "reading older"
	case m.atCap:
		return "holding all it can"
	case m.atStart:
		return "at the start"
	default:
		return ""
	}
}
