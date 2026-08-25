package source

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestATraceQueryCompilesIntoTraceQL(t *testing.T) {
	for _, tt := range []struct {
		name  string
		query TraceQuery
		want  string
	}{
		{
			name:  "nothing asked is everything in the window",
			query: TraceQuery{},
			want:  "{}",
		},
		{
			name:  "a service",
			query: TraceQuery{Service: "api"},
			want:  `{resource.service.name="api"}`,
		},
		{
			name:  "a service and what it was called to do",
			query: TraceQuery{Service: "api", Operation: "GET /v1/orders"},
			want:  `{resource.service.name="api" && name="GET /v1/orders"}`,
		},
		{
			name:  "a tag is unscoped, since it may have been recorded either side",
			query: TraceQuery{Tags: []TraceTag{{Key: "http.route", Value: "/v1/orders"}}},
			want:  `{.http.route="/v1/orders"}`,
		},
		{
			name:  "a tag scoped by hand is left as it was typed",
			query: TraceQuery{Tags: []TraceTag{{Key: "span.http.route", Value: "/x"}}},
			want:  `{span.http.route="/x"}`,
		},
		{
			name:  "an intrinsic is a field of the span, not an attribute of it",
			query: TraceQuery{Tags: []TraceTag{{Key: "status", Value: "error"}}},
			want:  `{status="error"}`,
		},
		{
			name: "values keep the type they were typed as",
			query: TraceQuery{Tags: []TraceTag{
				{Key: "http.status_code", Value: "500"},
				{Key: "error", Value: "true"},
				{Key: "sampler.param", Value: "0.5"},
			}},
			want: `{.http.status_code=500 && .error=true && .sampler.param=0.5}`,
		},
		{
			name:  "how long the trace took, not the span",
			query: TraceQuery{MinDuration: 500 * time.Millisecond, MaxDuration: 2 * time.Second},
			want:  `{traceDuration>500ms && traceDuration<2000ms}`,
		},
		{
			name: "everything at once",
			query: TraceQuery{
				Service:     "api",
				Operation:   "GET /v1/orders",
				Tags:        []TraceTag{{Key: "http.status_code", Value: "500"}},
				MinDuration: 100 * time.Millisecond,
			},
			want: `{resource.service.name="api" && name="GET /v1/orders" && .http.status_code=500 && traceDuration>100ms}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.query.TraceQL())
		})
	}
}

// A value is somebody else's text going into a query language, and the quoting
// is the whole of what keeps it one value rather than the end of the query and
// the start of something else.
func TestATraceQLValueCannotEndTheQuery(t *testing.T) {
	q := TraceQuery{Service: `api" || true || "`, Tags: []TraceTag{{Key: "k", Value: "a\\\"b"}}}
	require.Equal(t, `{resource.service.name="api\" || true || \"" && .k="a\\\"b"}`, q.TraceQL())
}

func TestATraceQueryCompilesIntoJaegerParams(t *testing.T) {
	now := time.Unix(1786694400, 0).UTC()
	q := TraceQuery{
		Service:     "api",
		Operation:   "GET /v1/orders",
		Tags:        []TraceTag{{Key: "http.status_code", Value: "500"}},
		MinDuration: 100 * time.Millisecond,
		Limit:       5,
		Range:       Range{Since: now.Add(-time.Hour)},
	}
	got := q.jaegerParams(now)

	require.Equal(t, "api", got.Get("service"))
	require.Equal(t, "GET /v1/orders", got.Get("operation"))
	require.Equal(t, `{"http.status_code":"500"}`, got.Get("tags"),
		"every jaeger tag value is a string, whatever it was recorded as")
	require.Equal(t, "100ms", got.Get("minDuration"))
	require.Empty(t, got.Get("maxDuration"))
	require.Equal(t, "5", got.Get("limit"))
	require.Equal(t, "1786690800000000", got.Get("start"), "microseconds, as everything in that API is")
	require.Equal(t, "1786694400000000", got.Get("end"))
}

// Both backends have to search the same interval or two results lists cannot be
// compared, so neither server is left to pick its own default.
func TestASearchWindowIsAlwaysSaid(t *testing.T) {
	now := time.Unix(1786694400, 0).UTC()

	tempo := TraceQuery{}.tempoParams(now)
	require.Equal(t, "1786690800", tempo.Get("start"), "seconds, which is what Tempo reads")
	require.Equal(t, "1786694400", tempo.Get("end"))
	require.Equal(t, "20", tempo.Get("limit"))
	require.Equal(t, "{}", tempo.Get("q"))

	jaeger := TraceQuery{Service: "api"}.jaegerParams(now)
	require.Equal(t, "1786690800000000", jaeger.Get("start"))

	// A window with only a start runs up to now: "the last hour" said a minute
	// ago still means up to this minute.
	open := TraceQuery{Range: Range{Since: now.Add(-2 * time.Hour)}}.tempoParams(now)
	require.Equal(t, "1786687200", open.Get("start"))
	require.Equal(t, "1786694400", open.Get("end"))
}

// Jaeger indexes per service and refuses a search that names none. Saying so
// here is the difference between a form telling somebody what to fill in and a
// 400 quoting a parameter name back at them.
func TestJaegerRefusesASearchWithNoService(t *testing.T) {
	require.Error(t, TraceQuery{}.Validate(CollectorJaeger))
	require.NoError(t, TraceQuery{Service: "api"}.Validate(CollectorJaeger))
	require.NoError(t, TraceQuery{}.Validate(CollectorTempo),
		"tempo can be asked for everything in the window")

	backwards := TraceQuery{Service: "api", MinDuration: time.Second, MaxDuration: time.Millisecond}
	require.Error(t, backwards.Validate(CollectorTempo))
	require.Error(t, backwards.Validate(CollectorJaeger))
}

func TestTagsAreReadAsTyped(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []TraceTag
	}{
		{in: "", want: nil},
		{in: "  ", want: nil},
		{in: "error=true", want: []TraceTag{{Key: "error", Value: "true"}}},
		{
			in:   "http.status_code=500 error=true",
			want: []TraceTag{{Key: "http.status_code", Value: "500"}, {Key: "error", Value: "true"}},
		},
		{
			in:   `db.statement="select 1" k=v`,
			want: []TraceTag{{Key: "db.statement", Value: "select 1"}, {Key: "k", Value: "v"}},
		},
		{in: "k=", want: []TraceTag{{Key: "k", Value: ""}}},
	} {
		got, err := ParseTags(tt.in)
		require.NoError(t, err, tt.in)
		require.Equal(t, tt.want, got, tt.in)
	}

	for _, bad := range []string{"error", `k="v`, "=v"} {
		_, err := ParseTags(bad)
		require.Error(t, err, bad)
	}
}

// What was asked has to be showable and editable again, so the tags survive the
// round trip through the field they were typed into.
func TestTagsWriteBackTheWayTheyWereTyped(t *testing.T) {
	for _, spec := range []string{"", "error=true", "http.status_code=500 error=true", `db.statement="select 1"`} {
		tags, err := ParseTags(spec)
		require.NoError(t, err)
		require.Equal(t, spec, TagsSpec(tags))
	}
}

func TestATraceQueryKnowsWhenItNarrowsNothing(t *testing.T) {
	require.True(t, TraceQuery{}.IsZero())
	require.True(t, TraceQuery{Limit: 5, Range: Range{Spec: "1h"}}.IsZero(),
		"a window and a limit are not narrowing")
	require.False(t, TraceQuery{Service: "api"}.IsZero())
	require.False(t, TraceQuery{MinDuration: time.Second}.IsZero())
	require.False(t, TraceQuery{Tags: []TraceTag{{Key: "k", Value: "v"}}}.IsZero())
}

func TestATempoQueryIsEscapedIntoTheURL(t *testing.T) {
	got := TraceQuery{Service: "api"}.tempoParams(time.Unix(1786694400, 0))
	require.Equal(t, `q=%7Bresource.service.name%3D%22api%22%7D`,
		url.Values{"q": got["q"]}.Encode(), "the braces and quotes reach the server as the query")
}

// The window is one value said twice: once as the duration a request is
// bounded by, once as the words the screen shows for it.
func TestTheDefaultWindowReadsAsWhatItSearches(t *testing.T) {
	now := time.Unix(1786694400, 0).UTC()

	got := TraceQuery{}.Window(now)
	require.Equal(t, searchWindowSpec, got.Spec)
	require.Equal(t, now.Add(-searchWindow), got.Since)
	require.Equal(t, now, got.Until)

	spelled, err := ParseRange(searchWindowSpec, now)
	require.NoError(t, err)
	require.Equal(t, now.Add(-searchWindow), spelled.Since,
		"the spec shown and the window asked for have drifted apart")

	// A window somebody typed is kept as they wrote it.
	typed := TraceQuery{Range: Range{Spec: "6h..1h", Since: now.Add(-6 * time.Hour), Until: now.Add(-time.Hour)}}
	require.Equal(t, "6h..1h", typed.Window(now).Spec)
	require.Equal(t, now.Add(-time.Hour), typed.Window(now).Until)
}

// TestAskedIsWhatTheStoreWasActuallySent: Jaeger takes named parameters and
// compiles nothing, so the two APIs cannot be reported the same way.
func TestAskedIsWhatTheStoreWasActuallySent(t *testing.T) {
	q := TraceQuery{
		Service:     "checkout",
		Operation:   "POST /orders",
		Tags:        []TraceTag{{Key: "error", Value: "true"}},
		MinDuration: 500 * time.Millisecond,
	}

	require.Equal(t,
		`service=checkout operation=POST /orders tags={"error":"true"} minDuration=500ms`,
		q.Asked(CollectorJaeger))
	require.Equal(t, q.TraceQL(), q.Asked(CollectorTempo))
	require.Equal(t, q.TraceQL(), q.Asked(""), "unset is tempo, as it is everywhere else")

	require.Equal(t, "everything in the window", TraceQuery{}.Asked(CollectorJaeger),
		"a jaeger search that narrows nothing has no parameters to write out")
	require.Equal(t, "{}", TraceQuery{}.Asked(CollectorTempo))
}
