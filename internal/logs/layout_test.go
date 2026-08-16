package logs

import (
	"strconv"
	"testing"

	"github.com/go-faster/pl"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// otlpLine is one line as a well-instrumented service sends it: a sentence that
// never changes, and everything that did change beside it as a label. The
// resource half of the label set is the same on every line by definition, the
// code half is the same for every line logged from that statement, and the five
// that are the news are the last of them.
func otlpLine(method, path string, status, ms int) source.Line {
	return source.Line{
		Data: []byte("got http request"),
		Labels: []source.Label{
			{Key: "detected_level", Value: "info"},
			{Key: "service_name", Value: "checkout-api"},
			{Key: "service_version", Value: "2.14.0"},
			{Key: "service_instance_id", Value: "3f1c9a2e"},
			{Key: "service_namespace", Value: "storefront"},
			{Key: "telemetry_sdk_name", Value: "opentelemetry"},
			{Key: "telemetry_sdk_language", Value: "go"},
			{Key: "telemetry_sdk_version", Value: "1.31.0"},
			{Key: "process_pid", Value: "1"},
			{Key: "process_runtime_name", Value: "go"},
			{Key: "process_runtime_version", Value: "go1.25.0"},
			{Key: "os_type", Value: "linux"},
			{Key: "os_description", Value: "Linux 6.11.0 #1 SMP PREEMPT_DYNAMIC storefront build 42"},
			{Key: "host_name", Value: "vm-checkout-01"},
			{Key: "host_arch", Value: "amd64"},
			{Key: "k8s_namespace_name", Value: "storefront"},
			{Key: "code_function_name", Value: "example.test/internal/httpx.(*Server).handleRequest"},
			{Key: "code_filepath", Value: "internal/httpx/server.go"},
			{Key: "code_lineno", Value: "212"},
			{Key: "content_type", Value: "application/json"},
			{Key: "trace_id", Value: "9f1d8c7b6a5e4d3c2b1a09f8e7d6c5b4"},
			{Key: "span_id", Value: "1a2b3c4d5e6f7081"},
			{Key: "method", Value: method},
			{Key: "path", Value: path},
			{Key: "status", Value: strconv.Itoa(status)},
			{Key: "duration_ms", Value: strconv.Itoa(ms)},
			{Key: "response_size", Value: "1284"},
		},
	}
}

func rowKeys(fields []RowField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Key)
	}
	return out
}

// TestARowIsWhatTellsTheLinesApart: the message is the same sentence three
// hundred times, so what the row has to show is the label set — minus
// everything in it that is also the same three hundred times.
func TestARowIsWhatTellsTheLinesApart(t *testing.T) {
	s := NewStore(10)
	e := s.Append(otlpLine("GET", "/cart/items", 200, 2))
	s.Append(otlpLine("POST", "/orders", 503, 30))

	require.Equal(t, []string{"method", "path", "status", "duration_ms"}, rowKeys(s.Row(e)))
}

// TestAConstantIsNotNews: which key that is cannot be listed in advance —
// `content_type` is nobody's idea of a resource attribute and says just as
// little when it never changes.
func TestAConstantIsNotNews(t *testing.T) {
	s := NewStore(10)
	e := s.Append(otlpLine("GET", "/cart/items", 200, 2))
	require.Contains(t, rowKeys(s.Row(e)), "content_type",
		"one line has agreed with nothing, so nothing is constant yet")

	s.Append(otlpLine("POST", "/cart/items", 503, 30))
	require.NotContains(t, rowKeys(s.Row(e)), "content_type")
	require.NotContains(t, rowKeys(s.Row(e)), "k8s_namespace_name",
		"one namespace being watched is not something to say on every row")
}

// TestAKeyThatVariesLaterIsShownOnTheRowsBeforeIt: the decision is made when the
// row is drawn, so a line arriving now changes what the rows above it show.
func TestAKeyThatVariesLaterIsShownOnTheRowsBeforeIt(t *testing.T) {
	s := NewStore(10)
	first := s.Append(otlpLine("GET", "/cart/items", 200, 2))
	s.Append(otlpLine("GET", "/cart/items", 200, 3))
	require.Equal(t, []string{"duration_ms"}, rowKeys(s.Row(first)),
		"two lines that agree about everything except one number")

	s.Append(otlpLine("POST", "/cart/items", 503, 30))
	require.Equal(t, []string{"method", "status", "duration_ms"}, rowKeys(s.Row(first)))
}

// TestAMergeKeepsWhatTellsItsStreamsApart: a label that never changes on either
// stream can still be the whole difference between them, which is what a merge
// is being read for.
func TestAMergeKeepsWhatTellsItsStreamsApart(t *testing.T) {
	s := NewStore(10)
	var last *Entry
	for _, name := range []string{"checkout", "warehouse"} {
		for i := range 2 {
			l := otlpLine("GET", "/cart/items", 200, i)
			l.Source = name
			l.Labels = append(l.Labels, source.Label{Key: "shard", Value: name})
			last = s.Append(l)
		}
	}
	require.Contains(t, rowKeys(s.Row(last)), "shard")
	require.NotContains(t, rowKeys(s.Row(last)), "content_type",
		"and drops what both of them agree about")
}

// TestAStreamThatJustJoinedShowsWhatItBrought: whether anything has agreed with
// a key yet is a question about the stream carrying it, so a merge's newcomer
// does not inherit what the streams beside it have settled.
func TestAStreamThatJustJoinedShowsWhatItBrought(t *testing.T) {
	s := NewStore(10)
	for i := range 3 {
		l := otlpLine("GET", "/cart/items", 200, i)
		l.Source = "checkout"
		s.Append(l)
	}
	joined := otlpLine("GET", "/cart/items", 200, 9)
	joined.Source = "warehouse"

	require.Contains(t, rowKeys(s.Row(s.Append(joined))), "content_type")
}

// TestARowIsOrderedByWhatIsRead: the verb and the route say what the line is
// about, the status says how it went, the timing says whether to care, and
// everything nobody recognizes keeps the order it arrived in behind all of it.
func TestARowIsOrderedByWhatIsRead(t *testing.T) {
	s := NewStore(10)
	scramble := func(method string, status int) source.Line {
		return source.Line{Data: []byte("served"), Labels: []source.Label{
			{Key: "response_size", Value: strconv.Itoa(status * 3)},
			{Key: "duration_ms", Value: strconv.Itoa(status)},
			{Key: "http_response_status_code", Value: strconv.Itoa(status)},
			{Key: "retries", Value: strconv.Itoa(status % 7)},
			{Key: "http_route", Value: "/orders/" + strconv.Itoa(status)},
			{Key: "http_request_method", Value: method},
		}}
	}
	e := s.Append(scramble("GET", 200))
	s.Append(scramble("POST", 503))

	require.Equal(t, []string{
		"http_request_method",
		"http_route",
		"http_response_status_code",
		"duration_ms",
		"response_size",
		"retries",
	}, rowKeys(s.Row(e)))
}

// TestWhatNeverReachesARow: a resource is the stream's name for itself, a code
// site is a question asked of one line, and an id that is different on every
// line sorts nothing and distinguishes nothing.
func TestWhatNeverReachesARow(t *testing.T) {
	s := NewStore(10)
	e := s.Append(source.Line{
		Data: []byte(`{"ts":"2026-08-11T15:16:36Z","level":"info","msg":"hi","trace_id":"abc","span_id":"de","user":"u1"}`),
		Labels: []source.Label{
			{Key: "service_name", Value: "checkout-api"},
			{Key: "host_name", Value: "vm-checkout-01"},
			{Key: "code_lineno", Value: "212"},
			{Key: "os_description", Value: "Linux 6.11.0"},
		},
	})
	require.Equal(t, []string{"user"}, rowKeys(e.Row))
}

// TestABlockIsNotAColumn: a stacktrace or a wrapped error is what pl renders
// under the message, where there is room for it.
func TestABlockIsNotAColumn(t *testing.T) {
	e := NewStore(10).Append(line(`{"msg":"boom","error":"wrapped:\n    a.go:1","attempt":3}`))
	require.Equal(t, []string{"attempt"}, rowKeys(e.Row))
	require.Positive(t, e.Extra, "and it is still counted")
}

// TestARowFieldNamesOnlyWhatWouldNotBeReadWithoutIt: a method looks like a
// method wherever it is written, and "42" could be anything.
func TestARowFieldNamesOnlyWhatWouldNotBeReadWithoutIt(t *testing.T) {
	known := RowField{Key: "method", Value: "GET"}
	require.Equal(t, ansiMethod+"GET"+ansiReset, known.Render())

	unknown := RowField{Key: "retries", Value: "3", Named: true}
	require.Equal(t, ansiKey+"retries"+ansiReset+"=3", unknown.Render())

	// Whatever a value turns out to be, it is drawn and not obeyed.
	nasty := RowField{Key: "note", Value: "a\x1b[2Jb", Named: true}
	require.Equal(t, ansiKey+"note"+ansiReset+"="+Escape("a\x1b[2Jb"), nasty.Render())
	require.Contains(t, nasty.Render(), `\e`)
}

// TestKlogAndPlainTextAreStillPlsToRender: a log whose meaning is in its
// message is what pl is for, and none of this touches it.
func TestKlogAndPlainTextAreStillPlsToRender(t *testing.T) {
	f := &pl.Formatter{Color: true, NoTime: true}
	s := NewStore(10)

	for _, raw := range []string{
		`I0811 15:16:36.123456       1 leaderelection.go:257] attempting to acquire leader lease storefront/checkout`,
		`E0811 15:16:37.000000       1 reflector.go:150] watch of *v1.Pod ended with: too old resource version`,
		`connect failed after 3 retries`,
	} {
		want, ok := f.Format([]byte(raw))
		require.True(t, ok)
		e := s.Append(line(raw))
		require.NotNil(t, e)
		if e.Text == Sanitize(Highlight(raw)) {
			// A line pl passed through verbatim is one telescope colors itself,
			// which it did before any of this too.
			continue
		}
		require.Equal(t, Sanitize(want), e.Text, raw)
		require.Equal(t, Sanitize(want), e.Head, raw)
		require.Empty(t, e.Row, raw)
	}
}
