package trace

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/jx"

	"github.com/oteldb/telescope/internal/logs"
)

// DecodeJaeger reads the traces in a Jaeger query API response.
//
// This is the format `/api/traces/{id}` answers with, and it is the one to
// speak first because it is the widest: Jaeger serves it, Tempo serves it, and
// oteldb serves it, which is three databases for one decoder. Its timestamps
// are microseconds since the epoch, not nanoseconds and not RFC 3339.
//
// A response carrying several traces yields several trees, in the order they
// arrived. A trace saved on its own — the object out of the response rather
// than the response — is read too, since that is what somebody pasting one into
// a file ends up with.
func DecodeJaeger(data []byte) ([]*Tree, error) {
	var doc struct {
		Data []jaegerTrace `json:"data"`

		TraceID   string                   `json:"traceID"`
		Spans     []jaegerSpan             `json:"spans"`
		Processes map[string]jaegerProcess `json:"processes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, errors.Wrap(err, "decode")
	}

	found := doc.Data
	if len(found) == 0 && len(doc.Spans) > 0 {
		found = []jaegerTrace{{TraceID: doc.TraceID, Spans: doc.Spans, Processes: doc.Processes}}
	}
	if len(found) == 0 {
		return nil, errors.New("no traces in response")
	}

	out := make([]*Tree, 0, len(found))
	for _, t := range found {
		out = append(out, t.tree())
	}
	return out, nil
}

type jaegerTrace struct {
	TraceID   string                   `json:"traceID"`
	Spans     []jaegerSpan             `json:"spans"`
	Processes map[string]jaegerProcess `json:"processes"`
}

type jaegerProcess struct {
	ServiceName string `json:"serviceName"`
}

type jaegerSpan struct {
	TraceID       string      `json:"traceID"`
	SpanID        string      `json:"spanID"`
	OperationName string      `json:"operationName"`
	References    []jaegerRef `json:"references"`
	StartTime     int64       `json:"startTime"`
	Duration      int64       `json:"duration"`
	Tags          []jaegerTag `json:"tags"`
	ProcessID     string      `json:"processID"`
	Process       *jaegerProc `json:"process"`
	Warnings      []string    `json:"warnings"`
}

// jaegerProc is a process written into the span instead of into the response's
// table, which is what the ES and OTLP translations do.
type jaegerProc struct {
	ServiceName string `json:"serviceName"`
}

type jaegerRef struct {
	RefType string `json:"refType"`
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

type jaegerTag struct {
	Key   string          `json:"key"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// unknownService is what Jaeger itself calls a span whose process it cannot
// find, and a span with no service still has to be drawn as something.
const unknownService = "unknown-service"

func (t jaegerTrace) tree() *Tree {
	spans := make([]Span, 0, len(t.Spans))
	for _, s := range t.Spans {
		spans = append(spans, s.span(t))
	}
	return Build(t.TraceID, spans)
}

func (s jaegerSpan) span(t jaegerTrace) Span {
	out := Span{
		TraceID:  s.TraceID,
		SpanID:   s.SpanID,
		ParentID: s.parent(),
		Name:     s.OperationName,
		Service:  s.service(t),
	}
	// Microseconds, and a thousand of them do not always fit where one did: a
	// duration this format can write is not always one Go can hold, and the
	// multiplication wraps into a span that ran backwards. A negative one is
	// nothing a clock produced either.
	if s.Duration > 0 && s.Duration <= math.MaxInt64/int64(time.Microsecond) {
		out.Duration = time.Duration(s.Duration) * time.Microsecond
	}
	// Left at the zero time rather than turned into 1970, so [repair] can tell
	// a span with no clock from one that really started at the epoch and give
	// it its caller's.
	if s.StartTime != 0 {
		out.Start = time.UnixMicro(s.StartTime).UTC()
	}
	out.Status, out.StatusMessage = s.status()
	for _, tag := range s.Tags {
		out.Attrs = append(out.Attrs, logs.Field{Key: tag.Key, Value: jx.Raw(tag.Value)})
	}
	return out
}

// parent is the span this one hung off.
//
// CHILD_OF is preferred over FOLLOWS_FROM because only the first says the
// parent was waiting; a span that merely followed another is drawn under it for
// want of anywhere better, which is what jaeger-ui does too. A reference into
// another trace is not a parent here — there is no span to hang it on — so it
// is skipped and the span comes out detached.
func (s jaegerSpan) parent() string {
	var follows string
	for _, ref := range s.References {
		if ref.TraceID != "" && s.TraceID != "" && ref.TraceID != s.TraceID {
			continue
		}
		switch ref.RefType {
		case "CHILD_OF":
			return ref.SpanID
		case "FOLLOWS_FROM":
			if follows == "" {
				follows = ref.SpanID
			}
		}
	}
	return follows
}

func (s jaegerSpan) service(t jaegerTrace) string {
	if s.Process != nil && s.Process.ServiceName != "" {
		return s.Process.ServiceName
	}
	if p, ok := t.Processes[s.ProcessID]; ok && p.ServiceName != "" {
		return p.ServiceName
	}
	return unknownService
}

// status reads how the span ended out of its tags, which is where every
// translation into this format puts it. There is more than one spelling because
// the format predates OpenTelemetry and kept the old one working.
func (s jaegerSpan) status() (Status, string) {
	var (
		st  Status
		msg string
	)
	for _, tag := range s.Tags {
		switch tag.Key {
		case "error":
			// OpenTracing's boolean, still written by every tracer that has not
			// moved, and by some that have.
			if str(tag.Value) == "true" {
				st = StatusError
			}
		case "otel.status_code", "status.code":
			switch strings.ToUpper(str(tag.Value)) {
			case "ERROR", "2":
				st = StatusError
			case "OK", "1":
				if st != StatusError {
					st = StatusOK
				}
			}
		case "otel.status_description", "status.message":
			msg = str(tag.Value)
		}
	}
	return st, msg
}

// str reads a tag's value as text whatever it was written as: the type is in a
// field beside it and the value itself may be a string, a number or a boolean.
func str(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	return string(raw)
}
