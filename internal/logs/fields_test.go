package logs

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func indexed(t *testing.T, lines ...source.Line) *Store {
	t.Helper()
	s := NewStore(100)
	for _, l := range lines {
		require.NotNil(t, s.Append(l))
	}
	return s
}

// TestFieldNames: what a stream is labeled with is what its lines turned out to
// carry, including the names a record is read under whatever the shipper called
// them.
func TestFieldNames(t *testing.T) {
	s := indexed(t,
		line(`{"level":"info","msg":"started","pod":"api-7"}`),
		line(`{"level":"error","msg":"exploded","zone":"eu"}`),
	)
	require.Equal(t, []string{"level", "msg", "pod", "stream", "zone"}, s.FieldNames())
}

// TestFieldNamesIncludeTheLabels: a database reports its labels beside the line
// rather than inside it, and a filter compares them just the same.
func TestFieldNamesIncludeTheLabels(t *testing.T) {
	s := indexed(t, source.Line{
		Data:   []byte("plain text"),
		Labels: []source.Label{{Key: "namespace", Value: "oteldb"}},
	})
	require.Contains(t, s.FieldNames(), "namespace")
	require.Equal(t, []string{"oteldb"}, s.FieldValues("namespace"))
}

// TestFieldValues: each value once, and nothing under a name never seen.
func TestFieldValues(t *testing.T) {
	s := indexed(t,
		line(`{"msg":"a","pod":"api-7"}`),
		line(`{"msg":"b","pod":"api-7"}`),
		line(`{"msg":"c","pod":"api-8"}`),
	)
	require.Equal(t, []string{"api-7", "api-8"}, s.FieldValues("pod"))
	require.Empty(t, s.FieldValues("zone"))
}

// TestFieldValuesSkipWhatCannotBeCompleted: a trace id and a timestamp are a
// different string on every line. The name is still worth offering.
func TestFieldValuesSkipWhatCannotBeCompleted(t *testing.T) {
	s := indexed(t, line(`{"msg":"a","ts":"2026-08-10T10:00:00Z","trace_id":"0af7651916cd43dd"}`))
	require.Contains(t, s.FieldNames(), "trace_id")
	require.Contains(t, s.FieldNames(), "ts")
	require.Empty(t, s.FieldValues("trace_id"))
	require.Empty(t, s.FieldValues("ts"))
	require.Empty(t, s.FieldValues("msg"), "a message is not a value to complete either")
}

// TestFieldIndexIsBounded: a log stream is not bounded and neither is what it
// says, so what is remembered of it has to be.
func TestFieldIndexIsBounded(t *testing.T) {
	s := NewStore(10)
	for i := range maxIndexValues * 2 {
		v := strconv.Itoa(i)
		s.Append(line(`{"msg":"x","pod":"api-` + v + `","k` + v + `":"v"}`))
	}
	require.Len(t, s.FieldValues("pod"), maxIndexValues)
	require.Less(t, len(s.FieldNames()), maxIndexValues*2+8, "and so do the names")

	long := make([]byte, maxIndexValueLen+1)
	for i := range long {
		long[i] = 'x'
	}
	s.Append(line(`{"msg":"x","zone":"` + string(long) + `"}`))
	require.Contains(t, s.FieldNames(), "zone")
	require.Empty(t, s.FieldValues("zone"), "a value too long to be read is too long to be offered")
}

// TestFieldIndexOutlivesTheCap: a name that was true of this stream stays worth
// completing, and forgetting it would make the suggestions flicker as the cap
// bites.
func TestFieldIndexOutlivesTheCap(t *testing.T) {
	s := NewStore(1)
	s.Append(line(`{"msg":"a","pod":"api-7"}`))
	s.Append(line(`{"msg":"b","zone":"eu"}`))

	require.Equal(t, 1, s.Len())
	require.Equal(t, []string{"api-7"}, s.FieldValues("pod"))

	s.Reset()
	require.Empty(t, s.FieldNames(), "a new stream is a new index")
}

// TestFieldValuesOfAMerge: the tag a merge marks its children with is a field
// like any other, and one of the few worth completing.
func TestFieldValuesOfAMerge(t *testing.T) {
	s := indexed(t,
		source.Line{Data: []byte(`{"msg":"a"}`), Source: "api"},
		source.Line{Data: []byte(`{"msg":"b"}`), Source: "worker"},
	)
	require.Equal(t, []string{"api", "worker"}, s.FieldValues("source"))
	require.Equal(t, []string{"stdout"}, s.FieldValues("stream"))
}
