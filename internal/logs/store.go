package logs

import (
	"time"

	"github.com/go-faster/pl"

	"github.com/oteldb/telescope/internal/source"
)

// Entry is one received line: the bytes, its parsed record and its rendering.
type Entry struct {
	Seq    int
	Raw    []byte
	Text   string // rendered with ANSI colors
	Stderr bool
	At     time.Time // Record time, falling back to arrival time
	Record Record
}

// Store keeps the received lines, capped at a maximum, and renders them once
// on arrival.
type Store struct {
	max     int
	entries []*Entry
	seq     int
	fmt     *pl.Formatter

	// dropped counts entries evicted by the cap.
	dropped int
}

// NewStore returns a store retaining at most max entries.
func NewStore(max int) *Store {
	return &Store{
		max: max,
		fmt: &pl.Formatter{Color: true},
	}
}

// Append renders and stores a line.
func (s *Store) Append(l source.Line) *Entry {
	rec := Parse(l.Data)

	text, ok := s.fmt.Format(l.Data)
	if !ok {
		return nil
	}
	// pl passes unstructured lines through verbatim; those are ours to color.
	if !rec.Structured && text == string(l.Data) {
		text = Highlight(text)
	}

	e := &Entry{
		Seq:    s.seq,
		Raw:    l.Data,
		Text:   text,
		Stderr: l.Stderr,
		At:     rec.Time,
		Record: rec,
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	s.seq++

	s.entries = append(s.entries, e)
	if len(s.entries) > s.max {
		n := len(s.entries) - s.max
		s.entries = append(s.entries[:0], s.entries[n:]...)
		s.dropped += n
	}
	return e
}

// Entries returns the retained entries, oldest first.
func (s *Store) Entries() []*Entry { return s.entries }

// Len returns the number of retained entries.
func (s *Store) Len() int { return len(s.entries) }

// Dropped returns how many entries were evicted by the cap.
func (s *Store) Dropped() int { return s.dropped }

// Reset drops every entry.
func (s *Store) Reset() {
	s.entries = nil
	s.seq = 0
	s.dropped = 0
}
