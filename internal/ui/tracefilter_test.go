package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-faster/jx"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/trace"
)

// filtered opens the trace, types a filter and applies it.
func filtered(t *testing.T, tr *trace.Tree, text string) tea.Model {
	t.Helper()
	m := traceModelOf(t, tr)
	m, _ = m.Update(k("/"))
	m = typeSearch(t, m, text)
	m, _ = m.Update(k("enter"))
	return m
}

// The chart is narrowed by the same language the log list takes, over what a
// span says rather than only over who ran it.
func TestTheChartNarrowsToWhatAFilterSelects(t *testing.T) {
	m := filtered(t, checkout(), "postgres")

	out := screen(t, m)
	require.Contains(t, out, "postgres INSERT orders")
	require.NotContains(t, out, "verify token", "nothing selected it")
	require.Contains(t, out, "3 of 8 spans match")
	require.Contains(t, out, "postgres", "and the filter in force is said under the chart")
}

// A span above a match is kept: dropping it would reparent what it called onto
// something that never called it, which is the rule the service filter follows.
func TestAFilterKeepsTheSpansThatHoldUpAMatch(t *testing.T) {
	tr := trace.Build("t", []trace.Span{
		span("root", "", "gateway", "GET /", 0, 100*ms),
		span("mid", "root", "sidecar", "proxy", 10*ms, 80*ms),
		span("leaf", "mid", "db", "select", 20*ms, 30*ms),
	})
	m := filtered(t, tr, "service.name=db")

	out := screen(t, m)
	require.Contains(t, out, "db select")
	require.Contains(t, out, "sidecar proxy", "the span that called it is still there")
	require.Contains(t, out, "gateway GET /")
	require.Contains(t, out, "1 of 3 spans match", "and the header says what was selected")

	g := m.(Model).trace.g
	require.True(t, g.scaffold(g.rows[0]), "what only holds up a match is drawn as structure")
	require.False(t, g.scaffold(g.rows[2]))
}

// Somebody typed a question, so "nothing matches" is the answer. Showing the
// trace back would claim the spans on screen are the ones asked for.
func TestAFilterThatSelectsNothingSaysSo(t *testing.T) {
	m := filtered(t, checkout(), "service.name=nowhere")

	out := screen(t, m)
	require.Contains(t, out, "no spans match this filter")
	require.Contains(t, out, "0 of 8 spans match")
	require.NotContains(t, out, "POST /checkout")
}

// The prompt is finished by what the spans in hand say: the whole trace is on
// screen, so there is nothing to ask anybody.
func TestTheFilterPromptCompletesWhatTheSpansSay(t *testing.T) {
	s := span("db", "", "orders-db", "INSERT orders", 0, 10*ms)
	s.Attrs = []logs.Field{
		{Key: "db.system", Value: jx.Raw(`"postgresql"`)},
		{Key: "db.statement", Value: jx.Raw(`"INSERT INTO orders"`)},
	}
	m := traceModelOf(t, trace.Build("t", []trace.Span{s}))

	m, _ = m.Update(k("/"))
	require.NotContains(t, screen(t, m), "db.system", "an empty prompt is a word about to be typed")

	m = typeSearch(t, m, "db.sy")
	require.Contains(t, screen(t, m), "db.system")

	// Accepting a name leaves the comparison behind it, and the values under it
	// are what is offered next.
	m, _ = m.Update(k("tab"))
	require.Equal(t, "db.system=", m.(Model).trace.filter.Value())
	require.Contains(t, screen(t, m), "postgresql")

	m, _ = m.Update(k("tab"))
	require.Equal(t, "db.system=postgresql", m.(Model).trace.filter.Value())

	m, _ = m.Update(k("enter"))
	require.Contains(t, screen(t, m), "1 of 1 spans match")
}

// A query that does not parse leaves the chart as it was, with the prompt still
// open on what was typed, which is where it can be fixed.
func TestAFilterThatIsNotAQueryStaysInThePrompt(t *testing.T) {
	m := traceModelOf(t, checkout())
	m, _ = m.Update(k("/"))
	m = typeSearch(t, m, "(postgres")
	m, _ = m.Update(k("enter"))

	require.Equal(t, traceFilter, m.(Model).trace.mode)
	require.Contains(t, screen(t, m), "POST /checkout", "the chart is as it was")
	require.Nil(t, m.(Model).trace.g.filter)
}

// esc leaves the prompt as the filter in force left it, rather than as what was
// half typed into it.
func TestEscapingTheFilterPromptKeepsWhatIsInForce(t *testing.T) {
	m := filtered(t, checkout(), "postgres")
	m, _ = m.Update(k("/"))
	m = typeSearch(t, m, " redis")
	m, _ = m.Update(k("esc"))

	require.Equal(t, traceGantt, m.(Model).trace.mode)
	require.Equal(t, "postgres", m.(Model).trace.filter.Value())
	require.Contains(t, screen(t, m), "3 of 8 spans match")

	// And an emptied prompt is no filter rather than a query matching nothing.
	m, _ = m.Update(k("/"))
	for range len("postgres") {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m, _ = m.Update(k("enter"))
	require.Nil(t, m.(Model).trace.g.filter)
	require.Contains(t, screen(t, m), "verify token")
}

// Every view writes its own keys, and the prompt takes its rows off the chart
// rather than off the terminal: a chart that jumped as suggestions arrived
// would be unreadable.
func TestTheFilterPromptSaysItsKeysAndTakesItsRowsFromTheChart(t *testing.T) {
	var spans []trace.Span
	for i := range 40 {
		spans = append(spans, span(string(rune('a'+i)), "", "api", "GET /", 0, ms))
	}
	m := traceModelOf(t, trace.Build("t", spans))
	require.Contains(t, screen(t, m), "/ filter")
	height := strings.Count(screen(t, m), "\n")

	m, _ = m.Update(k("/"))
	m = typeSearch(t, m, "GE")

	out := screen(t, m)
	require.Contains(t, out, "enter apply")
	require.Contains(t, out, "tab complete")
	require.Contains(t, out, "esc cancel")
	require.Equal(t, height, strings.Count(out, "\n"),
		"the prompt took its rows off the chart rather than off the terminal")
}

// What a query compares against a span is what the span document draws: the
// names it is read under, and what it was labeled with.
func TestWhatAQuerySeesInASpan(t *testing.T) {
	s := span("db", "gw", "orders-db", "INSERT orders", 0, 10*ms)
	s.Status = trace.StatusError
	s.StatusMessage = "deadlock detected"
	s.Attrs = []logs.Field{{Key: "db.rows", Value: jx.Raw(`3`)}}
	n := &trace.Node{Span: s}

	for _, tt := range []struct {
		q    string
		want bool
	}{
		{`service.name=orders-db`, true},
		{`service_name=orders-db`, true},
		{`name="INSERT orders"`, true},
		{`db.rows=3`, true},
		{`db.rows=4`, false},
		{`status=error`, true},
		{`level=error`, true /* OTLP says the span ended badly, and that is a severity */},
		{`deadlock`, true},
		{`parent_id=gw`, true},
		{`-service.name=gateway`, true},
		{`duration=10ms`, false /* not something a span carries as a label */},
	} {
		e, err := query.Parse(tt.q)
		require.NoError(t, err, tt.q)
		require.Equal(t, tt.want, query.Match(e, spanRecord{n}), tt.q)
	}

	// A span that said nothing about how it ended is not silently a healthy one,
	// so it reports no severity at all.
	quiet := &trace.Node{Span: span("a", "", "api", "GET /", 0, ms)}
	e, err := query.Parse("level>=info")
	require.NoError(t, err)
	require.False(t, query.Match(e, spanRecord{quiet}))
}

// A fold puts spans away rather than filtering them out, so a match inside one
// still holds its ancestors on screen.
func TestAFoldedSubtreeStillHoldsItsMatches(t *testing.T) {
	m := filtered(t, checkout(), "payments")
	m, _ = m.Update(k("C"))

	var names []string
	for _, n := range m.(Model).trace.g.rows {
		names = append(names, n.Name)
	}
	require.Equal(t, []string{"POST /checkout", "charge order"}, names,
		"the fold holding the match is drawn, and what it put away is not")
	require.Contains(t, screen(t, m), "1 of 8 spans match")
}
