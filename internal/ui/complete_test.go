package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/source"
)

// TestCompleteAt: what the cursor is sitting in is read off the raw text,
// because a query being typed is usually not one yet.
func TestCompleteAt(t *testing.T) {
	for _, tt := range []struct {
		name   string
		query  string
		cursor int
		want   completion
	}{
		{"a bare word is a name", "po", 2, completion{Prefix: "po", At: 0, OK: true}},
		{"an empty prompt wants a name", "", 0, completion{OK: true}},
		{"after an = the value is wanted", "pod=api", 7, completion{Key: "pod", Prefix: "api", At: 4, OK: true}},
		{"an = with nothing after it too", "pod=", 4, completion{Key: "pod", At: 4, OK: true}},
		{"only the term the cursor is in", "err pod=a", 9, completion{Key: "pod", Prefix: "a", At: 8, OK: true}},
		{"the cursor may sit in the key", "err pod=api", 7, completion{Prefix: "pod", At: 4, OK: true}},
		{"a two-character comparison", "level>=wa", 9, completion{Key: "level", Prefix: "wa", At: 7, OK: true}},
		{"a negated term completes the same", "-pod=api", 8, completion{Key: "pod", Prefix: "api", At: 5, OK: true}},
		{"a bracket starts a term", "(pod=a", 6, completion{Key: "pod", Prefix: "a", At: 5, OK: true}},
		{"a regexp is a pattern, not a value", "pod=/api", 8, completion{}},
		{"a quote starts something else", `pod="api`, 8, completion{Prefix: "api", At: 5, OK: true}},
		{"an operator with no key is nothing", "=api", 4, completion{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, completeAt(tt.query, tt.cursor))
		})
	}
}

// TestCompletionApply: a name is inserted with its comparison, since a name on
// its own is not a term, and a value that could not be typed bare is quoted.
func TestCompletionApply(t *testing.T) {
	for _, tt := range []struct {
		name  string
		query string
		at    int
		value string
		isKey bool
		want  string
		pos   int
	}{
		{"a name brings its =", "po", 2, "pod", true, "pod=", 4},
		{"a value replaces what was typed", "pod=ap", 6, "api-7", false, "pod=api-7", 9},
		{"and keeps what follows it", "pod=ap err", 6, "api-7", false, "pod=api-7 err", 9},
		{"a value that needs quoting gets it", "pod=w", 5, "web (old)", false, `pod="web (old)"`, 15},
	} {
		t.Run(tt.name, func(t *testing.T) {
			at := completeAt(tt.query, tt.at)
			got, pos := at.apply(tt.query, tt.value, tt.isKey)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.pos, pos)
		})
	}
}

// completingModel is a log view holding two structured lines, with the prompt
// open.
func completingModel(t *testing.T) tea.Model {
	t.Helper()
	return send(t, logsModel(t,
		`{"level":"info","msg":"started","pod":"api-7","zone":"eu"}`,
		`{"level":"error","msg":"exploded","pod":"api-8","zone":"us"}`,
	), k("/"))
}

// offered is what the prompt is currently suggesting.
func offered(m tea.Model) []complete.Candidate { return m.(Model).logs.suggestions() }

// suggested is the same, as the values would be typed.
func suggested(m tea.Model) []string {
	var out []string
	for _, c := range offered(m) {
		out = append(out, c.Value)
	}
	return out
}

// TestCompleteFromTheLinesRead: what is on screen is what the stream is actually
// saying, and it needs no database to be offered back.
func TestCompleteFromTheLinesRead(t *testing.T) {
	m := typed(t, completingModel(t), "po")
	require.Equal(t, []string{"pod"}, suggested(m))

	m = send(t, m, k("tab"))
	require.Equal(t, "pod=", m.(Model).logs.search.Value(), "a name is inserted with its comparison")
	require.Equal(t, []string{"api-7", "api-8"}, suggested(m), "and the values under it follow")

	m = send(t, m, k("down"), k("tab"))
	require.Equal(t, "pod=api-8", m.(Model).logs.search.Value())

	m = send(t, m, k("enter"))
	lg := m.(Model).logs
	require.Equal(t, "pod=api-8", lg.view.Filter().Query)
	require.Len(t, lg.view.Entries(lg.store), 1, "and it filters by what was completed")
}

// TestCompleteOffersNothingForAnEmptyPrompt: "/" is pressed to type a grep far
// more often than a field name, and a list taking rows off the view every time
// would be in the way.
func TestCompleteOffersNothingForAnEmptyPrompt(t *testing.T) {
	m := completingModel(t)
	require.Empty(t, suggested(m))

	before := m.(Model).logs.bodyHeight()
	m = typed(t, m, "po")
	require.NotEmpty(t, suggested(m))
	require.Less(t, m.(Model).logs.bodyHeight(), before,
		"the rows come out of the list, so the frame stays where it is")
}

// TestCompleteALevel: the levels a stream has actually logged are values like
// any other.
func TestCompleteALevel(t *testing.T) {
	require.Equal(t, []string{"error", "info"}, suggested(typed(t, completingModel(t), "level=")))
}

// TestCompleteSkipsWhatCannotBeCompleted: a trace id is a different string on
// every line, so remembering them would fill the index and complete nothing.
// The name is still worth offering.
func TestCompleteSkipsWhatCannotBeCompleted(t *testing.T) {
	m := send(t, logsModel(t,
		`{"msg":"a","trace_id":"0af7651916cd43dd8448eb211c80319c"}`,
		`{"msg":"b","trace_id":"1bf7651916cd43dd8448eb211c80319c"}`,
	), k("/"))

	require.Equal(t, []string{"trace_id"}, suggested(typed(t, m, "trace")))
	require.Empty(t, suggested(typed(t, m, "trace_id=")))
}

// TestCompleteFromTheDatabase: a database knows what it holds before a line
// carrying it has arrived, and what it alone knows is marked and offered behind
// what has been seen.
func TestCompleteFromTheDatabase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"value":"pod"},{"value":"region"}]}`))
	}))
	defer srv.Close()

	cfg := source.Config{
		Collector: source.CollectorVictoriaLogs,
		Endpoint:  source.Endpoint{Name: "prod", URL: srv.URL},
	}
	m := send(t, New(), size(), connectMsg{cfg: cfg})
	m = send(t, m, linesMsg{
		lines:  []source.Line{{Data: []byte(`{"msg":"started","pod":"api-7"}`)}},
		closed: true,
	})
	m = send(t, m, k("/"))
	require.True(t, m.(Model).logs.asked[""], "opening the prompt asks, since asking is a round trip")

	m = send(t, m, fetchFields(cfg, "")())
	m = typed(t, m, "o")

	require.Equal(t, []string{"pod", "region"}, suggested(m))
	require.Empty(t, offered(m)[0].Detail, "one of them is on screen")
	require.Equal(t, "not seen yet", offered(m)[1].Detail, "the other only on record")
}

// TestCompleteAsksACommandNothing: a process writing to a pipe has nothing to
// list, so nothing is asked of it.
func TestCompleteAsksACommandNothing(t *testing.T) {
	m := completingModel(t)
	require.False(t, m.(Model).logs.asked[""])
}

// TestCompleteDropsAnAnswerForAnotherPlace: a listing is a round trip, and the
// view may be reading somewhere else by the time it lands.
func TestCompleteDropsAnAnswerForAnotherPlace(t *testing.T) {
	m := completingModel(t)
	m = send(t, m, fieldsMsg{cfg: "somewhere else", values: []string{"region"}})
	require.Empty(t, suggested(typed(t, m, "reg")))
}
