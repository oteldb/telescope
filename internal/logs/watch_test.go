package logs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func eventFixture(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/kubernetes-event.json")
	require.NoError(t, err)
	return data
}

func TestEventsShowTheirOwnMessageAndOccurrenceHistory(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, level string }{{"normal", "info"}, {"warning", "warn"}} {
		t.Run(tt.name, func(t *testing.T) {
			data := strings.Replace(string(eventFixture(t)), `"level": "info"`, `"level": "`+tt.level+`"`, 1)
			e := NewStore(10).Append(source.Line{Data: []byte(data)})
			require.NotNil(t, e)
			w := e.Record.Watch
			require.NotNil(t, w)
			require.NotNil(t, w.Event)
			require.Equal(t, "source-controller", w.Event.Reporter)
			require.Equal(t, int64(1), w.Event.Increase)
			require.Equal(t, int64(1489), w.Count)
			require.Equal(t, "2026-10-29T17:49:42Z", w.Event.FirstSeen.Format(time.RFC3339))
			require.Equal(t, "2026-10-29T20:00:52Z", w.Event.LastSeen.Format(time.RFC3339))
			require.Equal(t, "MODIFIED", w.Action)
			require.Contains(t, e.Head, levelColors[tt.level]+"ArtifactUpToDate")
			require.Contains(t, ansi.Strip(e.Head), "1489 occurrences")
			require.NotContains(t, e.Head, "/lastTimestamp")
			require.NotContains(t, e.Head, "1488")
			require.Equal(t, 1, strings.Count(ansi.Strip(e.Text), "artifact up-to-date"))
			require.Zero(t, e.Extra)
			require.Equal(t, data, string(e.Raw))
			filter := (Filter{Query: "namespace=kube-system reason=ArtifactUpToDate resource_name=metrics-server"}).Compile()
			require.True(t, filter.Match(e))
		})
	}
}

func TestModernEventsKeepTheirFirstAndLastObservation(t *testing.T) {
	t.Parallel()
	line := `{"watch_event":"MODIFIED","kind":"Event","api_version":"events.k8s.io/v1","count":4,"object":{"note":"ready","eventTime":"2026-10-29T18:00:00Z","series":{"lastObservedTime":"2026-10-29T20:00:00Z"},"reportingController":"cluster-controller"},"changes":[{"path":"/series/count","before":2,"after":4}]}`
	w := Parse([]byte(line)).Watch
	require.NotNil(t, w.Event)
	require.Equal(t, "ready", w.Event.Message)
	require.Equal(t, "cluster-controller", w.Event.Reporter)
	require.Equal(t, int64(2), w.Event.Increase)
	require.Equal(t, "2026-10-29T18:00:00Z", w.Event.FirstSeen.Format(time.RFC3339))
	require.Equal(t, "2026-10-29T20:00:00Z", w.Event.LastSeen.Format(time.RFC3339))
}

func TestWatchSummaryOwnsTheValuesItKeeps(t *testing.T) {
	data := eventFixture(t)
	r := Parse(data)
	object := string(r.Watch.Object)
	message := r.Watch.Event.Message
	clear(data)
	_ = Parse([]byte(`{"msg":"next record","object":{"different":true}}`))
	require.Equal(t, object, string(r.Watch.Object))
	require.Equal(t, message, r.Watch.Event.Message)
	require.True(t, json.Valid(r.Watch.Object))
}

func TestWatchSummariesHandleMissingAndMalformedData(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, line string
		event      bool
	}{
		{"ordinary log", `{"msg":"ordinary log","count":1}`, false},
		{"custom Event kind", `{"watch_event":"ADDED","kind":"Event","api_version":"custom.example/v1","object":{"message":123}}`, false},
		{"malformed object", `{"watch_event":"ADDED","kind":"Event","api_version":"v1","object":[]}`, false},
		{"fallback message", `{"watch_event":"ADDED","kind":"Event","api_version":"v1","msg":"fallback","object":{}}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := Parse([]byte(tt.line))
			if r.Watch == nil {
				require.False(t, tt.event)
				return
			}
			require.Equal(t, tt.event, r.Watch.Event != nil)
			if tt.event {
				require.Equal(t, "fallback", r.Watch.Event.Message)
			}
		})
	}
	for _, tt := range []struct {
		count int64
		want  string
	}{{1, "1 occurrence"}, {2, "2 occurrences"}} {
		t.Run(tt.want, func(t *testing.T) { require.Equal(t, tt.want, (WatchRecord{Count: tt.count}).OccurrenceText()) })
	}
}

func FuzzWatchSummary(f *testing.F) {
	f.Add(string(eventFixture(f)))
	f.Add(`{"watch_event":"ADDED","kind":"Event","api_version":"v1","object":{}}`)
	f.Add(`{"watch_event":"MODIFIED","kind":"Event","api_version":"events.k8s.io/v1","object":{"note":"ready"},"changes":[{"path":"/count","before":-9223372036854775808,"after":9223372036854775807}]}`)
	f.Add(`{"watch_event":"ADDED","kind":"Event","api_version":"v1","object":[]}`)
	f.Fuzz(func(t *testing.T, line string) {
		r := Parse([]byte(line))
		if w := r.Watch; w != nil && w.Event != nil {
			require.GreaterOrEqual(t, w.Event.Increase, int64(0))
			for _, change := range w.Changes {
				if len(change.Before) > 0 {
					require.True(t, json.Valid(change.Before))
				}
				if len(change.After) > 0 {
					require.True(t, json.Valid(change.After))
				}
			}
		}
	})
}
