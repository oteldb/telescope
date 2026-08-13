package trace

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/jx"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/oteldb/telescope/internal/logs"
)

// DecodeOTLP reads the traces in an OTLP payload, in either encoding.
//
// Tempo answers `/api/traces/{id}` with protobuf and `/api/v2/traces/{id}` with
// JSON, and the two carry the same message, so which arrived is sniffed rather
// than asked for: a body that starts with a brace is JSON and anything else is
// protobuf. That is also what lets a trace saved to a file be opened without
// having to say which one it is.
//
// Unlike a Jaeger response, an OTLP payload is not one trace. It is spans,
// which may belong to several, so they are grouped by the id they carry.
func DecodeOTLP(data []byte) ([]*Tree, error) {
	td, err := unmarshalOTLP(data)
	if err != nil {
		return nil, err
	}
	return treesOf(spansOfOTLP(td)), nil
}

func unmarshalOTLP(data []byte) (*tracepb.TracesData, error) {
	if trimmedStartsWith(data, '{') {
		return unmarshalOTLPJSON(data)
	}
	var td tracepb.TracesData
	// A Tempo v1 body is a tempopb.Trace, whose one field is the same repeated
	// ResourceSpans at the same number as TracesData's, so the two decode as
	// each other and there is no reason to carry Tempo's type to say so.
	if err := proto.Unmarshal(data, &td); err != nil {
		return nil, errors.Wrap(err, "decode otlp")
	}
	return &td, nil
}

func trimmedStartsWith(data []byte, c byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return b == c
		}
	}
	return false
}

// unmarshalOTLPJSON reads OTLP's JSON encoding, in both spellings of it that
// are actually served.
//
// The specification says a trace or span id is written as a hex string.
// Everything built on protojson writes base64 instead, because that is what
// protojson does with a bytes field, and Tempo is one of them — it is what the
// Grafana plugin is tested against, so it is the one that matters in practice.
// Neither is going away.
//
// The two cannot be told apart by trying one and catching the failure, which is
// the trap here: hex digits are all in the base64 alphabet, so a hex id decodes
// as base64 without complaint and yields sixteen bytes of nonsense that still
// look like an id. Every document would parse and every id would be wrong.
// They are told apart by length instead — see [hexID] — and the ids are
// normalized before protojson ever sees them.
func unmarshalOTLPJSON(data []byte) (*tracepb.TracesData, error) {
	// Tempo's v2 endpoint wraps the payload in the reply that carried it.
	var wrapper struct {
		Trace json.RawMessage `json:"trace"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && len(wrapper.Trace) > 0 {
		data = wrapper.Trace
	}

	normalized, err := hexIDsToBase64(data)
	if err != nil {
		return nil, errors.Wrap(err, "decode otlp json")
	}
	var td tracepb.TracesData
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(normalized, &td); err != nil {
		return nil, errors.Wrap(err, "decode otlp json")
	}
	return &td, nil
}

// hexID reads an id written the way the specification says, and reports
// whether it was one.
//
// Length is what decides, because content cannot: every hex digit is also a
// base64 character, so "4bf92f3577b34da6a3ce929d0e0e4736" is a well-formed
// base64 string as well as a well-formed trace id, and decoding it as base64
// gives twenty-four bytes that no error will ever mention.
//
// A trace id is sixteen bytes and a span id eight, so hex spells them in
// thirty-two and sixteen characters, and base64 in twenty-four and twelve.
// Those four lengths do not collide, and no valid id has any other. So a
// string of exactly thirty-two or sixteen hex digits is hex, and everything
// else is left for protojson to read as base64.
func hexID(str []byte) ([]byte, bool) {
	if len(str) != 2*traceIDLen && len(str) != 2*spanIDLen {
		return nil, false
	}
	raw := make([]byte, hex.DecodedLen(len(str)))
	if _, err := hex.Decode(raw, str); err != nil {
		return nil, false
	}
	return raw, true
}

// The lengths OTLP gives an id, in bytes.
const (
	traceIDLen = 16
	spanIDLen  = 8
)

// isIDKey reports whether a field is one of the three written as bytes on the
// wire and as text in JSON. Switching on the bytes rather than looking up a
// string keeps the walk from allocating a key it will throw away.
func isIDKey(key []byte) bool {
	switch string(key) {
	case "traceId", "spanId", "parentSpanId":
		return true
	default:
		return false
	}
}

// hexIDsToBase64 rewrites the ids of a specification-conformant OTLP document
// into what protojson will read.
//
// The rewrite is streamed rather than done over a decoded document: the values
// beside the ids are somebody's whole trace, and unmarshalling them into maps
// only to marshal them back would cost more than the parse this is retrying.
// Everything that is not an id is copied through as the bytes it arrived as.
//
// It walks the whole document rather than the three places an id is known to
// appear, because the walk is the cheap part and the shape is not worth
// encoding twice. This runs only after a parse has already failed, so the
// second pass is paid for by documents that would otherwise not be read.
func hexIDsToBase64(data []byte) ([]byte, error) {
	e := &jx.Encoder{}
	if err := rewriteIDs(jx.DecodeBytes(data), e, false); err != nil {
		return nil, err
	}
	return e.Bytes(), nil
}

func rewriteIDs(d *jx.Decoder, e *jx.Encoder, isID bool) error {
	switch d.Next() {
	case jx.Object:
		e.ObjStart()
		if err := d.ObjBytes(func(d *jx.Decoder, key []byte) error {
			e.FieldStart(string(key))
			return rewriteIDs(d, e, isIDKey(key))
		}); err != nil {
			return err
		}
		e.ObjEnd()
		return nil
	case jx.Array:
		e.ArrStart()
		// An id is never an array element, so nothing inside one inherits the
		// name of the field the array is under.
		if err := d.Arr(func(d *jx.Decoder) error { return rewriteIDs(d, e, false) }); err != nil {
			return err
		}
		e.ArrEnd()
		return nil
	case jx.String:
		// Held only across the encode below, which copies: StrBytes aliases the
		// decoder's buffer and dies at the next call on it.
		str, err := d.StrBytes()
		if err != nil {
			return err
		}
		if isID {
			if raw, ok := hexID(str); ok {
				e.Base64(raw)
				return nil
			}
		}
		e.ByteStr(str)
		return nil
	default:
		raw, err := d.Raw()
		if err != nil {
			return err
		}
		e.Raw(raw)
		return nil
	}
}

func spansOfOTLP(td *tracepb.TracesData) []Span {
	var out []Span
	for _, rs := range td.GetResourceSpans() {
		service := serviceOf(rs.GetResource().GetAttributes())
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				out = append(out, spanOfOTLP(s, service))
			}
		}
	}
	return out
}

func spanOfOTLP(s *tracepb.Span, service string) Span {
	out := Span{
		TraceID:  hex.EncodeToString(s.GetTraceId()),
		SpanID:   hex.EncodeToString(s.GetSpanId()),
		ParentID: hex.EncodeToString(s.GetParentSpanId()),
		Name:     s.GetName(),
		Service:  service,
	}
	// OTLP counts nanoseconds in a uint64 and Go counts them in an int64, so
	// half the values a payload can carry have no time to be converted to and
	// their difference has no duration. Unchecked, the conversion wraps: a span
	// comes out having lasted minus two hundred and eighty years, which is not
	// a bar anything can draw. Neither bound is reachable by a real span — the
	// int64 runs out in the year 2262 and a duration in 292 years — so anything
	// past them is a span with no clock rather than a span to argue with.
	if start, end := s.GetStartTimeUnixNano(), s.GetEndTimeUnixNano(); start != 0 && start <= math.MaxInt64 {
		out.Start = time.Unix(0, int64(start)).UTC()
		if end > start && end-start <= math.MaxInt64 {
			out.Duration = time.Duration(end - start)
		}
	}
	switch st := s.GetStatus(); st.GetCode() {
	case tracepb.Status_STATUS_CODE_ERROR:
		out.Status, out.StatusMessage = StatusError, st.GetMessage()
	case tracepb.Status_STATUS_CODE_OK:
		out.Status = StatusOK
	}
	for _, kv := range s.GetAttributes() {
		out.Attrs = append(out.Attrs, logs.Field{Key: kv.GetKey(), Value: valueOfOTLP(kv.GetValue())})
	}
	return out
}

// serviceOf reads who ran a span. In OTLP that is not on the span but on the
// resource above it, which is the one thing this format and Jaeger's agree
// about beyond the names.
func serviceOf(attrs []*commonpb.KeyValue) string {
	for _, kv := range attrs {
		if kv.GetKey() == "service.name" {
			if name := kv.GetValue().GetStringValue(); name != "" {
				return name
			}
		}
	}
	return unknownService
}

// valueOfOTLP writes an attribute back out as the JSON its value was, so that a
// span attribute reads and copies exactly as a log field does: a string as a
// string, a number as a number, and anything nested as itself.
func valueOfOTLP(v *commonpb.AnyValue) jx.Raw {
	e := &jx.Encoder{}
	writeOTLPValue(e, v)
	return jx.Raw(e.Bytes())
}

func writeOTLPValue(e *jx.Encoder, v *commonpb.AnyValue) {
	switch v := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		e.Str(v.StringValue)
	case *commonpb.AnyValue_BoolValue:
		e.Bool(v.BoolValue)
	case *commonpb.AnyValue_IntValue:
		e.Int64(v.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		e.Float64(v.DoubleValue)
	case *commonpb.AnyValue_BytesValue:
		e.Base64(v.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		e.ArrStart()
		for _, item := range v.ArrayValue.GetValues() {
			writeOTLPValue(e, item)
		}
		e.ArrEnd()
	case *commonpb.AnyValue_KvlistValue:
		e.ObjStart()
		for _, kv := range v.KvlistValue.GetValues() {
			e.FieldStart(kv.GetKey())
			writeOTLPValue(e, kv.GetValue())
		}
		e.ObjEnd()
	default:
		e.Null()
	}
}

// treesOf groups spans by the trace they belong to.
//
// An OTLP payload is spans and not a trace: a fetch by id answers with one, but
// a file somebody saved may hold a whole export. Order is by the id so that
// reading the same document twice gives the same first trace.
func treesOf(spans []Span) []*Tree {
	byTrace := map[string][]Span{}
	var order []string
	for _, s := range spans {
		if _, seen := byTrace[s.TraceID]; !seen {
			order = append(order, s.TraceID)
		}
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
	}
	out := make([]*Tree, 0, len(order))
	for _, id := range order {
		out = append(out, Build(id, byTrace[id]))
	}
	return out
}
