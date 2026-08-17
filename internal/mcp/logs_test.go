package mcp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

// entry is a line as VictoriaLogs answers with one.
type entry struct {
	at     string
	level  string
	pod    string
	msg    string
	traced bool
}

func (e entry) json() string {
	out := `{"_time":"` + e.at + `","_msg":"` + e.msg + `","level":"` + e.level +
		`","namespace":"prod","pod":"` + e.pod + `"`
	if e.traced {
		out += `,"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`
	}
	return out + "}\n"
}

func (e entry) time(t *testing.T) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, e.at)
	require.NoError(t, err)
	return at
}

// logStore answers the query endpoint the way a database does: the newest
// entries before the end asked for, newest first, and no more than the limit.
// A stub that ignored the bound would answer the same page forever, which is
// the one thing paging back must not do.
func logStore(t *testing.T, entries []entry) (endpoint string, asked *url.Values) {
	t.Helper()
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		end, err := time.Parse(time.RFC3339Nano, got.Get("end"))
		require.NoError(t, err)

		limit, err := strconv.Atoi(got.Get("limit"))
		require.NoError(t, err)

		var body strings.Builder
		for i := len(entries) - 1; i >= 0 && limit > 0; i-- {
			if entries[i].time(t).After(end) {
				continue
			}
			body.WriteString(entries[i].json())
			limit--
		}
		_, _ = w.Write([]byte(body.String()))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &got
}

func logsOf(t *testing.T, cfg config.Config, in logsInput) (string, logsOutput) {
	t.Helper()
	res, out, err := logsHandler(cfg)(t.Context(), nil, in)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Content, 1)
	return res.Content[0].(*sdk.TextContent).Text, out
}

// TestLogsHoistsWhatEveryLineSays: a label every line carries is the stream's
// own name for itself, and writing it on all fifty of them is fifty copies of
// one fact.
func TestLogsHoistsWhatEveryLineSays(t *testing.T) {
	srv, _ := logStore(t, []entry{
		{at: "2026-08-17T12:00:01Z", level: "info", pod: "api-1", msg: "started"},
		{at: "2026-08-17T12:00:02Z", level: "error", pod: "api-2", msg: "connect: connection refused"},
	})
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	text, out := logsOf(t, cfg, logsInput{Place: "prod"})
	require.Equal(t, map[string]string{"namespace": "prod"}, out.Common)
	require.Equal(t, []string{"pod"}, out.Varies)
	require.Equal(t, 2, out.Returned)
	require.Contains(t, text, "common: namespace=prod")
	require.Contains(t, text, "12:00:02.000 ERROR pod=api-2  connect: connection refused")
	require.NotContains(t, text, "namespace=prod pod=api-1", "a hoisted label is not written twice")
}

// TestLogsFoldsWhatRepeats: a line said forty times is one thing happening
// forty times, and forty copies of it in an answer are thirty-nine copies of a
// sentence the reader already has.
func TestLogsFoldsWhatRepeats(t *testing.T) {
	var entries []entry
	for i := range 4 {
		entries = append(entries, entry{
			at:    time.Date(2026, 8, 17, 12, 0, i, 0, time.UTC).Format(time.RFC3339Nano),
			level: "error", pod: "api-1", msg: "connect: connection refused",
		})
	}
	srv, _ := logStore(t, entries)
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	text, out := logsOf(t, cfg, logsInput{Place: "prod"})
	require.Equal(t, 4, out.Returned, "the count is of lines and not of rows")
	require.Contains(t, text, "×4")
	require.Equal(t, 1, strings.Count(text, "connect: connection refused"))
}

// TestLogsAsksTheDatabaseTheFilter: the filter is telescope's own everywhere,
// and where it compiles the database is the one that applies it.
func TestLogsAsksTheDatabaseTheFilter(t *testing.T) {
	srv, asked := logStore(t, []entry{
		{at: "2026-08-17T12:00:01Z", level: "info", pod: "api-1", msg: "started"},
		{at: "2026-08-17T12:00:02Z", level: "error", pod: "api-2", msg: "refused"},
	})
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	_, out := logsOf(t, cfg, logsInput{Place: "prod", Filter: "pod=api-2"})
	require.Contains(t, asked.Get("query"), "api-2")
	require.Equal(t, 1, out.Matched, "what the database did not answer, the view still filtered")
}

// TestLogsRefusesAQueryItCannotRead: a filter that does not parse would
// silently select nothing, and an agent told nothing matched would believe it.
func TestLogsRefusesAQueryItCannotRead(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	}, nil)

	_, _, err := logsHandler(cfg)(t.Context(), nil, logsInput{Place: "prod", Filter: "level>="})
	require.ErrorContains(t, err, "filter")
}

// TestLogsSaysTheWindowWasNotFinished: an answer cut by the limit and one that
// is all there was read the same, and only one of them means stop asking.
func TestLogsSaysTheWindowWasNotFinished(t *testing.T) {
	var entries []entry
	for i := range 6 {
		entries = append(entries, entry{
			at:    time.Date(2026, 8, 17, 12, 0, i, 0, time.UTC).Format(time.RFC3339Nano),
			level: "info", pod: "api-1", msg: "line " + string(rune('a'+i)),
		})
	}
	srv, _ := logStore(t, entries)
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	text, out := logsOf(t, cfg, logsInput{Place: "prod", Limit: 2})
	require.Equal(t, 2, out.Returned)
	require.False(t, out.Covered)
	require.Contains(t, out.Note, "the window holds more before them")
	require.Contains(t, text, "note: this is as many as was asked for")

	_, all := logsOf(t, cfg, logsInput{Place: "prod", Limit: 50})
	require.True(t, all.Covered, "a place that answered everything it had said so")
	require.Empty(t, all.Note)
}

// TestLogsRefusesWhatItCannotAskTwice: a free-form command is somebody else's
// line with nowhere to put a bound in it.
func TestLogsRefusesWhatItCannotAskTwice(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "tail", Type: "command", Args: "tail -f /var/log/syslog"},
	}, nil)

	_, _, err := logsHandler(cfg)(t.Context(), nil, logsInput{Place: "tail"})
	require.ErrorContains(t, err, "can only follow")
}
