package trace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const tempoSearch = `{
	"traces": [
		{
			"traceID": "4bf92f3577b34da6a3ce929d0e0e4736",
			"rootServiceName": "gateway",
			"rootTraceName": "POST /checkout",
			"startTimeUnixNano": "1786694400000000000",
			"durationMs": 560,
			"spanSets": [{"spans": [{"spanID": "a"}, {"spanID": "b"}], "matched": 2}]
		},
		{
			"traceID": "0af7651916cd43dd8448eb211c80319c",
			"rootServiceName": "gateway",
			"rootTraceName": "GET /cart",
			"startTimeUnixNano": "1786694399000000000",
			"durationMs": 12
		}
	],
	"metrics": {"inspectedTraces": 9}
}`

func TestATempoSearchReadsAsResults(t *testing.T) {
	got, err := DecodeSearch([]byte(tempoSearch))
	require.NoError(t, err)
	require.Len(t, got, 2)

	require.Equal(t, Result{
		TraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
		Service:  "gateway",
		Name:     "POST /checkout",
		Start:    time.Unix(0, 1786694400000000000).UTC(),
		Duration: 560 * time.Millisecond,
		Matched:  2,
	}, got[0])
	require.Equal(t, 12*time.Millisecond, got[1].Duration)
	require.Zero(t, got[1].Matched, "a result with no span set matched nothing it said about")
}

// Tempo counts the spans a query selected and never the trace's own, so a row
// drawn off a search must not claim to know how big the trace is.
func TestATempoResultCountsNoSpans(t *testing.T) {
	got, err := DecodeSearch([]byte(tempoSearch))
	require.NoError(t, err)
	for _, r := range got {
		require.Zero(t, r.Spans)
		require.Zero(t, r.Errors)
	}
}

// protojson writes a 64-bit field as a string and a 32-bit one as a number, so
// both spellings arrive from servers that both believe they speak this API.
func TestASearchNumberIsReadQuotedOrBare(t *testing.T) {
	for _, doc := range []string{
		`{"traces":[{"startTimeUnixNano":"1786694400000000000","durationMs":"560"}]}`,
		`{"traces":[{"startTimeUnixNano":1786694400000000000,"durationMs":560}]}`,
		`{"traces":[{"startTimeUnixNano":1786694400000000000.0,"durationMs":560}]}`,
	} {
		got, err := DecodeSearch([]byte(doc))
		require.NoError(t, err, doc)
		require.Len(t, got, 1)
		require.Equal(t, time.Unix(0, 1786694400000000000).UTC(), got[0].Start, doc)
		require.Equal(t, 560*time.Millisecond, got[0].Duration, doc)
	}
}

// Finding nothing is an answer. A search that returned no trace must not read
// as a search that failed, or the screen would report an error for a query that
// simply did not match.
func TestAnEmptySearchIsNotAFailure(t *testing.T) {
	for _, doc := range []string{`{"traces":[]}`, `{}`, `{"metrics":{}}`} {
		got, err := DecodeSearch([]byte(doc))
		require.NoError(t, err, doc)
		require.Empty(t, got, doc)
	}

	_, err := DecodeSearch([]byte(`not json`))
	require.Error(t, err)
}

// Jaeger answers a search with the traces themselves, so what Tempo only
// summarizes is known here and counted.
func TestAJaegerTraceSummarizesItself(t *testing.T) {
	found, err := DecodeJaeger(readTestdata(t, "checkout.json"))
	require.NoError(t, err)
	require.Len(t, found, 1)

	got := Summary(found[0])
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", got.TraceID)
	require.Equal(t, "gateway", got.Service)
	require.Equal(t, "POST /checkout", got.Name)
	require.Equal(t, 6, got.Spans)
	require.Equal(t, 560*time.Millisecond, got.Duration)
	require.Positive(t, got.Errors, "the checkout trace has a failed span")
	require.Zero(t, got.Matched, "jaeger has no notion of a span the query selected")
}

func TestSummaryOfNothing(t *testing.T) {
	require.Equal(t, Result{}, Summary(nil))
	require.Equal(t, Result{TraceID: "t"}, Summary(Build("t", nil)))
}

func TestResultsSortNewestFirst(t *testing.T) {
	at := func(s int64) time.Time { return time.Unix(s, 0).UTC() }
	got := []Result{
		{TraceID: "old", Start: at(100)},
		{TraceID: "new", Start: at(300)},
		{TraceID: "mid", Start: at(200)},
		{TraceID: "same", Start: at(300)},
	}
	SortResults(got)
	require.Equal(t, []string{"new", "same", "mid", "old"},
		[]string{got[0].TraceID, got[1].TraceID, got[2].TraceID, got[3].TraceID},
		"ties keep the order the database gave them")
}
