package query

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogQL(t *testing.T) {
	for _, tt := range []struct {
		name  string
		query string
		want  string
	}{
		{"a field is the selector", "pod=api-7", `{pod=~"(?i)api-7"}`},
		{"and every field of a conjunction is in it", "pod=api ns=prod", `{pod=~"(?i)api", ns=~"(?i)prod"}`},
		{"a denial comes along with one that selects", "app=api pod!=api-7", `{app=~"(?i)api", pod!~"(?i)api-7"}`},
		{
			"a match is padded back out to the whole value",
			"pod~api-", `{pod=~"(?i).*(?:api-).*"}`,
		},
		{
			"an alternation is grouped before it is padded",
			"pod~/api|web/", `{pod=~"(?i).*(?:api|web).*"}`,
		},
		{"what is left of the filter is the view's", "pod=api reset", `{pod=~"(?i)api"}`},
		{"a level says nothing about a stream", "pod=api level>=warn", `{pod=~"(?i)api"}`},

		// Nothing to select by is nothing to ask, which is not the same as
		// asking for everything: Loki has no match-all to fall back on.
		{"a word alone selects no stream", "reset", ""},
		{"nor does a phrase or a regexp", `"connection reset" /res[ei]t/`, ""},
		{"nor a level", "level>=warn", ""},
		{"a denial alone matches every stream that lacks the label", "pod!=api-7", ""},
		{"and so does a pattern that admits the empty string", "pod~/a*/", ""},
		{"a dotted key is not a Loki label", "k8s.pod.name=api", ""},
		{"a name a record is read under is not a label", "msg=hello", ""},
		{"the merge tag is telescope's own", "source=api", ""},
		{"a value with a metacharacter is not escaped, it is dropped", "pod=api+7", ""},
		{"a branch of an or cannot be dropped", "pod=api or pod=web", ""},
		{"nor the operand of a not", "not pod=api", ""},
		{"a field under an or is not a conjunct", "level>=warn (pod=api or pod=web)", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, err := Parse(tt.query)
			require.NoError(t, err)

			got, ok := LogQL(e)
			if tt.want == "" {
				require.False(t, ok, "got %q", got)
				return
			}
			require.True(t, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestLogQLEmpty(t *testing.T) {
	_, ok := LogQL(nil)
	require.False(t, ok)
}
