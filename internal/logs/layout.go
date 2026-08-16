package logs

import (
	"cmp"
	"slices"
	"strings"

	"github.com/oteldb/telescope/internal/source"
)

// RowField is one attribute picked to stand beside the message in the list.
//
// A well-instrumented service writes the same sentence on every line and puts
// what happened in its attributes: three hundred lines reading "got http
// request" are three hundred different requests, and the method, the route, the
// status and how long it took are all there, indexed, and nowhere on screen.
// These are what the row shows instead.
type RowField struct {
	Key   string
	Value string
	// Named says the key has to be written out beside the value. A value whose
	// key means nothing here is unreadable alone — "42" could be anything — and
	// one that is recognized reads better without it, since a method looks like
	// a method and a route like a route wherever they are written.
	Named bool
}

// rowFields are the attributes of one line worth a column, in the order a
// reader scans them.
//
// Which of them are worth showing on the day is not settled here: see
// [Store.Row]. This is what a line has to offer and what order it offers it in,
// which is a property of the line and can be worked out once.
func rowFields(rec Record, labels []source.Label) []RowField {
	out := make([]RowField, 0, len(rec.Fields)+len(labels))
	add := func(key, value string) {
		if !worthARow(key, value) {
			return
		}
		// A database answers with the label and the line repeats it in the
		// record, spelled either way; twice on a row is once too many.
		norm := normalizeKey(key)
		if slices.ContainsFunc(out, func(f RowField) bool { return normalizeKey(f.Key) == norm }) {
			return
		}
		out = append(out, RowField{Key: key, Value: value, Named: semanticOf(key) == semNone})
	}
	// What the line said about itself first, then what the source said about
	// the line, which is the order the rest of the reading resolves a name in.
	for _, f := range rec.Fields {
		add(f.Key, f.String())
	}
	for _, l := range labels {
		add(l.Key, l.Value)
	}
	slices.SortStableFunc(out, func(a, b RowField) int {
		return cmp.Compare(semanticOf(a.Key).rank(), semanticOf(b.Key).rank())
	})
	return out
}

// Row is what of e the list should actually show, which is the fields that have
// said more than one thing on this source's lines.
//
// It is asked at draw time and not answered when the line arrived: a key that
// reads the same across the first fifty lines is a key the fifty-first can
// disagree with, and the row that was drawn before it did has to change its
// mind with the rest of them.
func (s *Store) Row(e *Entry) []RowField {
	if len(e.Row) == 0 {
		return nil
	}
	out := make([]RowField, 0, len(e.Row))
	for _, f := range e.Row {
		if s.vary.varies(e.Source, f.Key) {
			out = append(out, f)
		}
	}
	return out
}

// message is a structured line as the row says it: what the line meant to say,
// and nothing it was labeled with. pl bolds a message and so does this, since
// it is the one part of a row that is a sentence.
func message(rec Record) string {
	if rec.Body == "" {
		return ""
	}
	return ansiBold + Escape(rec.Body) + ansiReset
}

// worthARow rejects what a row is better without, whatever its value turns out
// to be.
func worthARow(key, value string) bool {
	if key == "" || value == "" {
		return false
	}
	// A stacktrace or a wrapped error is a block and not a column. pl renders
	// those under the message, where there is room for them.
	if strings.ContainsAny(value, "\n\r") {
		return false
	}
	if !semanticOf(key).onRow() {
		return false
	}
	switch KindOf(key) {
	case KindTime, KindLevel:
		// Both are drawn in the gutter for every line, in a column that lines
		// up down the screen rather than wherever the attributes reached.
		return false
	case KindTrace:
		// A trace id is a different string on every line, so a screenful of
		// them sorts nothing and distinguishes nothing. The gutter marks that
		// there is one, and one key opens it.
		return false
	}
	return !matches(key, bodyKeys)
}

// onRow reports whether a key belongs beside a message at all.
func (s semantic) onRow() bool { return s != semResource && s != semCode }

// rank is where a field sits on the row. It follows what a reader is looking
// for and not what the field is called: the verb and the route say what the
// line is about, the status says how it went, and the timing says whether to
// care. Anything unrecognized keeps the order it arrived in, behind all of it.
func (s semantic) rank() int {
	switch s {
	case semHTTPMethod:
		return 0
	case semHTTPRoute:
		return 1
	case semHTTPStatus:
		return 2
	case semDuration:
		return 3
	case semRPCStatus:
		return 4
	case semRPCName:
		return 5
	case semDestination:
		return 6
	case semNamespace, semPod, semContainer, semNode, semNumber:
		return 7
	default:
		return 8
	}
}

// Render draws a field as the row shows it: the value colored by what its key
// says it is, and the key in front of it only where the value would not be
// readable without one.
func (f RowField) Render() string {
	// A value out of a log database is bytes somebody else chose, and one that
	// had to be escaped is shown as the escaping wrote it: the escapes are the
	// thing worth seeing, colored as a duration or not.
	value := Escape(f.Value)
	if value == f.Value {
		if colored, ok := HighlightField(f.Key, f.Value); ok {
			value = colored
		}
	}
	if !f.Named {
		return value
	}
	return ansiKey + Escape(f.Key) + ansiReset + "=" + value
}
