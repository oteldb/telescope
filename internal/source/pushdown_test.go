package source

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/query"
)

func parse(t *testing.T, s string) query.Expr {
	t.Helper()
	e, err := query.Parse(s)
	require.NoError(t, err)
	return e
}

// TestVictoriaLogsPushesTheFilter: the query a place names bounds what it can
// ever produce, and the filter narrows it further wherever LogsQL can say so.
func TestVictoriaLogsPushesTheFilter(t *testing.T) {
	for _, tt := range []struct {
		name     string
		selector string
		filter   string
		want     string
	}{
		{"nothing named at all", "", "", "*"},
		{"only a selector", "level:error", "", "level:error"},
		{"only a filter", "", "reset", `*:~"(?i)reset"`},
		{"both", "level:error", "reset", `(level:error) *:~"(?i)reset"`},
		{"a filter it cannot be asked", "level:error", "level>=warn", "level:error"},
		{"the askable half of a filter", "", "level>=warn reset", `*:~"(?i)reset"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Collector: CollectorVictoriaLogs, Target: tt.selector}
			require.Equal(t, tt.want, cfg.WithFilter(parse(t, tt.filter)).vlogsQuery())
		})
	}
}

// TestPushedIsPerSource: a group asks each place as much of the filter as that
// place can answer, and the answer for one that answers none of it is nothing.
func TestPushedIsPerSource(t *testing.T) {
	group := Config{
		Collector: CollectorMerge,
		Merge: []Config{
			{Name: "vl", Collector: CollectorVictoriaLogs},
			{Name: "docker", Collector: CollectorDocker, Container: "api"},
		},
	}
	require.Equal(t, []string{"*", ""}, group.Pushed())
	require.Equal(t,
		[]string{`*:~"(?i)reset"`, ""},
		group.WithFilter(parse(t, "reset")).Pushed(),
		"the filter reaches the children of a group")

	require.Equal(t, group.Pushed(), group.WithFilter(parse(t, "level>=warn")).Pushed(),
		"a filter nothing can be asked changes no query, so nothing is asked again")
}
