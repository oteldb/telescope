package logs

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func line(s string) source.Line { return source.Line{Data: []byte(s)} }

func TestStoreCap(t *testing.T) {
	s := NewStore(3)
	for i := range 5 {
		require.NotNil(t, s.Append(line("line "+strconv.Itoa(i))))
	}
	require.Equal(t, 3, s.Len())
	require.Equal(t, 2, s.Dropped())
	require.Equal(t, 2, s.Entries()[0].Seq)
	require.Equal(t, 4, s.Entries()[2].Seq)
}

func TestStoreHighlightsOnlyUnstructured(t *testing.T) {
	s := NewStore(10)

	plain := s.Append(line("connect failed after 3 retries"))
	require.False(t, plain.Record.Structured)
	require.NotEqual(t, "connect failed after 3 retries", plain.Text, "plain lines are colored")

	structured := s.Append(line(`{"level":"info","msg":"hi"}`))
	require.True(t, structured.Record.Structured)
	require.Contains(t, structured.Text, "hi")
}

func TestViewFilter(t *testing.T) {
	s := NewStore(100)
	s.Append(line(`{"level":"info","msg":"alpha"}`))
	s.Append(line(`{"level":"error","msg":"beta"}`))
	s.Append(line(`plain gamma`))

	for _, tt := range []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"all", Filter{}, []string{"alpha", "beta", "plain gamma"}},
		{"literal", Filter{Query: "eta"}, []string{"beta"}},
		{"case insensitive", Filter{Query: "ALPHA"}, []string{"alpha"}},
		{"regexp", Filter{Query: "al|ga"}, []string{"alpha", "plain gamma"}},
		{"broken regexp is literal", Filter{Query: "beta("}, nil},
		{"level", Filter{MinLevel: LevelError}, []string{"beta", "plain gamma"}},
		{"level and query", Filter{MinLevel: LevelError, Query: "a"}, []string{"beta", "plain gamma"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := NewView(tt.filter)
			require.Equal(t, tt.want, bodies(v.Entries(s)))
		})
	}
}

// TestViewIncremental checks that appends fold into an existing projection and
// that eviction from the store forces a rescan.
func TestViewIncremental(t *testing.T) {
	s := NewStore(2)
	v := NewView(Filter{Query: "keep"})

	s.Append(line("keep one"))
	require.Equal(t, []string{"keep one"}, bodies(v.Entries(s)))

	s.Append(line("drop two"))
	s.Append(line("keep three"))
	require.Equal(t, []string{"keep three"}, bodies(v.Entries(s)))

	// "keep one" is evicted by the cap, so the projection must be rebuilt.
	s.Append(line("keep four"))
	require.Equal(t, []string{"keep three", "keep four"}, bodies(v.Entries(s)))
}

func TestViewSetFilterResets(t *testing.T) {
	s := NewStore(10)
	s.Append(line("alpha"))
	s.Append(line("beta"))

	v := NewView(Filter{Query: "alpha"})
	require.Equal(t, []string{"alpha"}, bodies(v.Entries(s)))

	v.SetFilter(Filter{Query: "beta"})
	require.Equal(t, []string{"beta"}, bodies(v.Entries(s)))
}

func TestFilterDescribe(t *testing.T) {
	require.Equal(t, "no filter", Filter{}.Compile().Describe())
	require.Equal(t, "re:a|b", Filter{Query: "a|b"}.Compile().Describe())
	require.Equal(t, "text:a(", Filter{Query: "a("}.Compile().Describe())
	require.Equal(t, "level≥warn", Filter{MinLevel: LevelWarn}.Compile().Describe())
}

func bodies(entries []*Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Record.Body)
	}
	return out
}
