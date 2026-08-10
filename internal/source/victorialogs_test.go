package source

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-faster/jx"
	"github.com/stretchr/testify/require"
)

// vlogsEntry renders one JSON line the way VictoriaLogs answers.
func vlogsEntry(ts, msg string) string {
	return `{"_time":"` + ts + `","_stream":"{app=\"api\"}","_msg":"` + msg + `"}` + "\n"
}

// drain reads a stream into its lines and its exit error.
func drain(t *testing.T, s *Stream) ([]string, error) {
	t.Helper()
	var out []string
	for line := range s.Lines() {
		out = append(out, string(line.Data))
	}
	select {
	case err := <-s.Done():
		return out, err
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not finish")
		return nil, nil
	}
}

func vlogsConfig(url string, follow bool) Config {
	return Config{
		Collector: CollectorVictoriaLogs,
		Endpoint:  Endpoint{Name: "prod", URL: url},
		Target:    `app:api`,
		Tail:      100,
		Follow:    follow,
	}
}

// TestVictoriaLogsBackfill: the query endpoint answers newest first, and the
// view wants oldest first.
func TestVictoriaLogsBackfill(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, vlogsQueryPath, r.URL.Path)
		got = r.URL.Query()
		_, _ = w.Write([]byte(
			vlogsEntry("2026-08-11T10:00:03Z", "third") +
				vlogsEntry("2026-08-11T10:00:02Z", "second") +
				vlogsEntry("2026-08-11T10:00:01Z", "first"),
		))
	}))
	defer srv.Close()

	s, err := Start(t.Context(), vlogsConfig(srv.URL, false))
	require.NoError(t, err)
	lines, err := drain(t, s)
	require.NoError(t, err)

	require.Len(t, lines, 3)
	require.Contains(t, lines[0], "first")
	require.Contains(t, lines[2], "third")
	require.Equal(t, "app:api", got.Get("query"))
	require.Equal(t, "100", got.Get("limit"))
}

// TestVictoriaLogsTailSkipsWhatWasShown: live tailing starts before the
// backfill ended, so the overlap must not be printed twice.
func TestVictoriaLogsTailSkipsWhatWasShown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case vlogsQueryPath:
			_, _ = w.Write([]byte(vlogsEntry("2026-08-11T10:00:02Z", "second")))
		case vlogsTailPath:
			require.NotEmpty(t, r.URL.Query().Get("start_offset"))
			_, _ = w.Write([]byte(
				vlogsEntry("2026-08-11T10:00:01Z", "first") +
					vlogsEntry("2026-08-11T10:00:02Z", "second") +
					vlogsEntry("2026-08-11T10:00:03Z", "third"),
			))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	s, err := Start(t.Context(), vlogsConfig(srv.URL, true))
	require.NoError(t, err)
	lines, err := drain(t, s)
	require.NoError(t, err)

	require.Len(t, lines, 2)
	require.Contains(t, lines[0], "second")
	require.Contains(t, lines[1], "third")
}

// TestVictoriaLogsAuth: the endpoint's token and tenant are what a Grafana
// datasource proxy in front of the database checks.
func TestVictoriaLogsAuth(t *testing.T) {
	var h http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h = r.Header.Clone()
	}))
	defer srv.Close()

	cfg := vlogsConfig(srv.URL, false)
	cfg.Endpoint.Token = "s3cret"
	cfg.Endpoint.Tenant = "7:9"
	cfg.Endpoint.Header = map[string]string{"X-Scope": "logs"}

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	_, err = drain(t, s)
	require.NoError(t, err)

	require.Equal(t, "Bearer s3cret", h.Get("Authorization"))
	require.Equal(t, "7", h.Get("AccountID"))
	require.Equal(t, "9", h.Get("ProjectID"))
	require.Equal(t, "logs", h.Get("X-Scope"))
}

// TestVictoriaLogsTenantAccountOnly: a tenant may name the account alone.
func TestVictoriaLogsTenantAccountOnly(t *testing.T) {
	var h http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h = r.Header.Clone()
	}))
	defer srv.Close()

	cfg := vlogsConfig(srv.URL, false)
	cfg.Endpoint.Tenant = "7"

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	_, err = drain(t, s)
	require.NoError(t, err)
	require.Equal(t, "7", h.Get("AccountID"))
	require.Empty(t, h.Get("ProjectID"))
}

// TestVictoriaLogsError: what the endpoint said is what the user needs to see,
// since a proxy answers for itself as readily as the database does.
func TestVictoriaLogsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("{\n  \"message\": \"Unauthorized\"\n}"))
	}))
	defer srv.Close()

	s, err := Start(t.Context(), vlogsConfig(srv.URL, false))
	require.NoError(t, err)
	_, err = drain(t, s)
	require.ErrorContains(t, err, "401")
	require.ErrorContains(t, err, `{ "message": "Unauthorized" }`)
}

// TestVictoriaLogsClose: closing the view ends the stream without an error, the
// same as quitting a followed command.
func TestVictoriaLogsClose(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == vlogsTailPath {
			_, _ = w.Write([]byte(vlogsEntry("2026-08-11T10:00:09Z", "live")))
			w.(http.Flusher).Flush()
			<-release
		}
	}))
	defer srv.Close()
	defer close(release)

	s, err := Start(t.Context(), vlogsConfig(srv.URL, true))
	require.NoError(t, err)
	require.Contains(t, string((<-s.Lines()).Data), "live")

	s.Close()
	_, err = drain(t, s)
	require.NoError(t, err)
}

func TestVictoriaLogsValidate(t *testing.T) {
	cfg := vlogsConfig("https://logs.example.com", true)
	require.NoError(t, cfg.Validate())

	// LogsQL has a match-all, so nothing typed is a tail of the whole
	// database rather than a source that cannot open.
	noQuery := cfg
	noQuery.Target = "  "
	require.NoError(t, noQuery.Validate())
	require.Equal(t, "logsql '*'", noQuery.Command())

	noEndpoint := cfg
	noEndpoint.Endpoint = Endpoint{}
	require.ErrorContains(t, noEndpoint.Validate(), "endpoint")

	// The transport is a property of a command, and there is no command here.
	overSSH := cfg
	overSSH.Transport, overSSH.Host = TransportSSH, ""
	require.NoError(t, overSSH.Validate())
	require.Nil(t, overSSH.Argv())
}

// TestVictoriaLogsTitle: a title is shown to the user and copied into bug
// reports, so it must name the endpoint and never the token.
func TestVictoriaLogsTitle(t *testing.T) {
	cfg := vlogsConfig("https://grafana.example.com/api/datasources/proxy/uid/abc", false)
	cfg.Endpoint.Token = "s3cret"
	require.Equal(t, "victorialogs://prod · logsql app:api", cfg.Title())
	require.NotContains(t, cfg.Title(), "s3cret")

	cfg.Endpoint.Name = ""
	require.Contains(t, cfg.Title(), "grafana.example.com")
}

// TestVictoriaLogsNormalize: the underscored keys are VictoriaLogs' envelope,
// not the application's fields, and a viewer that shows them as fields has no
// message column left.
func TestVictoriaLogsNormalize(t *testing.T) {
	for _, tt := range []struct {
		name  string
		entry string
		want  string
	}{
		{
			"envelope",
			`{"_time":"2026-08-11T10:00:00Z","_msg":"boom","level":"error"}`,
			`{"time":"2026-08-11T10:00:00Z","msg":"boom","level":"error"}`,
		},
		{
			"the application's own keys win",
			`{"_time":"2026-08-11T10:00:00Z","_msg":"envelope","msg":"real"}`,
			`{"time":"2026-08-11T10:00:00Z","_msg":"envelope","msg":"real"}`,
		},
		{
			"other fields are untouched",
			`{"_msg":"hi","_stream":"{app=\"api\"}","_stream_id":"ab","app":"api"}`,
			`{"msg":"hi","_stream":"{app=\"api\"}","_stream_id":"ab","app":"api"}`,
		},
		{"nothing to rename", `{"msg":"hi"}`, `{"msg":"hi"}`},
		{"not an object", `not json`, `not json`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, string(vlogsNormalize([]byte(tt.entry))))
		})
	}

	// The scanner reuses its buffer, so a retained entry must be a copy.
	buf := []byte(`{"msg":"hi"}`)
	got := vlogsNormalize(buf)
	copy(buf, `{"msg":"XX"}`)
	require.Equal(t, `{"msg":"hi"}`, string(got))
}

func FuzzVictoriaLogsNormalize(f *testing.F) {
	for _, s := range []string{
		vlogsEntry("2026-08-11T10:00:01Z", "hi"), `{"msg":"hi","_msg":"x"}`, `{}`, `[]`, `{`, ``,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, entry string) {
		got := vlogsNormalize([]byte(entry))
		if !jx.Valid([]byte(entry)) {
			return
		}
		require.True(t, jx.Valid(got), "valid input must stay valid")
	})
}

func TestVictoriaLogsTime(t *testing.T) {
	for _, tt := range []struct {
		name  string
		entry string
		want  string
	}{
		{"entry", vlogsEntry("2026-08-11T10:00:01.5Z", "hi"), "2026-08-11T10:00:01.5Z"},
		{"no time", `{"_msg":"hi"}`, ""},
		{"not a time", `{"_time":"soon"}`, ""},
		{"not an object", `[]`, ""},
		{"truncated", `{"_time":`, ""},
		{"empty", ``, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := vlogsTime([]byte(tt.entry))
			require.Equal(t, tt.want != "", ok)
			if tt.want != "" {
				require.Equal(t, tt.want, got.Format(time.RFC3339Nano))
			}
		})
	}
}

func FuzzVictoriaLogsTime(f *testing.F) {
	for _, s := range []string{
		vlogsEntry("2026-08-11T10:00:01Z", "hi"), `{"_time":"soon"}`, `{"_time":1}`, `[]`, `{`, ``,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, entry string) {
		if ts, ok := vlogsTime([]byte(entry)); ok {
			require.False(t, ts.IsZero())
		}
	})
}

// TestEndpointAPI: the URL is a base that API paths hang off, which is what
// makes a Grafana datasource proxy usable without special-casing it.
func TestEndpointAPI(t *testing.T) {
	for _, tt := range []struct {
		name string
		url  string
		want string
	}{
		{"database", "https://logs.example.com", "https://logs.example.com/select/logsql/query?query=x"},
		{
			"grafana proxy",
			"https://grafana.example.com/api/datasources/proxy/uid/abc",
			"https://grafana.example.com/api/datasources/proxy/uid/abc/select/logsql/query?query=x",
		},
		{"trailing slash", "https://logs.example.com/", "https://logs.example.com/select/logsql/query?query=x"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Endpoint{URL: tt.url}.api(vlogsQueryPath, url.Values{"query": {"x"}})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	_, err := Endpoint{URL: "logs.example.com"}.api(vlogsQueryPath, nil)
	require.ErrorContains(t, err, "scheme")
}

func TestEndpointLabel(t *testing.T) {
	require.Equal(t, "prod", Endpoint{Name: "prod", URL: "https://logs.example.com"}.Label())
	require.Equal(t, "logs.example.com", Endpoint{URL: "https://logs.example.com/x"}.Label())
}
