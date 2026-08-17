package ui

import (
	"strconv"
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

// The line under the form says the window the results came out of. A blank
// range field searches the last hour, so saying "all" there would claim a
// history nobody looked through.
func TestASearchSaysTheWindowItLookedIn(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m = send(t, m, k("enter"))
	m, _ = m.Update(searchLoadedMsg{results: []trace.Result{{TraceID: "abc"}}})

	out := screen(t, m)
	require.Contains(t, out, "in last 1h")
	require.NotContains(t, out, "in all", "the last hour is not all of history")

	// A window somebody typed is shown the way they wrote it.
	typed := send(t, searchScreen(t, tempoStore()), k("tab"), k("tab"), k("tab"), k("tab"), k("tab"))
	typed = typeSearch(t, typed, "6h..1h")
	typed = send(t, typed, k("enter"))
	typed, _ = typed.Update(searchLoadedMsg{results: []trace.Result{{TraceID: "abc"}}})
	require.Contains(t, screen(t, typed), "6h..1h")
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
	m = send(t, m, k("ctrl+n"))
	m, _ = m.Update(k("enter"))
	require.Equal(t, "gateway", searchOf(m).inputs[fieldService].Value())
	require.False(t, searchOf(m).searching, "the suggestion was accepted, not searched for")
}

// A store worth searching always has something to offer under the first field,
// so a list that took the arrows would take them for the whole form. The list
// is stepped into and out of instead, and the arrows stay the way down the
// fields.
func TestArrowsWalkTheFormWhileTheStoreIsOffering(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchNamesMsg{field: fieldService, names: []string{"api", "gateway"}})
	require.Contains(t, screen(t, m), "gateway", "there is a list under the field")

	m = send(t, m, k("down"), k("down"))
	require.Equal(t, int(fieldTags), searchOf(m).focus, "the arrows walked the form")
	m = send(t, m, k("up"))
	require.Equal(t, int(fieldOperation), searchOf(m).focus)

	// ctrl+n hands the keys to the list, and from there the arrows walk it.
	m = send(t, m, k("up"), k("ctrl+n"), k("down"))
	require.Equal(t, int(fieldService), searchOf(m).focus, "the field is still the one being filled")
	require.Equal(t, 1, searchOf(m).sug)

	// esc gives them back without leaving the screen, and so does walking off
	// the top of the list.
	m = send(t, m, k("esc"))
	require.Equal(t, -1, searchOf(m).sug)
	require.Equal(t, stateSearch, m.(Model).state)

	m = send(t, m, k("ctrl+n"), k("up"))
	require.Equal(t, -1, searchOf(m).sug, "off the top is back into the field")
	m = send(t, m, k("down"))
	require.Equal(t, int(fieldOperation), searchOf(m).focus, "and the arrows walk the form again")
}

// A suggestion is in the list because it matched, and where it matched is what
// says why it is there — the same highlight the start screen's prompt draws.
func TestSearchShowsWhyASuggestionIsOffered(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchNamesMsg{field: fieldService, names: []string{"gateway", "gate-keeper"}})
	m = typeSearch(t, m, "gate")
	require.Contains(t, m.View(), styleMatch.Render("gate"))
}

// The tag field is a filter and reads as one: a key, what it is compared with,
// and the value are three things and not one string.
func TestSearchColorsTheTagsItSearchesBy(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m = send(t, m, k("down"), k("down"))
	m = typeSearch(t, m, "http.status_code=500")

	// Read off an unfocused row: the focused one carries the cursor wash, which
	// is laid over the colors rather than instead of them.
	m = send(t, m, k("down"))
	out := m.View()
	require.Contains(t, out, styleKey.Render("http.status_code"))
	require.Contains(t, out, styleDim.Render("="))
	require.Contains(t, out, styleFilter.Render("500"))
}

// A field that says something is lit and one that is still its own example is
// not, so what the search is narrowed by can be read without reading the
// placeholders to find out which of them are placeholders.
func TestAFilledFieldReadsAsFilled(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m = typeSearch(t, m, "api")
	m = send(t, m, k("down"))

	out := m.View()
	require.Contains(t, out, styleFilter.Render(padRight("service", labelWidth)))
	require.Contains(t, out, styleLabel.Render(padRight("max", labelWidth)))
}

// The number of traces to ask for is the number the screen can show, since the
// window is what decides how much of an answer is an answer.
func TestTheLimitIsWhatTheScreenHolds(t *testing.T) {
	m := searchScreen(t, tempoStore())
	q, err := searchOf(m).query()
	require.NoError(t, err)
	require.Equal(t, searchOf(m).defaultLimit(), q.Limit)
	require.Contains(t, screen(t, m), strconv.Itoa(q.Limit)+" — as many traces as this screen holds")

	// A number typed into the field is the number asked for.
	m = send(t, m, k("down"), k("down"), k("down"), k("down"), k("down"), k("down"))
	m = typeSearch(t, m, "5")
	q, err = searchOf(m).query()
	require.NoError(t, err)
	require.Equal(t, 5, q.Limit)
}

// The results are a table, so what a row says is read down a column rather than
// found again on every line.
func TestSearchResultsLineUp(t *testing.T) {
	at := time.Date(2026, 8, 17, 13, 12, 48, 0, time.Local)
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchLoadedMsg{results: []trace.Result{
		{TraceID: "aaaa", Service: "oteldb", Name: "engine.merge", Start: at, Matched: 1},
		{TraceID: "bbbb", Service: "oteldb", Name: "chstorage.metrics.timeseries.queryTimeseries", Start: at, Matched: 5},
	}})

	var cols []int
	for line := range strings.SplitSeq(screen(t, m), "\n") {
		if i := strings.Index(line, "matched"); i > 0 {
			cols = append(cols, ansi.StringWidth(line[:i]))
		}
	}
	require.Len(t, cols, 2)
	require.Equal(t, cols[0], cols[1], "the count is a column, whatever the name before it was")
}

// An id is carried rather than read, so it is cut where the row needs the
// width for what the trace was — and not where the row has width to spare.
func TestTheWholeIdIsShownWhereItFits(t *testing.T) {
	const id = "4bf92f3577b34da6a3ce929d0e0e4736"
	results := []trace.Result{{TraceID: id, Service: "gateway", Name: "POST /checkout", Start: time.Now(), Matched: 3}}

	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchLoadedMsg{results: results})
	require.Contains(t, screen(t, m), "4bf92f3577b3…", "eighty columns is not room for all of it")

	wide := send(t, New(), tea.WindowSizeMsg{Width: 160, Height: 30})
	wide, _ = wide.Update(openSearchMsg{at: tempoStore()})
	wide, _ = wide.Update(searchLoadedMsg{results: results})
	out := screen(t, wide)
	require.Contains(t, out, id)
	require.NotContains(t, out, "4bf92f3577b3…")
	require.Contains(t, out, "POST /checkout", "and the name did not pay for it")
}

// The list says how many it did not show. A store with four hundred operations
// must not read as one with six.
func TestSuggestionsSayWhatDidNotFit(t *testing.T) {
	names := make([]string, 80)
	for i := range names {
		names[i] = "op-" + strconv.Itoa(i)
	}
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchNamesMsg{field: fieldService, names: names})

	out := screen(t, m)
	shown := strings.Count(out, "op-")
	require.Greater(t, shown, 6, "the list takes the space the screen has")
	require.Contains(t, out, "… "+strconv.Itoa(80-shown)+" more")
}

// Every letter is a letter in a form: q searches for a service called queue
// here, and quits only from the results.
func TestSearchFieldsTakeEveryLetter(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m = typeSearch(t, m, "queue")
	require.Equal(t, "queue", searchOf(m).value(fieldService))
	require.Equal(t, stateSearch, m.(Model).state)
}

// A field is narrower than what can be typed into it, so it shows the end being
// typed rather than the start: a tag filter that has scrolled out from under
// the caret is a field nobody can edit.
func TestALongFilterScrollsUnderTheCaret(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m = send(t, m, k("down"), k("down"))
	m = typeSearch(t, m, strings.Repeat("key.of.a.tag=value ", 8)+"tail=x")

	out := screen(t, m)
	require.Contains(t, out, "tail=x", "the caret is on screen")
	require.Less(t, strings.Count(out, "key.of.a.tag"), 8, "and what does not fit is not")
}

// The operations of a service go stale the moment the service does, since
// Jaeger indexes them per service.
func TestOperationSuggestionsBelongToOneService(t *testing.T) {
	m := searchScreen(t, jaegerStore())
	m, _ = m.Update(searchNamesMsg{field: fieldOperation, of: "api", names: []string{"GET /v1/orders"}})
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

// The tag field holds several pairs, so what is offered is what the cursor is
// in the middle of: the keys while one is being named, and that key's values
// once it is.
func TestTagsCompleteTheirKeysAndThenTheirValues(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchNamesMsg{field: fieldTags, names: []string{"http.method", "http.status_code"}})
	m = send(t, m, k("tab"), k("tab"))
	m = typeSearch(t, m, "http.st")

	out := screen(t, m)
	require.Contains(t, out, "tag keys the store knows")
	require.Contains(t, out, "http.status_code")

	// Accepting a name leaves the comparison behind it, which is where the value
	// goes.
	m = send(t, m, k("ctrl+n"))
	m, _ = m.Update(k("enter"))
	require.Equal(t, "http.status_code=", searchOf(m).value(fieldTags))
	require.False(t, searchOf(m).searching, "the suggestion was accepted, not searched for")

	// And now the field is a value, so the values of that key are what is
	// offered.
	m, _ = m.Update(searchNamesMsg{field: fieldTags, of: "http.status_code", names: []string{"500", "503"}})
	out = screen(t, m)
	require.Contains(t, out, "http.status_code values the store knows")
	require.Contains(t, out, "503")

	m = send(t, m, k("ctrl+n"))
	m, _ = m.Update(k("enter"))
	require.Equal(t, "http.status_code=500", searchOf(m).value(fieldTags))

	// A second pair completes the same way, and finishing it leaves the first
	// alone.
	m = typeSearch(t, m, " http.me")
	m = send(t, m, k("ctrl+n"))
	m, _ = m.Update(k("enter"))
	require.Equal(t, "http.status_code=500 http.method=", searchOf(m).value(fieldTags))
}

// Values belong to the key they were listed for. Offering them under another is
// offering what some other tag has been.
func TestTagValuesAreNotOfferedUnderAnotherKey(t *testing.T) {
	m := searchScreen(t, tempoStore())
	m, _ = m.Update(searchNamesMsg{field: fieldTags, of: "http.method", names: []string{"POST"}})
	m = send(t, m, k("tab"), k("tab"))

	m = typeSearch(t, m, "http.method=")
	require.Contains(t, screen(t, m), "POST")

	m = typeSearch(t, m, "P db.system=")
	require.NotContains(t, screen(t, m), "POST", "those are what another tag has been")
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
