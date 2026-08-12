package logs

import (
	"slices"
	"strings"
)

// What the index will hold of a stream that never ends. A log stream is not
// bounded and neither is what it says: these are what makes offering it back
// cost a fixed amount of memory rather than a growing one.
const (
	maxIndexKeys     = 512
	maxIndexValues   = 64
	maxIndexValueLen = 96
)

// fieldIndex remembers what the lines were labeled with, so the filter prompt
// can offer it back.
//
// It is built as lines arrive rather than scanned for when asked: the store
// holds two hundred thousand entries and the prompt is redrawn on every
// keystroke. Nothing is ever forgotten, not even when the entry that taught it
// is evicted — a name that was true of this stream stays worth completing, and
// dropping it would make the suggestions flicker as the cap bites.
type fieldIndex struct {
	// order is the keys as they were first seen, which is stable and therefore
	// something a test can assert on. What order they are offered in is the
	// prompt's business.
	order  []string
	values map[string][]string
}

// observe records one key, and the value under it when that value is worth
// keeping. A key is remembered whether or not its values are.
func (ix *fieldIndex) observe(key, value string, keepValue bool) {
	if key == "" {
		return
	}
	if ix.values == nil {
		ix.values = make(map[string][]string)
	}
	values, known := ix.values[key]
	if !known {
		if len(ix.order) >= maxIndexKeys {
			return
		}
		ix.order = append(ix.order, key)
		ix.values[key] = nil
	}
	if !keepValue || value == "" || len(value) > maxIndexValueLen ||
		len(values) >= maxIndexValues || slices.Contains(values, value) {
		return
	}
	ix.values[key] = append(values, value)
}

// index records what one entry is worth completing by.
//
// The values of a key whose kind is a time or a trace are not kept: they are a
// different string on every line, so they would fill the index and complete
// nothing. The key itself still is — "trace_id=" is a term worth starting.
func (ix *fieldIndex) index(e *Entry) {
	for _, f := range e.Record.Fields {
		ix.observe(f.Key, f.String(), completable(f.Key))
	}
	for _, l := range e.Labels {
		ix.observe(l.Key, l.Value, completable(l.Key))
	}
	// The names a record is read under whatever the shipper called them, offered
	// only where this stream has something under them.
	if e.Record.HasLevel {
		ix.observe("level", e.Record.Level.String(), true)
	}
	if e.Record.Body != "" {
		ix.observe("msg", "", false)
	}
	if e.Record.TraceID != "" {
		ix.observe("trace_id", "", false)
	}
	if e.Record.SpanID != "" {
		ix.observe("span_id", "", false)
	}
	if e.Source != "" {
		ix.observe("source", e.Source, true)
	}
	ix.observe("stream", streamName(e.Stderr), true)
}

func streamName(stderr bool) string {
	if stderr {
		return "stderr"
	}
	return "stdout"
}

// completable reports whether a key's values are few enough to be worth
// remembering.
func completable(key string) bool {
	switch KindOf(key) {
	case KindTrace, KindTime:
		return false
	default:
		return !matches(key, bodyKeys)
	}
}

// FieldNames are the names the lines so far carried, sorted, which is what the
// filter prompt completes a bare word into.
func (s *Store) FieldNames() []string {
	out := slices.Clone(s.index.order)
	slices.SortFunc(out, compareFold)
	return out
}

// FieldValues are the values seen under key, sorted. It is empty for a key
// nothing was seen under, and for one whose values were never worth keeping;
// see [fieldIndex].
func (s *Store) FieldValues(key string) []string {
	out := slices.Clone(s.index.values[key])
	slices.SortFunc(out, compareFold)
	return out
}

// compareFold orders suggestions the way a reader scans them, which is by the
// letters and not by their case.
func compareFold(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}
