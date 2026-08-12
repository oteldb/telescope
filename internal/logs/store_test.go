package logs

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/source"
)

func line(s string) source.Line { return source.Line{Data: []byte(s)} }

func TestStoreCap(t *testing.T) {
	s := NewStore(3)
	for i := range 5 {
		require.NotNil(t, s.Append(line("line "+strconv.Itoa(i))))
	}
	require.Equal(t, 3, s.Len())
	require.Equal(t, 2, s.Dropped())
	require.Equal(t, 2, s.Entries()[0].Seq)
	require.Equal(t, 4, s.Entries()[2].Seq)
}

func TestStoreHighlightsOnlyUnstructured(t *testing.T) {
	s := NewStore(10)

	plain := s.Append(line("connect failed after 3 retries"))
	require.False(t, plain.Record.Structured)
	require.NotEqual(t, "connect failed after 3 retries", plain.Text, "plain lines are colored")

	structured := s.Append(line(`{"level":"info","msg":"hi"}`))
	require.True(t, structured.Record.Structured)
	require.Contains(t, structured.Text, "hi")
}

// TestStoreFoldsMultilineRenders: pl renders a stacktrace or a multi-line
// error across several lines, but the log list draws one row per entry, so the
// remainder has to be counted rather than emitted.
func TestStoreFoldsMultilineRenders(t *testing.T) {
	s := NewStore(10)

	e := s.Append(line(`{"level":"error","msg":"boom","error":"wrapped:\n    a.go:1\n  - inner"}`))
	require.Contains(t, e.Text, "\n", "the full rendering keeps its lines")
	require.NotContains(t, e.Head, "\n", "the list gets a single line")
	require.Equal(t, 2, e.Extra, "and a count of what was folded")
	require.Contains(t, e.Head, "boom")
	require.True(t, strings.HasSuffix(e.Head, ansiReset), "no color leaks past the cut")

	single := s.Append(line(`{"level":"info","msg":"fine"}`))
	require.Equal(t, single.Text, single.Head)
	require.Zero(t, single.Extra)
}

func TestViewFilter(t *testing.T) {
	s := NewStore(100)
	s.Append(line(`{"level":"info","msg":"alpha"}`))
	s.Append(line(`{"level":"error","msg":"beta"}`))
	s.Append(line(`plain gamma`))

	for _, tt := range []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"all", Filter{}, []string{"alpha", "beta", "plain gamma"}},
		{"literal", Filter{Query: "eta"}, []string{"beta"}},
		{"case insensitive", Filter{Query: "ALPHA"}, []string{"alpha"}},
		{"regexp", Filter{Query: "/al|ga/"}, []string{"alpha", "plain gamma"}},
		{"a query that does not parse selects nothing", Filter{Query: "beta("}, nil},
		{"terms are and-ed", Filter{Query: "plain gamma"}, []string{"plain gamma"}},
		{"alternatives", Filter{Query: "alpha or beta"}, []string{"alpha", "beta"}},
		{"negation", Filter{Query: "-alpha"}, []string{"beta", "plain gamma"}},
		{"level compared", Filter{Query: "level>=error"}, []string{"beta"}},
		{"level", Filter{MinLevel: LevelError}, []string{"beta", "plain gamma"}},
		{"level and query", Filter{MinLevel: LevelError, Query: "a"}, []string{"beta", "plain gamma"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := NewView(tt.filter)
			require.Equal(t, tt.want, bodies(v.Entries(s)))
		})
	}
}

// TestViewIncremental checks that appends fold into an existing projection and
// that eviction from the store forces a rescan.
func TestViewIncremental(t *testing.T) {
	s := NewStore(2)
	v := NewView(Filter{Query: "keep"})

	s.Append(line("keep one"))
	require.Equal(t, []string{"keep one"}, bodies(v.Entries(s)))

	s.Append(line("drop two"))
	s.Append(line("keep three"))
	require.Equal(t, []string{"keep three"}, bodies(v.Entries(s)))

	// "keep one" is evicted by the cap, so the projection must be rebuilt.
	s.Append(line("keep four"))
	require.Equal(t, []string{"keep three", "keep four"}, bodies(v.Entries(s)))
}

func TestViewSetFilterResets(t *testing.T) {
	s := NewStore(10)
	s.Append(line("alpha"))
	s.Append(line("beta"))

	v := NewView(Filter{Query: "alpha"})
	require.Equal(t, []string{"alpha"}, bodies(v.Entries(s)))

	v.SetFilter(Filter{Query: "beta"})
	require.Equal(t, []string{"beta"}, bodies(v.Entries(s)))
}

func TestFilterDescribe(t *testing.T) {
	require.Equal(t, "no filter", Filter{}.Compile().Describe())
	require.Equal(t, "/a|b/", Filter{Query: "/a|b/"}.Compile().Describe())
	require.Equal(t, "level≥warn", Filter{MinLevel: LevelWarn}.Compile().Describe())
	// A query is described as it would be typed, not as it was.
	require.Equal(t, "alpha or beta", Filter{Query: "alpha OR   beta"}.Compile().Describe())
	require.Contains(t, Filter{Query: "a("}.Compile().Describe(), "bad query")
}

func bodies(entries []*Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Record.Body)
	}
	return out
}

// TestLevelFromLabels: a log database indexes the severity whether or not the
// message repeats it, and a Loki line is often only the message.
func TestLevelFromLabels(t *testing.T) {
	s := NewStore(10)
	e := s.Append(source.Line{
		Data:   []byte("read_request_line: client closed socket"),
		Labels: []source.Label{{Key: "detected_level", Value: "ERROR"}},
	})
	require.False(t, e.Record.Structured, "the line is still a bare sentence")
	require.True(t, e.Record.HasLevel)
	require.Equal(t, zapcore.ErrorLevel, e.Record.Level)

	// And what the line says about itself is not overruled by a label.
	own := s.Append(source.Line{
		Data:   []byte(`{"level":"info","msg":"hi"}`),
		Labels: []source.Label{{Key: "detected_level", Value: "ERROR"}},
	})
	require.Equal(t, zapcore.InfoLevel, own.Record.Level)
}

// TestLabelLevelFilters: a promoted level is a level, so the level filter
// reaches it.
func TestLabelLevelFilters(t *testing.T) {
	s := NewStore(10)
	s.Append(source.Line{Data: []byte("quiet"), Labels: []source.Label{{Key: "level", Value: "INFO"}}})
	s.Append(source.Line{Data: []byte("loud"), Labels: []source.Label{{Key: "level", Value: "ERROR"}}})

	v := NewView(Filter{MinLevel: LevelError})
	got := v.Entries(s)
	require.Len(t, got, 1)
	require.Equal(t, "loud", got[0].Record.Body)
}

// TestAWordMatchesWhatTheLineSays: for a structured line that is its values.
// A key is not something the line said, and it is also the only reading a log
// database can answer, so pushing a query down cannot change the answer.
func TestAWordMatchesWhatTheLineSays(t *testing.T) {
	s := NewStore(10)
	s.Append(line(`{"level":"info","msg":"connection reset","pod":"api-7"}`))
	s.Append(line(`plain connection reset`))

	for _, tt := range []struct {
		name  string
		query string
		want  int
	}{
		{"a value is matched", "reset", 2},
		{"a value of any field is matched", "api-7", 1},
		{"part of a value is matched", "api", 1},
		{"a key is not", "pod", 0},
		{"nor is the JSON around it", `":"`, 0},
		{"an unstructured line is all value", "plain", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Len(t, NewView(Filter{Query: tt.query}).Entries(s), tt.want)
		})
	}
}

// TestLabelsAreGreppable: the list has no room for twenty labels, so the only
// way to reach them is the filter.
func TestLabelsAreGreppable(t *testing.T) {
	s := NewStore(10)
	s.Append(source.Line{Data: []byte("artifact up-to-date"), Labels: []source.Label{
		{Key: "k8s_pod_name", Value: "source-controller-7f56dddc9d"},
	}})
	s.Append(line("artifact up-to-date"))

	for _, q := range []string{
		"source-controller",
		"k8s_pod_name=source-controller-7f56dddc9d",
		"k8s_pod_name~source",
	} {
		require.Len(t, NewView(Filter{Query: q}).Entries(s), 1, "query %q", q)
	}
}

// TestSourceTimeIsATime: a time reported beside the line is when it was
// written, not when it turned up here.
func TestSourceTimeIsATime(t *testing.T) {
	s := NewStore(10)
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.UTC)

	reported := s.Append(source.Line{Data: []byte("hello"), At: at})
	require.True(t, reported.HasTime)
	require.Equal(t, at, reported.At)

	bare := s.Append(line("hello"))
	require.False(t, bare.HasTime)
	require.False(t, bare.At.IsZero(), "it still has an arrival time to sort by")
}

// TestBandsFollowTheSecond: the lines of one second belong together, and the
// seam between two seconds is what a reader is actually looking for.
func TestBandsFollowTheSecond(t *testing.T) {
	s := NewStore(10)
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.UTC)

	first := s.Append(source.Line{Data: []byte("a"), At: at})
	same := s.Append(source.Line{Data: []byte("b"), At: at.Add(400 * time.Millisecond)})
	next := s.Append(source.Line{Data: []byte("c"), At: at.Add(time.Second)})
	later := s.Append(source.Line{Data: []byte("d"), At: at.Add(90 * time.Second)})

	require.Equal(t, first.Band, same.Band, "one second is one band")
	require.NotEqual(t, same.Band, next.Band, "the next second is the next band")
	require.NotEqual(t, next.Band, later.Band, "however long the gap")
}

// TestFieldReadsTheNameTheShipperKept: an OTLP attribute is written
// service.name and stored service_name by most of what stores it, and a filter
// naming either has to reach the same lines a query pushed down does.
func TestFieldReadsTheNameTheShipperKept(t *testing.T) {
	e := NewStore(10).Append(source.Line{
		Data: []byte(`{"msg":"hi","k8s_pod_name":"api-0"}`),
		Labels: []source.Label{
			{Key: "service_name", Value: "caddy.service"},
			{Key: "service.namespace", Value: "systemd"},
		},
	})

	for _, tt := range []struct{ key, want string }{
		{"service_name", "caddy.service"},
		{"service.name", "caddy.service"},
		{"k8s_pod_name", "api-0"},
		{"k8s.pod.name", "api-0"},
		// The exact spelling is still what it is, and still found first.
		{"service.namespace", "systemd"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			got, ok := e.Field(tt.key)
			require.True(t, ok)
			require.Equal(t, tt.want, got)
		})
	}

	// Only the one direction: what is stored dotted is not also underscored,
	// since nothing renames it that way.
	_, ok := e.Field("service_namespace")
	require.False(t, ok)
	_, ok = e.Field("nothing.here")
	require.False(t, ok)
}
