package logs

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func TestResourceChangesAreHighlighted(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, event, color string
	}{
		{"add", "ADDED", ansiOK},
		{"modify", "MODIFIED", ansiWarn},
		{"delete", "DELETED", ansiErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore(10)
			line := `{"watch_event":"` + tt.event + `","msg":"` + tt.event + ` Cluster octo/demo","namespace":"octo","kind":"Cluster","changes":[{"path":"/spec/replicas","before":1,"after":2},{"path":"/spec/new","after":null},{"path":"/spec/removed","before":"gone"}],"object":{"kind":"Cluster"}}`
			entry := store.Append(source.Line{Data: []byte(line)})
			require.NotNil(t, entry)
			require.Contains(t, entry.Head, tt.color+tt.event)
			require.Contains(t, entry.Head, ansiErr+"1"+ansiReset+" → "+ansiOK+"2"+ansiReset)
			require.Contains(t, entry.Head, ansiOK+"+ null")
			require.Contains(t, entry.Head, ansiErr+`- "gone"`)
			require.Contains(t, entry.Text, entry.Head, "the entry also highlights changes")
			require.Equal(t, line, string(entry.Raw))
			for _, field := range entry.Row {
				require.NotContains(t, []string{"object", "changes"}, field.Key)
			}
			filter := (Filter{Query: "namespace=octo kind=Cluster"}).Compile()
			require.NoError(t, filter.Err())
			require.True(t, filter.Match(entry))
			filter = (Filter{Query: "namespace=other"}).Compile()
			require.NoError(t, filter.Err())
			require.False(t, filter.Match(entry))
			require.Contains(t, ansi.Strip(entry.Head), "/spec/replicas: 1 → 2")
		})
	}
}

func TestResourceChangesEscapeUntrustedValues(t *testing.T) {
	t.Parallel()
	rec := Parse([]byte(`{"watch_event":"MODIFIED","msg":"hello\u001b[2J","changes":[{"path":"/a\n/b","before":"\u001b[2J","after":"ok"}]}`))
	got, ok := resourceMessage(rec)
	require.True(t, ok)
	require.NotContains(t, got, "\x1b[2J")
	require.NotContains(t, got, "\n")
}

func FuzzResourceMessage(f *testing.F) {
	f.Add(`{"watch_event":"MODIFIED","msg":"Cluster demo","changes":[{"path":"/spec/replicas","before":1,"after":2}]}`)
	f.Add(`{"msg":"ordinary log","changes":"not a diff"}`)
	f.Fuzz(func(t *testing.T, input string) {
		text, ok := resourceMessage(Parse([]byte(input)))
		if ok {
			require.NotContains(t, text, "\n")
			require.NotContains(t, text, "\r")
		}
	})
}
