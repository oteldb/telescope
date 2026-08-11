package logs

import (
	"strings"
	"time"

	"github.com/go-faster/pl"

	"github.com/oteldb/telescope/internal/source"
)

// Entry is one received line: the bytes, its parsed record and its rendering.
type Entry struct {
	Seq int
	Raw []byte
	// Text is the full rendering, which spans several lines when the record
	// carries a stacktrace or a multi-line error.
	Text string
	// Head is the first line of Text and Extra counts the rest. The log list
	// shows one row per entry, so the remainder is folded away until the entry
	// is opened.
	Head   string
	Extra  int
	Stderr bool
	// Source names which stream the line came from, for a merge of several.
	Source string
	// Labels are what the source reported beside the line.
	Labels []source.Label
	At     time.Time // Record time, falling back to arrival time
	// HasTime says At is when the line was written rather than when it turned
	// up here, which is the difference between a time worth showing and one
	// that only says the view is running.
	HasTime bool
	Record  Record

	// labelText is the label set as one string, so a filter can match it.
	labelText string
}

// LineTime is when a line was written, as far as anything here can tell: what
// the source reported, else what the line itself says. It is what a merge of
// several sources orders by.
func LineTime(l source.Line) time.Time {
	if !l.At.IsZero() {
		return l.At
	}
	return Parse(l.Data).Time
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
	// What the line does not say about itself, the source may have said for
	// it: a Loki entry is often a bare message with the severity in a label.
	if !rec.HasLevel {
		if lvl, ok := levelFromLabels(l.Labels); ok {
			rec.Level, rec.HasLevel = lvl, true
		}
	}
	if rec.TraceID == "" {
		rec.TraceID = labelValue(l.Labels, traceKeys)
	}
	if rec.SpanID == "" {
		rec.SpanID = labelValue(l.Labels, spanKeys)
	}

	text, ok := s.fmt.Format(l.Data)
	if !ok {
		return nil
	}
	// pl passes unstructured lines through verbatim; those are ours to color.
	if !rec.Structured && text == string(l.Data) {
		text = Highlight(text)
	}
	// Whatever produced the rendering, part of it came from somebody else's
	// bytes, and a cursor movement inside a list is not a rendering.
	text = Sanitize(text)

	head, rest, multiline := strings.Cut(text, "\n")
	extra := 0
	if multiline {
		extra = strings.Count(rest, "\n") + 1
		// Splitting can cut a color sequence off from its reset.
		head += ansiReset
	}

	e := &Entry{
		Seq:       s.seq,
		Raw:       l.Data,
		Text:      text,
		Head:      head,
		Extra:     extra,
		Stderr:    l.Stderr,
		Source:    l.Source,
		Labels:    l.Labels,
		At:        rec.Time,
		HasTime:   !rec.Time.IsZero() || !l.At.IsZero(),
		Record:    rec,
		labelText: labelText(l.Labels),
	}
	// A source that reports the time out of band, such as a log database,
	// knows better than the arrival time; a time inside the line still wins,
	// since that is when the application says it happened.
	if e.At.IsZero() {
		e.At = l.At
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
