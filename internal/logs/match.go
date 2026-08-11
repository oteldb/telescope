package logs

import (
	"strings"

	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/query"
)

// Haystack is what a bare term of a query matches: the line, and the labels the
// source reported beside it, since the list has no room to show them.
func (e *Entry) Haystack() [][]byte { return [][]byte{e.Raw, []byte(e.labelText)} }

// Level is the severity the line reported, if it reported one.
func (e *Entry) Level() (zapcore.Level, bool) { return e.Record.Level, e.Record.HasLevel }

// Field resolves a name a query compares against: what the line called it
// first, then what the source said about the line, then the few names a record
// is read under whatever it called them.
func (e *Entry) Field(key string) (string, bool) {
	for _, f := range e.Record.Fields {
		if strings.EqualFold(f.Key, key) {
			return f.String(), true
		}
	}
	for _, l := range e.Labels {
		if strings.EqualFold(l.Key, key) {
			return l.Value, true
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

var _ query.Record = (*Entry)(nil)
