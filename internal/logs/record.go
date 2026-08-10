// Package logs stores, parses and renders log lines.
package logs

import (
	"time"

	"github.com/go-faster/jx"
	"go.uber.org/zap/zapcore"
)

// Field is a single structured attribute of a [Record], in the order it
// appeared on the wire.
type Field struct {
	Key   string
	Value jx.Raw
}

// String renders the value for display, unquoting JSON strings.
func (f Field) String() string { return asString(f.Value) }

// Record is the structured view of a log line.
//
// It intentionally mirrors the shape of oteldb's logparser.Record so that
// parsing can be delegated there once that package is exported.
type Record struct {
	// Structured is false for lines we could not parse, which are shown as-is.
	Structured bool

	Time  time.Time
	Level zapcore.Level
	Body  string

	TraceID string
	SpanID  string

	Fields []Field
}

// HasTime reports whether the record carried a usable timestamp.
func (r Record) HasTime() bool { return !r.Time.IsZero() }
