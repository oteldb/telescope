package source

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// lokiResult renders a streams response the way Loki answers a log query.
func lokiResult(streams ...[][2]string) string {
	type stream struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	}
	body := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string   `json:"resultType"`
			Result     []stream `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	body.Data.ResultType = "streams"
	body.Data.Result = []stream{}
	for i, values := range streams {
		body.Data.Result = append(body.Data.Result, stream{
			Stream: map[string]string{"app": "api" + strconv.Itoa(i)},
			Values: values,
		})
	}
	out, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// at renders a nanosecond timestamp the way Loki writes one.
func at(sec int) string { return strconv.FormatInt(time.Unix(int64(sec), 0).UnixNano(), 10) }

func lokiConfig(url string, follow bool) Config {
	return Config{
		Collector: CollectorLoki,
		Endpoint:  Endpoint{Name: "prod", URL: url, Collector: CollectorLoki},
		Target:    `{app="api"}`,
		Tail:      100,
		Follow:    follow,
	}
}

// TestLokiBackfill: Loki answers a backward query newest first and one list per
// stream, and the view wants a single timeline, oldest first.
func TestLokiBackfill(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, lokiQueryRangePath, r.URL.Path)
		got = r.URL.Query()
		_, _ = w.Write([]byte(lokiResult(
			[][2]string{{at(3), "third"}, {at(1), "first"}},
			[][2]string{{at(4), "fourth"}, {at(2), "second"}},
		)))
	}))
	defer srv.Close()

	s, err := Start(t.Context(), lokiConfig(srv.URL, false))
	require.NoError(t, err)
	lines, err := drain(t, s)
	require.NoError(t, err)

	require.Equal(t, []string{"first", "second", "third", "fourth"}, lines)
	require.Equal(t, `{app="api"}`, got.Get("query"))
	require.Equal(t, "100", got.Get("limit"))
	require.Equal(t, "backward", got.Get("direction"))
	require.NotEmpty(t, got.Get("since"), "a line count is not a time range, and Loki needs one")
}

// TestLokiCarriesTheTimestamp: a Loki line is the application's own output, so
// the time is only in the response. Without it every backfilled line would be
// stamped with the moment it arrived.
func TestLokiCarriesTheTimestamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(lokiResult([][2]string{{at(1700000000), "hello"}})))
	}))
	defer srv.Close()

	s, err := Start(t.Context(), lokiConfig(srv.URL, false))
	require.NoError(t, err)
	line, ok := <-s.Lines()
	require.True(t, ok)
	require.Equal(t, "hello", string(line.Data))
	require.Equal(t, time.Unix(1700000000, 0), line.At)
}

// TestLokiFollowResumes: following is the same query against a moving start,
// since Loki's own tail is a websocket a datasource proxy will not upgrade.
func TestLokiFollowResumes(t *testing.T) {
	prev := lokiPoll
	lokiPoll = time.Millisecond
	t.Cleanup(func() { lokiPoll = prev })

	var (
		mu     sync.Mutex
		starts []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("direction") == "backward" {
			_, _ = w.Write([]byte(lokiResult([][2]string{{at(10), "history"}})))
			return
		}
		mu.Lock()
		starts = append(starts, q.Get("start"))
		n := len(starts)
		mu.Unlock()
		_, _ = w.Write([]byte(lokiResult([][2]string{{at(10 + n), "live " + strconv.Itoa(n)}})))
	}))
	defer srv.Close()

	s, err := Start(t.Context(), lokiConfig(srv.URL, true))
	require.NoError(t, err)
	require.Equal(t, "history", string((<-s.Lines()).Data))
	require.Equal(t, "live 1", string((<-s.Lines()).Data))
	require.Equal(t, "live 2", string((<-s.Lines()).Data))
	s.Close()
	_, err = drain(t, s)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, strconv.FormatInt(time.Unix(10, 0).UnixNano()+1, 10), starts[0],
		"the first poll starts just after the newest line already shown")
	require.Equal(t, strconv.FormatInt(time.Unix(11, 0).UnixNano()+1, 10), starts[1],
		"and each one after it moves on")
}

func TestLokiValidate(t *testing.T) {
	cfg := lokiConfig("https://logs.example.com", true)
	require.NoError(t, cfg.Validate())

	// A LogQL query without a stream selector is a parse error from the server,
	// so it is worth catching before the request.
	bare := cfg
	bare.Target = "error"
	require.ErrorContains(t, bare.Validate(), "stream selector")

	empty := cfg
	empty.Target = " "
	require.ErrorContains(t, empty.Validate(), "LogQL")

	require.Equal(t, `loki://prod · logql '{app="api"}'`, cfg.Title())
}

// TestLokiTenant: Loki spells its tenant differently from VictoriaLogs.
func TestLokiTenant(t *testing.T) {
	var h http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h = r.Header.Clone()
		_, _ = w.Write([]byte(lokiResult()))
	}))
	defer srv.Close()

	cfg := lokiConfig(srv.URL, false)
	cfg.Endpoint.Tenant = "team-a"
	cfg.Endpoint.Token = "s3cret"

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	_, err = drain(t, s)
	require.NoError(t, err)
	require.Equal(t, "team-a", h.Get("X-Scope-OrgID"))
	require.Equal(t, "Bearer s3cret", h.Get("Authorization"))
}

func TestParseLokiStreams(t *testing.T) {
	t.Run("structured metadata is skipped", func(t *testing.T) {
		body := `{"data":{"resultType":"streams","result":[{"stream":{"app":"api"},` +
			`"values":[["1700000000000000000","hi",{"trace_id":"abc"}]]}]}}`
		entries, err := parseLokiStreams([]byte(body))
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, "hi", string(entries[0].data))
	})

	t.Run("a metric query has no lines", func(t *testing.T) {
		body := `{"data":{"resultType":"matrix","result":[{"metric":{},"values":[[1700000000,"1"]]}]}}`
		_, err := parseLokiStreams([]byte(body))
		require.ErrorContains(t, err, "matrix")
	})

	t.Run("empty result", func(t *testing.T) {
		entries, err := parseLokiStreams([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
		require.NoError(t, err)
		require.Empty(t, entries)
	})

	t.Run("not a document at all", func(t *testing.T) {
		_, err := parseLokiStreams([]byte(`<html>502 Bad Gateway</html>`))
		require.ErrorContains(t, err, "decode")
	})

	t.Run("one bad entry does not lose the rest", func(t *testing.T) {
		body := `{"data":{"resultType":"streams","result":[{"values":[` +
			`["not-a-number","dropped"],["1700000000000000000","kept"]]}]}}`
		entries, err := parseLokiStreams([]byte(body))
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, "kept", string(entries[0].data))
	})
}

func FuzzParseLokiStreams(f *testing.F) {
	f.Add(lokiResult([][2]string{{at(1), "hi"}}))
	f.Add(`{"data":{"resultType":"streams","result":[{"values":[["x","y"]]}]}}`)
	f.Add(`{"data":{}}`)
	f.Add(`{`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, body string) {
		entries, err := parseLokiStreams([]byte(body))
		if err != nil {
			require.Empty(t, entries)
		}
	})
}

// TestLokiCarriesLabels: a Loki line is often nothing but a message, and what
// wrote it is only in the labels of the stream it was found in. Dropping them
// leaves a view of bare sentences with nothing to tell them apart.
func TestLokiCarriesLabels(t *testing.T) {
	const body = `{"status":"success","data":{"resultType":"streams","result":[
		{"stream":{"service_name":"api","detected_level":"ERROR","namespace":"prod"},
		 "values":[["1700000000000000000","boom",{"trace_id":"abc","detected_level":"WARN"}],
		           ["1700000001000000000","fine"]]}]}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	s, err := Start(t.Context(), lokiConfig(srv.URL, false))
	require.NoError(t, err)

	var got []Line
	for l := range s.Lines() {
		got = append(got, l)
	}
	require.NoError(t, <-s.Done())
	require.Len(t, got, 2)

	require.Equal(t, []Label{
		{Key: "detected_level", Value: "WARN"},
		{Key: "namespace", Value: "prod"},
		{Key: "service_name", Value: "api"},
		{Key: "trace_id", Value: "abc"},
	}, got[0].Labels, "the entry's own metadata wins over the stream's")

	require.Equal(t, []Label{
		{Key: "detected_level", Value: "ERROR"},
		{Key: "namespace", Value: "prod"},
		{Key: "service_name", Value: "api"},
	}, got[1].Labels, "an entry with no metadata of its own still knows its stream")
}
