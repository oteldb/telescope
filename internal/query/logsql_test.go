package query

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogsQL(t *testing.T) {
	for _, tt := range []struct {
		name  string
		query string
		want  string
	}{
		{"a word is a regexp over every field", "reset", `*:~"(?i)reset"`},
		{"a phrase keeps its spaces", `"connection reset"`, `*:~"(?i)connection reset"`},
		{"a regexp is sent as one", "/res[ei]t/", `*:~"(?i)res[ei]t"`},
		{"terms are and-ed", "connection reset", `*:~"(?i)connection" *:~"(?i)reset"`},
		{"alternatives are grouped", "alpha or beta", `(*:~"(?i)alpha" OR *:~"(?i)beta")`},
		{"a negation is a negation", "-alpha", `-*:~"(?i)alpha"`},
		{"a field is anchored and folded", "pod=api-7", `pod:~"(?i)^api-7$"`},
		{"a field match is not anchored", "pod~api", `pod:~"(?i)api"`},
		{"a denied field is a negated filter", "pod!=api-7", `-pod:~"(?i)^api-7$"`},
		{"a dotted key is a key", "k8s.pod.name=api", `k8s.pod.name:~"(?i)^api$"`},
		{"nesting is kept", "a (b or c)", `*:~"(?i)a" (*:~"(?i)b" OR *:~"(?i)c")`},
		{"one field or another", "pod=api-7 or pod=api-8",
			`(pod:~"(?i)^api-7$" OR pod:~"(?i)^api-8$")`},
		{"a dropped conjunct leaves the or whole", "level>=warn (pod=api-7 or pod=api-8)",
			`(pod:~"(?i)^api-7$" OR pod:~"(?i)^api-8$")`},

		// What is dropped is dropped from a conjunction only, and the filter
		// still runs here over whatever comes back.
		{"a level is read from too many spellings to push", "level>=warn", ""},
		{"a level beside a term leaves the term", "level>=warn reset", `*:~"(?i)reset"`},
		{"a name a record is read under is not a field", "msg=hello", ""},
		{"the merge tag is telescope's own", "source=api", ""},
		{"a term with a metacharacter is not escaped, it is kept", "a+b", ""},
		{"a term with a quote is not written into a string", `"say \"hi\""`, ""},
		{"a regexp with a backslash is not written into a string", `/\d+/`, ""},
		{"a dropped branch takes its or with it", "level>=warn or reset", ""},
		{"a dropped operand takes its not with it", "not level>=warn", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, err := Parse(tt.query)
			require.NoError(t, err)

			got, ok := LogsQL(e)
			if tt.want == "" {
				require.False(t, ok, "got %q", got)
				return
			}
			require.True(t, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestLogsQLNarrowsNothingItCannotSay checks the rule the whole design rests
// on: a query that survives compilation must not exclude a record the filter
// itself would keep. Only a conjunction may lose a term.
func TestLogsQLNarrowsNothingItCannotSay(t *testing.T) {
	for _, query := range []string{
		"level>=warn or reset",
		"pod=api-7 or level>=warn",
		"not level>=warn",
		"(msg=hello or pod=api)",
		"-(level>=warn and pod=api)",
	} {
		t.Run(query, func(t *testing.T) {
			e, err := Parse(query)
			require.NoError(t, err)
			_, ok := LogsQL(e)
			require.False(t, ok, "a term that cannot be said must take its branch with it")
		})
	}
}

func TestLogsQLEmpty(t *testing.T) {
	_, ok := LogsQL(nil)
	require.False(t, ok)
}
