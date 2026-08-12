package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/source"
)

// rowOf is the row of an entry with the given key.
func rowOf(t *testing.T, m entryModel, key string) item {
	t.Helper()
	for _, it := range m.document(m.docWidth()) {
		if it.key == key {
			return it
		}
	}
	t.Fatalf("no row for %q", key)
	return item{}
}

// TestWhatIsWorthNarrowingBy: a value shared with other lines selects them; one
// that belongs to this line alone selects nothing but itself.
func TestWhatIsWorthNarrowingBy(t *testing.T) {
	m := entryOf(t, `{"level":"warn","ts":"2026-08-10T10:00:00Z","msg":"boom","pod":"api-0"}`)

	term := func(key string) string {
		e := rowOf(t, m, key).term()
		if e == nil {
			return ""
		}
		return e.String()
	}
	require.Equal(t, "pod=api-0", term("pod"))
	require.Equal(t, "level=warn", term("level"), "a level narrows as a level, not as a word")

	require.Empty(t, term("time"), "no two lines share a timestamp")
	require.Empty(t, term("body"), "and a body with an id in it selects the line it came from")
	require.Empty(t, term("rendered"))
	require.Empty(t, term("raw"))
}

// TestNarrowingTermIsWrittenAsItWouldBeTyped: the term goes through the prompt,
// so it has to be a term the prompt could have read.
func TestNarrowingTermIsWrittenAsItWouldBeTyped(t *testing.T) {
	m := entryOf(t, `{"msg":"boom"}`,
		source.Label{Key: "unit", Value: "systemd resolved.service"},
		source.Label{Key: "cmd", Value: "not"},
	)

	spaced := rowOf(t, m, "unit").term().String()
	require.Equal(t, `unit="systemd resolved.service"`, spaced)
	roundTrip(t, spaced)

	keyword := rowOf(t, m, "cmd").term().String()
	require.Equal(t, `cmd="not"`, keyword, "a value the lexer would read as an operator is quoted")
	roundTrip(t, keyword)
}

func roundTrip(t *testing.T, q string) {
	t.Helper()
	parsed, err := query.Parse(q)
	require.NoError(t, err, "the prompt can read what the jump wrote")
	require.Equal(t, q, parsed.String())
}

// TestNarrowAndsOntoWhatIsAlreadyThere: a jump narrows, so what was on the
// prompt has to survive it.
func TestNarrowAndsOntoWhatIsAlreadyThere(t *testing.T) {
	pod := query.Field{Key: "pod", Op: query.OpEq, Value: "api-0"}

	for _, tt := range []struct {
		name, from, want string
	}{
		{"nothing", "", "pod=api-0"},
		{"a word", "timeout", "timeout pod=api-0"},
		{"a conjunction", "timeout level>=warn", "timeout level>=warn pod=api-0"},
		// The one that matters: an or that picked the term up in one branch
		// would admit lines the filter was never meant to.
		{"a disjunction", "timeout or refused", "(timeout or refused) pod=api-0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newLogs(source.Config{Collector: source.CollectorDocker, Container: "app"},
				logs.NewStore(10), tt.from)
			m, _ = m.narrow(pod)

			require.Equal(t, tt.want, m.view.Filter().Query)
			require.Equal(t, tt.want, m.search.Value(), "and the prompt says what is in force")
			roundTrip(t, tt.want)
		})
	}
}

// TestFilterJumpLandsOnTheList: what the narrowing did is only visible in the
// list, so the entry gets out of the way.
func TestFilterJumpLandsOnTheList(t *testing.T) {
	m := logsModel(t,
		`{"msg":"first","pod":"api-0"}`,
		`{"msg":"second","pod":"api-1"}`,
	)
	m = send(t, m, k("enter"))
	require.Equal(t, stateEntry, m.(Model).state)

	m = jump(t, m, "pod")

	require.Equal(t, stateLogs, m.(Model).state)
	require.Equal(t, "pod=api-1", m.(Model).logs.view.Filter().Query,
		"the entry the cursor was on is the one it narrows by")

	out := screen(t, m)
	require.Contains(t, out, "second")
	require.NotContains(t, out, "first", "the line that does not match is gone")
}

// TestFilterJumpAsksADatabaseAgain: pushing a term down is what makes a jump
// worth anything on a log database — the lines it selects are mostly ones that
// were never fetched, so filtering what is already here would find nothing.
func TestFilterJumpAsksADatabaseAgain(t *testing.T) {
	m := newLogs(source.Config{Collector: source.CollectorVictoriaLogs}, logs.NewStore(10), "")
	_, cmd := m.narrow(query.Text{Value: "reset"})
	require.NotNil(t, cmd, "the source can answer this one, so it is asked again")

	msg, ok := cmd().(requeryMsg)
	require.True(t, ok)
	require.Equal(t, "reset", msg.query)

	// A term no database can be asked changes nothing about what to fetch, and
	// filtering what came back is the whole of the answer.
	m = newLogs(source.Config{Collector: source.CollectorVictoriaLogs}, logs.NewStore(10), "")
	_, cmd = m.narrow(query.Level{Op: query.OpGe, Level: zapcore.WarnLevel})
	require.Nil(t, cmd)
}

// TestFilterJumpSaysWhenThereIsNothingToDo: a key that silently does nothing
// reads as a key that is broken.
func TestFilterJumpSaysWhenThereIsNothingToDo(t *testing.T) {
	m := entryOf(t, `{"msg":"boom"}`)
	m = selectKey(t, m, "body")

	m, cmd := m.Update(k("f"))
	require.Nil(t, cmd, "and the view does not change")
	require.Contains(t, ansi.Strip(m.View()), "nothing to narrow by")
}

// jump puts the entry cursor on key and presses f, following where it leads.
func jump(t *testing.T, m tea.Model, key string) tea.Model {
	t.Helper()
	root := m.(Model)
	root.entry = selectKey(t, root.entry, key)

	next, cmd := tea.Model(root).Update(k("f"))
	require.NotNil(t, cmd, "f asks for the narrowing")
	next, _ = next.Update(cmd())
	return next
}
