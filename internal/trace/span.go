// Package trace holds a trace as it is read: spans, the tree they form, and
// the window of time a view of them covers.
//
// Nothing here draws anything and nothing here fetches anything. A trace
// arrives as a flat list of spans — that is what every backend returns and what
// the wire format is — and the shape a reader wants is a tree over an interval.
// Working that out is arithmetic, so it is tested as arithmetic, apart from the
// terminal it ends up on.
package trace

import (
	"time"

	"github.com/oteldb/telescope/internal/logs"
)

// Status is what a span said about how it ended.
//
// It is deliberately three values and not a boolean: OTLP distinguishes a span
// that reported success from one that reported nothing, and a view that colored
// every unset span green would be claiming something the instrumentation never
// said.
type Status uint8

const (
	// StatusUnset is a span that said nothing about its outcome, which is most
	// of them.
	StatusUnset Status = iota
	StatusOK
	StatusError
)

// Span is one operation inside a trace.
//
// Attrs are [logs.Field] so a span reads with the same machinery a log record
// does: the escaping, the value coloring and the copy-and-open paths in the
// entry view all take that shape already, and a span attribute is a log
// attribute that happens to have arrived on a different endpoint.
type Span struct {
	TraceID  string
	SpanID   string
	ParentID string

	// Name is the operation, Service who ran it. Both are somebody else's
	// bytes and neither is sanitized here — that belongs to whatever draws it.
	Name    string
	Service string

	Start    time.Time
	Duration time.Duration

	Status Status
	// StatusMessage is what an errored span said about why, when it said
	// anything.
	StatusMessage string

	Attrs []logs.Field
}

// End is when the span stopped.
func (s Span) End() time.Time { return s.Start.Add(s.Duration) }

// Failed reports whether the span itself ended badly. It says nothing about
// its children: a request that failed deep inside still returns an error at
// every level above, and a parent that swallowed one reports none.
func (s Span) Failed() bool { return s.Status == StatusError }
