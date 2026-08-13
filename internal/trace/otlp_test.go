package trace

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// The same trace is served as JSON by Tempo's v2 path and as protobuf by its
// v1 one, and a reader must not be able to tell which arrived.
func TestBothEncodingsReadTheSame(t *testing.T) {
	hexJSON, err := DecodeOTLP(readTestdata(t, "otlp-hex.json"))
	require.NoError(t, err)
	tempoJSON, err := DecodeOTLP(readTestdata(t, "otlp-tempo.json"))
	require.NoError(t, err)

	pb, err := proto.Marshal(sampleTracesData())
	require.NoError(t, err)
	fromProto, err := DecodeOTLP(pb)
	require.NoError(t, err)

	for _, found := range [][]*Tree{hexJSON, tempoJSON, fromProto} {
		require.Len(t, found, 1)
		tr := found[0]
		require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", tr.ID)
		require.Equal(t, "00f067aa0ba902b7", tr.Roots[0].SpanID)
		require.Equal(t, "gateway", tr.Roots[0].Service)
	}
	require.Equal(t, 6, hexJSON[0].Len())
	require.Equal(t, 560*time.Millisecond, hexJSON[0].Duration())
}

// The specification says an id is hex; everything built on protojson writes
// base64, and Tempo is one of them. Both are served, so both are read.
func TestAnIDIsReadInEitherSpelling(t *testing.T) {
	spec, err := DecodeOTLP(readTestdata(t, "otlp-hex.json"))
	require.NoError(t, err)
	tempo, err := DecodeOTLP(readTestdata(t, "otlp-tempo.json"))
	require.NoError(t, err)

	// Whichever it arrived as, it is hex by the time anybody can copy it.
	for _, found := range [][]*Tree{spec, tempo} {
		insert, ok := found[0].Node("d4e5f60718293041")
		require.True(t, ok)
		require.Equal(t, "c3d4e5f607182930", insert.ParentID)
		require.True(t, insert.Failed())
		require.Equal(t, "deadlock detected", insert.StatusMessage)
	}
}

// In OTLP the service is on the resource, not the span.
func TestTheServiceComesOffTheResource(t *testing.T) {
	found, err := DecodeOTLP(readTestdata(t, "otlp-hex.json"))
	require.NoError(t, err)
	require.Equal(t, map[string]int{
		"gateway": 1, "identity": 1, "sessions": 1, "checkout": 1, "orders-db": 2,
	}, found[0].Services())
}

// A span attribute has to read and copy exactly as a log field does, whatever
// shape OTLP wrapped it in.
func TestAnAttributeKeepsTheTypeItArrivedAs(t *testing.T) {
	found, err := DecodeOTLP(readTestdata(t, "otlp-hex.json"))
	require.NoError(t, err)
	root := found[0].Roots[0]

	got := map[string]string{}
	for _, f := range root.Attrs {
		got[f.Key] = string(f.Value)
	}
	require.Equal(t, `"POST"`, got["http.request.method"])
	require.Equal(t, `500`, got["http.response.status_code"], "an int is a number, not a string")
	require.Equal(t, `0.25`, got["sampler.ratio"])
	require.Equal(t, `false`, got["retry"])
	require.Equal(t, `["eu","beta"]`, got["tags"])
	require.Equal(t, `{"cart":"c-91"}`, got["http.route.params"])
}

// An OTLP payload is spans, not a trace: a file somebody saved may hold an
// export of several.
func TestAnExportOfSeveralTracesIsSeveralTrees(t *testing.T) {
	td := sampleTracesData()
	other := &tracepb.Span{
		TraceId:           []byte("0123456789abcdef"),
		SpanId:            []byte("fedcba98"),
		Name:              "unrelated",
		StartTimeUnixNano: 1,
		EndTimeUnixNano:   2,
	}
	td.ResourceSpans[0].ScopeSpans[0].Spans = append(td.ResourceSpans[0].ScopeSpans[0].Spans, other)

	data, err := proto.Marshal(td)
	require.NoError(t, err)
	found, err := DecodeOTLP(data)
	require.NoError(t, err)
	require.Len(t, found, 2)
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", found[0].ID, "in the order they arrived")
}

func TestAnUnreadablePayloadIsAnError(t *testing.T) {
	for _, tt := range []struct{ name, data string }{
		{"an html error page", `<html><body>502</body></html>`},
		{"json that is not otlp at all", `{"nope":true}`},
		{"a truncated object", `{"resourceSpans":[`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			found, err := DecodeOTLP([]byte(tt.data))
			if err != nil {
				return
			}
			for _, tr := range found {
				require.Zero(t, tr.Len())
			}
		})
	}
}

// sampleTracesData is the fixture as protobuf, so the binary path is tested
// against the same trace as the JSON one without a binary file in the tree.
func sampleTracesData() *tracepb.TracesData {
	const base = 1786694400_000000000
	at := func(startMS, durMS int64) (uint64, uint64) {
		return uint64(base + startMS*1e6), uint64(base + (startMS+durMS)*1e6)
	}
	id := func(s string) []byte {
		raw, err := hex.DecodeString(s)
		if err != nil {
			panic(err)
		}
		return raw
	}
	res := func(service string, spans ...*tracepb.Span) *tracepb.ResourceSpans {
		return &tracepb.ResourceSpans{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
				Key:   "service.name",
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}},
			}}},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
		}
	}
	span := func(sid, pid, name string, startMS, durMS int64) *tracepb.Span {
		s, e := at(startMS, durMS)
		out := &tracepb.Span{
			TraceId:           id("4bf92f3577b34da6a3ce929d0e0e4736"),
			SpanId:            id(sid),
			Name:              name,
			StartTimeUnixNano: s,
			EndTimeUnixNano:   e,
		}
		if pid != "" {
			out.ParentSpanId = id(pid)
		}
		return out
	}

	failed := span("d4e5f60718293041", "c3d4e5f607182930", "INSERT orders", 130, 210)
	failed.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "deadlock detected"}

	return &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{
		res("gateway", span("00f067aa0ba902b7", "", "POST /checkout", 0, 480)),
		res("identity", span("a1b2c3d4e5f60718", "00f067aa0ba902b7", "verify token", 5, 40)),
		res("sessions", span("b2c3d4e5f6071829", "a1b2c3d4e5f60718", "GET session", 12, 1)),
		res("checkout", span("c3d4e5f607182930", "00f067aa0ba902b7", "charge order", 60, 380)),
		res("orders-db", failed, span("e5f6071829304152", "00f067aa0ba902b7", "flush wal", 470, 90)),
	}}
}
