package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
	"github.com/oteldb/telescope/internal/trace"
)

// searchScreen opens the search over one store, as the start screen does.
func searchScreen(t *testing.T, at source.Endpoint) tea.Model {
	t.Helper()
	m := send(t, New(), size())
	m, _ = m.Update(openSearchMsg{at: at})
	return m
}

func tempoStore() source.Endpoint {
	return source.Endpoint{Name: "prod", URL: "https://tempo.example.com", Collector: source.CollectorTempo}
}

func jaegerStore() source.Endpoint {
	return source.Endpoint{Name: "prod", URL: "https://vt.example.com", Collector: source.CollectorJaeger}
}

// typeSearch types into the field the form is on.
func typeSearch(t *testing.T, m tea.Model, text string) tea.Model {
	t.Helper()
	for _, r := range text {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func searchOf(m tea.Model) searchModel { return m.(Model).search }

// TestSearchFormCompilesWhatWasTyped: the form is Jaeger's and the query is the
// store's, so what is typed has to arrive as the language that store reads.
func TestSearchFormCompilesWhatWasTyped(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m = typeSearch(t, m, "api")
	m = send(t, m, k("tab"))
	m = typeSearch(t, m, "GET /v1/orders")
	m = send(t, m, k("tab"))
	m = typeSearch(t, m, "http.status_code=500")

	q, err := searchOf(m).query()
	require.NoError(t, err)
	require.Equal(t,
		`{resource.service.name="api" && name="GET /v1/orders" && .http.status_code=500}`,
		q.TraceQL())
}

// A field that cannot be read is said where it was typed, and the results
// already on screen are left alone: a mistake in the next question is not an
// answer to the last one.
func TestSearchRefusesAFieldItCannotRead(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchLoadedMsg{results: []trace.Result{{TraceID: "abc", Name: "GET /"}}})

	// Back into the form, onto the min field, and something that is not a
	// duration.
	m = send(t, m, k("esc"), k("tab"), k("tab"), k("tab"))
	m = typeSearch(t, m, "fast")
	m = send(t, m, k("enter"))

	out := screen(t, m)
	require.Contains(t, out, "min: is not a duration")
	require.Contains(t, out, "GET /", "the answer to the last search is still there")
	require.False(t, searchOf(m).searching, "nothing was asked")
}

// Jaeger indexes per service and refuses to search without one. Saying so in
// the form is the difference between telling somebody what to fill in and
// quoting a parameter name back at them.
func TestSearchSaysWhatAJaegerStoreNeeds(t *testing.T) {
	m := searchScreen(t, jaegerStore())
	require.Contains(t, screen(t, m), "required here", "the field says so before it is skipped")

	m = send(t, m, k("enter"))
	require.Contains(t, screen(t, m), "searches by service")
	require.False(t, searchOf(m).searching)

	// Tempo can be asked for everything in the window, so the same empty form
	// is a search there.
	tempo := send(t, searchScreen(t, tempoStore()), k("enter"))
	require.True(t, searchOf(tempo).searching)
}

// The two stores count different things, so a row says which it is counting.
// "38 spans" and "3 matched" are different claims about the same trace.
func TestSearchRowsCountWhatTheStoreCounted(t *testing.T) {
	at := time.Date(2026, 8, 14, 12, 3, 41, 0, time.Local)

	tempo := searchScreen(t, tempoStore())
	tempo, _ = tempo.Update(searchLoadedMsg{results: []trace.Result{{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		Service: "gateway", Name: "POST /checkout",
		Start: at, Duration: 560 * time.Millisecond, Matched: 3,
	}}})
	out := screen(t, tempo)
	require.Contains(t, out, "gateway")
	require.Contains(t, out, "POST /checkout")
	require.Contains(t, out, "3 matched")
	require.NotContains(t, out, "spans", "tempo did not say how big the trace is")
	require.Contains(t, out, "4bf92f3577b3…", "the id is shortened to what tells rows apart")

	jaeger := searchScreen(t, jaegerStore())
	jaeger, _ = jaeger.Update(searchLoadedMsg{results: []trace.Result{{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		Service: "gateway", Name: "POST /checkout",
		Start: at, Duration: 560 * time.Millisecond, Spans: 38, Errors: 2,
	}}})
	out = screen(t, jaeger)
	require.Contains(t, out, "38 spans")
	require.Contains(t, out, "✗2")
}

// A search that matched nothing is an answer, and the question is the only
// thing left to doubt, so it is repeated back.
func TestSearchSaysWhatMatchedNothing(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m = typeSearch(t, m, "api")
	m = send(t, m, k("enter"))
	m, _ = m.Update(searchLoadedMsg{})

	out := screen(t, m)
	require.Contains(t, out, "no traces matched")
	require.Contains(t, out, `{resource.service.name="api"}`)
}

func TestSearchReportsWhatTheStoreRefusedWith(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchErrMsg{err: errSearch("400 Bad Request: invalid TraceQL")})
	require.Contains(t, screen(t, m), "invalid TraceQL")
}

// A Jaeger search answers with the traces themselves, so the gantt for a row
// has already been read and asking for it again would be the same document.
func TestOpeningAJaegerResultAsksForNothing(t *testing.T) {
	tree := trace.Build("4bf92f3577b34da6a3ce929d0e0e4736", []trace.Span{{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "a",
		Name: "POST /checkout", Service: "gateway",
		Start: time.Now(), Duration: 100 * time.Millisecond,
	}})

	m := searchScreen(t, jaegerStore())
	m, _ = m.Update(searchLoadedMsg{
		results: []trace.Result{trace.Summary(tree)},
		trees:   map[string]*trace.Tree{tree.ID: tree},
	})
	require.Equal(t, focusResults, searchOf(m).focus, "the answer takes the cursor")

	m = send(t, m, k("enter"))
	require.Equal(t, stateTrace, m.(Model).state)
	require.NotNil(t, m.(Model).trace.g, "the trace was drawn, not fetched")
	require.Contains(t, screen(t, m), "POST /checkout")

	// It is held by where it was read from, so leaving and opening it again
	// asks nothing either.
	held := m.(Model)
	_, cached := held.traces.get(jaegerStore().URL, tree.ID)
	require.True(t, cached)

	m = send(t, m, k("esc"))
	require.Equal(t, stateSearch, m.(Model).state, "esc goes back to the results")
}

// A trace picked out of a search has no log list under it, and dropping into an
// empty one would say less than saying so.
func TestNarrowingFromASearchedTraceSaysThereAreNoLogs(t *testing.T) {
	tree := trace.Build("t", []trace.Span{{
		TraceID: "t", SpanID: "a", Name: "GET /", Service: "api",
		Start: time.Now(), Duration: time.Millisecond,
	}})

	m := searchScreen(t, jaegerStore())
	m, _ = m.Update(searchLoadedMsg{
		results: []trace.Result{trace.Summary(tree)},
		trees:   map[string]*trace.Tree{"t": tree},
	})
	m = send(t, m, k("enter"))
	require.Equal(t, stateTrace, m.(Model).state)

	// f is not one of the keys that navigate, so the message it sends is
	// delivered by hand.
	m, cmd := m.Update(k("f"))
	m, _ = m.Update(cmd())
	require.Equal(t, stateTrace, m.(Model).state, "it stays where the reader is")
	require.Contains(t, screen(t, m), "no logs here")
}

// esc unwinds one layer at a time: out of the results into the form, and out of
// the form to where the search was opened from.
func TestSearchUnwindsWithEsc(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchLoadedMsg{results: []trace.Result{{TraceID: "abc"}}})
	require.Equal(t, focusResults, searchOf(m).focus)

	m = send(t, m, k("esc"))
	require.Equal(t, stateSearch, m.(Model).state)
	require.Equal(t, 0, searchOf(m).focus, "esc edits the search rather than leaving it")

	m = send(t, m, k("esc"))
	require.Equal(t, stateStart, m.(Model).state)
}

// What the store says it holds is offered under the field it belongs to, since
// a service is spelled the way the instrumentation spelled it and nobody
// remembers which.
func TestSearchOffersWhatTheStoreHolds(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchNamesMsg{field: fieldService, names: []string{"api", "gateway", "worker"}})

	out := screen(t, m)
	require.Contains(t, out, "service the store knows")
	require.Contains(t, out, "gateway")

	// Typing narrows them, and enter accepts the highlighted one rather than
	// searching for what was half typed.
	m = typeSearch(t, m, "gate")
	require.NotContains(t, screen(t, m), "worker")
	m = send(t, m, k("down"))
	m, _ = m.Update(k("enter"))
	require.Equal(t, "gateway", searchOf(m).inputs[fieldService].Value())
	require.False(t, searchOf(m).searching, "the suggestion was accepted, not searched for")
}

// The operations of a service go stale the moment the service does, since
// Jaeger indexes them per service.
func TestOperationSuggestionsBelongToOneService(t *testing.T) {
	m := searchScreen(t, jaegerStore())
	m, _ = m.Update(searchNamesMsg{field: fieldOperation, service: "api", names: []string{"GET /v1/orders"}})
	m = typeSearch(t, m, "api")
	m = send(t, m, k("tab"))
	require.Contains(t, screen(t, m), "operation the store knows")

	// A different service is a different list, and the old one is not offered
	// for it. The field's own example still stands there — it is a placeholder
	// and not something the store said.
	m = send(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	m = typeSearch(t, m, "2")
	m = send(t, m, k("tab"))
	require.NotContains(t, screen(t, m), "operation the store knows")
}

// TestSearchOpensFromTheStartScreen: alt+t on a saved place searches the store
// that place declares.
func TestSearchOpensFromTheStartScreen(t *testing.T) {
	withConfig(t, []config.Place{
		{
			Name: "prod", Type: "victorialogs", URL: "https://logs.example.com",
			Traces: config.TraceStore{URL: "https://vt.example.com/select/jaeger", Type: "jaeger"},
		},
		{Name: "local", Type: "docker", Container: "app"},
	}, nil)

	m := send(t, New(), size())
	require.Contains(t, screen(t, m), "alt+t search traces")

	m = send(t, m, altT())
	require.Equal(t, stateSearch, m.(Model).state)
	at := searchOf(m).at
	require.Equal(t, "https://vt.example.com/select/jaeger", at.URL)
	require.Equal(t, source.CollectorJaeger, at.Collector, "the config says which API answers")
	require.Contains(t, screen(t, m), "search traces")

	// A place that reads no traces has nothing to search.
	m = send(t, m, k("esc"))
	m = send(t, m, k("down"), k("down"), altT())
	require.Equal(t, stateStart, m.(Model).state)
	require.Contains(t, screen(t, m), "reads no traces")
}

// A group reads several stores and an id from one means nothing to another, so
// there is no one search to open.
func TestSearchRefusesAGroup(t *testing.T) {
	withConfig(t,
		[]config.Place{
			{Name: "eu", Type: "victorialogs", URL: "https://eu.example.com", Traces: config.TraceStore{URL: "https://eu-traces"}},
			{Name: "us", Type: "victorialogs", URL: "https://us.example.com", Traces: config.TraceStore{URL: "https://us-traces"}},
		},
		[]config.Group{{Name: "prod", Places: []string{"eu", "us"}}},
	)

	m := send(t, New(), size(), altT())
	require.Equal(t, stateStart, m.(Model).state)
	require.Contains(t, screen(t, m), "search the traces of one of its places")
}

// The key is offered only where something can answer it.
func TestSearchKeyIsNotOfferedWithoutATraceStore(t *testing.T) {
	withConfig(t, []config.Place{{Name: "local", Type: "docker", Container: "app"}}, nil)
	require.NotContains(t, screen(t, send(t, New(), size())), "alt+t")
}

// The store is named where the search is typed: where a trace comes from is
// half of what its id means.
func TestSearchNamesTheStoreItAsks(t *testing.T) {
	out := screen(t, searchScreen(t, jaegerStore()))
	require.Contains(t, out, "prod")
	require.Contains(t, out, "jaeger")

	require.Contains(t, screen(t, searchScreen(t, tempoStore())), "tempo")
}

// The screen fills the window whatever it is holding, as every other one does.
func TestSearchFillsTheWindow(t *testing.T) {
	m := searchScreen(t, tempoStore())
	require.Equal(t, 30, strings.Count(screen(t, m), "\n")+1)

	results := make([]trace.Result, 40)
	for i := range results {
		results[i] = trace.Result{TraceID: "id", Name: "GET /", Start: time.Now()}
	}
	m = send(t, m, k("enter"))
	m, _ = m.Update(searchLoadedMsg{results: results})
	out := screen(t, m)
	require.Equal(t, 30, strings.Count(out, "\n")+1)
	require.Contains(t, ansi.Strip(out), "40 traces")
}

func altT() tea.Msg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t"), Alt: true}
}
