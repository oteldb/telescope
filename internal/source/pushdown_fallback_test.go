package source

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/query"
)

// vlogsRefusing serves a database that will not read the "*:filter" form, the
// way every VictoriaLogs older than 2026 answers it, and records what it was
// asked. The endpoint is forgotten afterwards, since the next test to bind this
// address is a different server.
func vlogsRefusing(t *testing.T, refuse func(q string) bool, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		asked = append(asked, q)
		if refuse(q) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`cannot parse query [` + q + `]: cannot read *substr* filter: ` +
				`compound token cannot be equal to ":"`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(func() {
		srv.Close()
		forgetPushdown(Endpoint{URL: srv.URL})
	})
	return srv, &asked
}

func filterExpr(t *testing.T, s string) query.Expr {
	t.Helper()
	e, err := query.Parse(s)
	require.NoError(t, err)
	return e
}

// TestPushdownRefusedIsAskedAgainWithoutIt: a filter the database will not parse
// costs the optimization, not the stream. The lines still arrive; the view is
// what filters them.
func TestPushdownRefusedIsAskedAgainWithoutIt(t *testing.T) {
	srv, asked := vlogsRefusing(t,
		func(q string) bool { return strings.Contains(q, "*:~") },
		vlogsEntry("2026-08-11T10:00:01Z", "reset by peer")+
			vlogsEntry("2026-08-11T10:00:02Z", "all quiet"),
	)

	cfg := vlogsConfig(srv.URL, false).WithFilter(filterExpr(t, "reset"))
	require.Equal(t, `(app:api) *:~"(?i)reset"`, cfg.vlogsQuery())

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	lines, err := drain(t, s)
	require.NoError(t, err, "a refused filter is not a refused stream")
	require.Len(t, lines, 2)

	require.Equal(t, []string{`(app:api) *:~"(?i)reset"`, "app:api"}, *asked,
		"the place alone is what is left to ask")
}

// TestPushdownRefusedIsRememberedPerEndpoint: the round trip that discovers a
// database cannot read a compiled filter is paid once, not on every filter.
func TestPushdownRefusedIsRememberedPerEndpoint(t *testing.T) {
	srv, asked := vlogsRefusing(t,
		func(q string) bool { return strings.Contains(q, "*:~") },
		vlogsEntry("2026-08-11T10:00:01Z", "reset by peer"),
	)

	base := vlogsConfig(srv.URL, false)
	s, err := Start(t.Context(), base.WithFilter(filterExpr(t, "reset")))
	require.NoError(t, err)
	_, err = drain(t, s)
	require.NoError(t, err)

	next := base.WithFilter(filterExpr(t, "quiet"))
	require.Equal(t, "app:api", next.vlogsQuery(), "there is nothing left to push")

	s, err = Start(t.Context(), next)
	require.NoError(t, err)
	_, err = drain(t, s)
	require.NoError(t, err)

	require.Equal(t, []string{`(app:api) *:~"(?i)reset"`, "app:api", "app:api"}, *asked)
}

// TestPushdownRefusedStopsTheRequery: once a database answers the place alone,
// two filters ask it the same thing, and the view has no reason to open the
// stream again.
func TestPushdownRefusedStopsTheRequery(t *testing.T) {
	srv, _ := vlogsRefusing(t,
		func(q string) bool { return strings.Contains(q, "*:~") },
		vlogsEntry("2026-08-11T10:00:01Z", "reset by peer"),
	)

	base := vlogsConfig(srv.URL, false)
	s, err := Start(t.Context(), base.WithFilter(filterExpr(t, "reset")))
	require.NoError(t, err)
	_, err = drain(t, s)
	require.NoError(t, err)

	require.Equal(t,
		base.WithFilter(filterExpr(t, "reset")).Pushed(),
		base.WithFilter(filterExpr(t, "quiet")).Pushed(),
	)
}

// TestPushdownKeepsARefusedPlace: a place the database will not read is the
// user's own mistake, and there is nothing narrower to fall back to. It is
// reported rather than widened into a tail of the whole database.
func TestPushdownKeepsARefusedPlace(t *testing.T) {
	srv, asked := vlogsRefusing(t, func(string) bool { return true }, "")

	s, err := Start(t.Context(), vlogsConfig(srv.URL, false))
	require.NoError(t, err)
	_, err = drain(t, s)
	require.ErrorContains(t, err, "400")
	require.Len(t, *asked, 1, "the place is all there was to ask")
}

// TestPushdownKeepsAnErrorThatIsNotTheQuery: a database that is unwell would
// answer a wider query no better, and retrying would hide why.
func TestPushdownKeepsAnErrorThatIsNotTheQuery(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("query"))
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(func() {
		srv.Close()
		forgetPushdown(Endpoint{URL: srv.URL})
	})

	cfg := vlogsConfig(srv.URL, false).WithFilter(filterExpr(t, "reset"))
	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	_, err = drain(t, s)
	require.ErrorContains(t, err, "500")
	require.Len(t, asked, 1)
	require.False(t, refusesPushdown(Endpoint{URL: srv.URL}))
}

// TestPushdownFallsBackWhileTailing: a place with no backfill discovers the
// refusal on the tail request instead, and falls back there.
func TestPushdownFallsBackWhileTailing(t *testing.T) {
	var asked []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		asked = append(asked, q)
		require.Equal(t, vlogsTailPath, r.URL.Path)
		if strings.Contains(q.Get("query"), "*:~") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("cannot parse query"))
			return
		}
		_, _ = w.Write([]byte(vlogsEntry("2026-08-11T10:00:01Z", "reset by peer")))
	}))
	t.Cleanup(func() {
		srv.Close()
		forgetPushdown(Endpoint{URL: srv.URL})
	})

	cfg := vlogsConfig(srv.URL, true).WithFilter(filterExpr(t, "reset"))
	cfg.Tail = 0

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	lines, err := drain(t, s)
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Len(t, asked, 2)
	require.Equal(t, "app:api", asked[1].Get("query"))
}
