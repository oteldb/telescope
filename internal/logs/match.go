package logs

import (
	"strings"

	"github.com/go-faster/jx"

	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/query"
)

// Haystack is what a bare term of a query matches: what the line says, and what
// the source said beside it, since the list has no room to show the labels.
//
// For a structured line that is its values and not the JSON around them. A key
// is not something a line said — searching for "level" should not match every
// line that has one — and it is also the only reading a log database can answer
// for us: see [query.LogsQL], which searches field values and nothing else.
func (e *Entry) Haystack() [][]byte {
	if !e.Record.Structured {
		return [][]byte{e.Raw, []byte(e.labelText)}
	}
	out := make([][]byte, 0, len(e.Record.Fields)+len(e.Labels))
	for _, f := range e.Record.Fields {
		out = append(out, unquoted(f.Value))
	}
	for _, l := range e.Labels {
		out = append(out, []byte(l.Value))
	}
	return out
}

// unquoted is a JSON value as the bytes to search, without decoding it: a
// string keeps its escapes, since a term holding one would not have been
// pushed to a database either.
func unquoted(raw jx.Raw) []byte {
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return raw[1 : len(raw)-1]
	}
	return raw
}

// Level is the severity the line reported, if it reported one.
func (e *Entry) Level() (zapcore.Level, bool) { return e.Record.Level, e.Record.HasLevel }

// Field resolves a name a query compares against: what the line called it
// first, then what the source said about the line, then the few names a record
// is read under whatever it called them.
//
// A key is matched exactly, and failing that under the name a shipper would
// have kept it as: a field name is what it is, but service.name and
// service_name are one attribute written twice, and a database asked for either
// answers with the other. Values are compared without case because a pod name
// typed in a hurry is still that pod.
func (e *Entry) Field(key string) (string, bool) {
	if v, ok := e.named(key); ok {
		return v, true
	}
	// Failing that, the name the shipper kept. An OTLP attribute arrives as
	// service.name and is stored as service_name by most of what stores it —
	// including the databases here, which answer a query for the one with the
	// other. A filter that reaches the same lines a pushed-down query does is
	// the whole point; the exact spelling above still wins where both exist.
	if under := strings.ReplaceAll(key, ".", "_"); under != key {
		if v, ok := e.named(under); ok {
			return v, true
		}
	}
	switch {
	case matches(key, bodyKeys):
		return e.Record.Body, e.Record.Body != ""
	case matches(key, traceKeys):
		return e.Record.TraceID, e.Record.TraceID != ""
	case matches(key, spanKeys):
		return e.Record.SpanID, e.Record.SpanID != ""
	case strings.EqualFold(key, "source"):
		return e.Source, e.Source != ""
	case strings.EqualFold(key, "stream"):
		if e.Stderr {
			return "stderr", true
		}
		return "stdout", true
	}
	return "", false
}

// named is what the line or its source called key, exactly.
func (e *Entry) named(key string) (string, bool) {
	for _, f := range e.Record.Fields {
		if f.Key == key {
			return f.String(), true
		}
	}
	for _, l := range e.Labels {
		if l.Key == key {
			return l.Value, true
		}
	}
	return "", false
}

var _ query.Record = (*Entry)(nil)
