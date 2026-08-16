package logs

import (
	"slices"
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
	// Kind says the line is telescope reporting on the source rather than
	// anything the source wrote, and which report it is, which is why the list
	// marks it.
	Kind source.Kind
	// Source names which stream the line came from, for a merge of several.
	Source string
	// Labels are what the source reported beside the line.
	Labels []source.Label
	At     time.Time // Record time, falling back to arrival time
	// HasTime says At is when the line was written rather than when it turned
	// up here, which is the difference between a time worth showing and one
	// that only says the view is running.
	HasTime bool
	// Band alternates with the second an entry belongs to, so a view can shade
	// the lines that happened together and leave a seam where time passed.
	Band   bool
	Record Record

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

	// band and bandAt alternate the shading of each second, worked out as
	// entries arrive: which second a line belongs to is settled once, and a
	// view that scrolled or filtered could not work it out again.
	band   bool
	bandAt time.Time

	// index is what the prompt completes by, built here for the same reason the
	// band is: on arrival, once.
	index fieldIndex
}

// NewStore returns a store retaining at most limit entries.
func NewStore(limit int) *Store {
	return &Store{
		max: limit,
		// The time is left out of the rendering and drawn by the view instead.
		// A rendering is worked out once, when the line arrives, and how a time
		// is written is something the reader changes while looking at it.
		fmt: &pl.Formatter{Color: true, NoTime: true},
	}
}

// Append renders and stores a line.
func (s *Store) Append(l source.Line) *Entry {
	e := s.render(l)
	if e == nil {
		return nil
	}
	if sec := e.At.Truncate(time.Second); !sec.Equal(s.bandAt) {
		s.band, s.bandAt = !s.band, sec
	}
	e.Band = s.band
	e.Seq = s.seq
	s.seq++
	s.index.index(e)

	s.entries = append(s.entries, e)
	if len(s.entries) > s.max {
		n := len(s.entries) - s.max
		s.entries = append(s.entries[:0], s.entries[n:]...)
		s.dropped += n
	}
	return e
}

// Prepend stores a page of lines older than everything held, which is what a
// database answers when the view asks for what came before its first line. They
// arrive oldest first, as a stream's do.
//
// It is not an append run backwards. A page is bounded by [Store.Room] rather
// than by eviction — dropping the newest lines to make room for older ones would
// undo the reading that asked for them — and the shading is worked out from the
// entry the page joins, so the seam between the page and what was already held
// alternates like every other second boundary.
func (s *Store) Prepend(lines []source.Line) []*Entry {
	room := s.Room()
	if room <= 0 || len(lines) == 0 {
		return nil
	}
	// The newest of the page is what joins what is held, so a page too large to
	// fit loses its far end and not its near one.
	if len(lines) > room {
		lines = lines[len(lines)-room:]
	}

	page := make([]*Entry, 0, len(lines))
	for _, l := range lines {
		if e := s.render(l); e != nil {
			page = append(page, e)
		}
	}
	if len(page) == 0 {
		return nil
	}
	for _, e := range page {
		e.Seq = s.seq
		s.seq++
		s.index.index(e)
	}

	switch {
	case len(s.entries) == 0:
		for _, e := range page {
			if sec := e.At.Truncate(time.Second); !sec.Equal(s.bandAt) {
				s.band, s.bandAt = !s.band, sec
			}
			e.Band = s.band
		}
	default:
		// Backwards from the entry the page runs into, so that page and store
		// agree about the second they share.
		first := s.entries[0]
		band, at := first.Band, first.At.Truncate(time.Second)
		for _, e := range slices.Backward(page) {
			if sec := e.At.Truncate(time.Second); !sec.Equal(at) {
				band, at = !band, sec
			}
			e.Band = band
		}
	}

	s.entries = append(page, s.entries...)
	return page
}

// render turns a line into an entry, without deciding where in the store it
// belongs: what a line says is its own, and its sequence, shading and place in
// the list are the store's.
func (s *Store) render(l source.Line) *Entry {
	if l.Kind.IsNote() {
		return s.note(l)
	}
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
	// A structured line pl colored itself still says nothing about what its
	// fields mean, which is where the status codes and the pod names are.
	switch {
	case rec.Structured:
		text = highlightRecord(text, rec.Fields)
	case text == string(l.Data):
		text = Highlight(text)
	}
	// Whatever produced the rendering, part of it came from somebody else's
	// bytes, and a cursor movement inside a list is not a rendering.
	text = Sanitize(text)

	head, extra := fold(text)

	e := &Entry{
		Raw:       l.Data,
		Text:      text,
		Head:      head,
		Extra:     extra,
		Stderr:    l.Stderr,
		Kind:      l.Kind,
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
	return e
}

// note turns telescope's own words into an entry. Nothing here is parsed or
// formatted: a note has no structure to find, and what it says is written from
// its kind rather than read out of a line.
func (s *Store) note(l source.Line) *Entry {
	// The reason is an error as somebody else's tool reported it, so it reaches
	// the screen the way every other line from outside does.
	text := Sanitize(noteText(l))
	head, extra := fold(text)
	e := &Entry{
		Raw:    []byte(text),
		Text:   text,
		Head:   head,
		Extra:  extra,
		Stderr: l.Stderr,
		Kind:   l.Kind,
		Source: l.Source,
		At:     l.At,
		Record: Record{Body: text},
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	return e
}

// fold splits a rendering into the row the list shows and how much of it is
// folded away until the entry is opened.
func fold(text string) (head string, extra int) {
	head, rest, multiline := strings.Cut(text, "\n")
	if multiline {
		extra = strings.Count(rest, "\n") + 1
		// Splitting can cut a color sequence off from its reset.
		head += ansiReset
	}
	return head, extra
}

// Entries returns the retained entries, oldest first.
func (s *Store) Entries() []*Entry { return s.entries }

// Len returns the number of retained entries.
func (s *Store) Len() int { return len(s.entries) }

// Room is how many more entries fit under the cap. A page is worth asking a
// database for only while there is somewhere to put it: reading further back is
// what the cap costs once it is reached, and a page that evicted the newest
// lines to land would be reading in a circle.
func (s *Store) Room() int { return max(s.max-len(s.entries), 0) }

// Dropped returns how many entries were evicted by the cap.
func (s *Store) Dropped() int { return s.dropped }

// Reset drops every entry.
func (s *Store) Reset() {
	s.entries = nil
	s.seq = 0
	s.dropped = 0
	s.band, s.bandAt = false, time.Time{}
	s.index = fieldIndex{}
}
