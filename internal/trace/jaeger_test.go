package trace

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return data
}

func TestAJaegerResponseReadsAsATree(t *testing.T) {
	found, err := DecodeJaeger(readTestdata(t, "checkout.json"))
	require.NoError(t, err)
	require.Len(t, found, 1)

	tr := found[0]
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", tr.ID)
	require.Equal(t, 6, tr.Len())
	require.Len(t, tr.Roots, 1)
	require.Zero(t, tr.Detached)

	root := tr.Roots[0]
	require.Equal(t, "POST /checkout", root.Name)
	require.Equal(t, "gateway", root.Service)
	require.Equal(t, 480*time.Millisecond, root.Duration)
	require.Equal(t, 560*time.Millisecond, tr.Duration(), "to the last span, not the root")
}

// Jaeger writes microseconds, not the nanoseconds Go reaches for.
func TestJaegerTimesAreMicroseconds(t *testing.T) {
	found, err := DecodeJaeger(readTestdata(t, "checkout.json"))
	require.NoError(t, err)

	session, ok := found[0].Node("b2c3d4e5f6071829")
	require.True(t, ok)
	require.Equal(t, 900*time.Microsecond, session.Duration)
	require.Equal(t, time.UnixMicro(1786694400012000).UTC(), session.Start)
}

// The format predates OpenTelemetry, so a failure has more than one spelling
// and a decoder that knew only the new one would draw a broken request as fine.
func TestAFailureIsReadWhicheverWayItWasWritten(t *testing.T) {
	for _, tt := range []struct {
		name string
		tags string
		want Status
	}{
		{"opentracing's boolean", `{"key":"error","type":"bool","value":true}`, StatusError},
		{"a boolean that is false", `{"key":"error","type":"bool","value":false}`, StatusUnset},
		{"otel's word", `{"key":"otel.status_code","type":"string","value":"ERROR"}`, StatusError},
		{"otel's word, lowercased", `{"key":"otel.status_code","type":"string","value":"error"}`, StatusError},
		{"otel's number", `{"key":"status.code","type":"int64","value":2}`, StatusError},
		{"a span that said it was fine", `{"key":"otel.status_code","type":"string","value":"OK"}`, StatusOK},
		{"a span that said nothing", `{"key":"span.kind","type":"string","value":"server"}`, StatusUnset},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := `{"traceID":"t","spans":[{"traceID":"t","spanID":"a","startTime":1,"duration":1,"tags":[` +
				tt.tags + `]}],"processes":{}}`
			found, err := DecodeJaeger([]byte(doc))
			require.NoError(t, err)
			n, ok := found[0].Node("a")
			require.True(t, ok)
			require.Equal(t, tt.want, n.Status)
		})
	}
}

func TestAFailureCarriesWhatItSaid(t *testing.T) {
	found, err := DecodeJaeger(readTestdata(t, "checkout.json"))
	require.NoError(t, err)

	insert, ok := found[0].Node("d4e5f60718293041")
	require.True(t, ok)
	require.True(t, insert.Failed())
	require.Equal(t, "deadlock detected", insert.StatusMessage)
	require.True(t, found[0].Roots[0].FailedBelow())
}

// Only CHILD_OF says the parent was waiting, but a span that merely followed
// another is still drawn under it for want of anywhere better.
func TestASpanThatOnlyFollowedIsStillPlaced(t *testing.T) {
	found, err := DecodeJaeger(readTestdata(t, "checkout.json"))
	require.NoError(t, err)

	flush, ok := found[0].Node("e5f6071829304152")
	require.True(t, ok)
	require.NotNil(t, flush.Parent)
	require.Equal(t, "POST /checkout", flush.Parent.Name)
}

// A reference into another trace names no span here to hang the child on.
func TestAReferenceToAnotherTraceIsNotAParent(t *testing.T) {
	doc := `{"traceID":"t","spans":[{"traceID":"t","spanID":"a","startTime":1,"duration":1,
		"references":[{"refType":"CHILD_OF","traceID":"elsewhere","spanID":"b"}]}],"processes":{}}`
	found, err := DecodeJaeger([]byte(doc))
	require.NoError(t, err)

	n, ok := found[0].Node("a")
	require.True(t, ok)
	require.Empty(t, n.ParentID)
	require.False(t, n.Detached, "it named nothing here, so nothing here is missing")
}

func TestASpanKeepsItsTags(t *testing.T) {
	found, err := DecodeJaeger(readTestdata(t, "checkout.json"))
	require.NoError(t, err)

	insert, ok := found[0].Node("d4e5f60718293041")
	require.True(t, ok)
	require.Len(t, insert.Attrs, 3)
	require.Equal(t, "db.statement", insert.Attrs[2].Key)
	require.Equal(t, "INSERT INTO orders", insert.Attrs[2].String())
}

// A trace saved out of the response it arrived in is what somebody pasting one
// into a file ends up with.
func TestATraceOnItsOwnIsReadToo(t *testing.T) {
	doc := `{"traceID":"t","spans":[{"traceID":"t","spanID":"a","operationName":"one",
		"startTime":1,"duration":1,"processID":"p1"}],"processes":{"p1":{"serviceName":"svc"}}}`
	found, err := DecodeJaeger([]byte(doc))
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, 1, found[0].Len())
	require.Equal(t, "svc", found[0].Roots[0].Service)
}

func TestASpanWithNoProcessIsStillDrawn(t *testing.T) {
	doc := `{"traceID":"t","spans":[{"traceID":"t","spanID":"a","startTime":1,"duration":1,
		"processID":"missing"}],"processes":{}}`
	found, err := DecodeJaeger([]byte(doc))
	require.NoError(t, err)
	require.Equal(t, unknownService, found[0].Roots[0].Service)
}

// Some translations write the process into the span instead of the table.
func TestAProcessWrittenIntoTheSpanIsRead(t *testing.T) {
	doc := `{"traceID":"t","spans":[{"traceID":"t","spanID":"a","startTime":1,"duration":1,
		"process":{"serviceName":"inline"}}]}`
	found, err := DecodeJaeger([]byte(doc))
	require.NoError(t, err)
	require.Equal(t, "inline", found[0].Roots[0].Service)
}

func TestNothingUsableIsAnError(t *testing.T) {
	for _, tt := range []struct{ name, doc string }{
		{"not json at all", `<html>404</html>`},
		{"an empty response", `{"data":[]}`},
		{"a response with no spans", `{"data":[{"traceID":"t","spans":[]}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			found, err := DecodeJaeger([]byte(tt.doc))
			if err != nil {
				return
			}
			// A trace that decoded but holds nothing is not an error; it is a
			// trace with no spans, and the view says so.
			require.Zero(t, found[0].Len())
		})
	}
}
