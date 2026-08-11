package logs

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestEscape(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"clean", "artifact up-to-date", "artifact up-to-date"},
		{"newline", "a\nb", `a\nb`},
		{"tab and return", "a\tb\rc", `a\tb\rc`},
		{"escape", "\x1b[31mred", `\e[31mred`},
		{"nul", "a\x00b", `a\x00b`},
		{"delete", "a\x7f", `a\x7f`},
		{"c1", "a\u009b", `a\x9b`},
		{"a byte that is not a character", "a\x9b", `a\x9b`},
		{"broken utf-8", "a\xff", `a\xff`},
		{"unicode is not a control", "héllo — ok", "héllo — ok"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, strip(Escape(tt.in)))
		})
	}
	require.Equal(t, "plain", Escape("plain"), "a clean value is returned untouched")
}

// TestSanitizeKeepsColors: a collector's own coloring is what makes a rendered
// line look like it does in a terminal, and only the coloring is.
func TestSanitizeKeepsColors(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"color survives", "\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m"},
		{"clearing the screen does not", "\x1b[2Jgone", `\e[2Jgone`},
		{"nor moving the cursor", "\x1b[5Aup", `\e[5Aup`},
		{"nor a carriage return", "over\rwritten", `over\rwritten`},
		{"a line break is the rendering's own", "line\nbreak", "line\nbreak"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Sanitize(tt.in)
			if tt.want == tt.in {
				require.Equal(t, tt.want, got)
				return
			}
			require.Equal(t, tt.want, strip(got))
		})
	}
}

// TestStoreSanitizesTheRendering: what a database hands back reaches the list,
// and a list is no place to run somebody else's escape sequences.
func TestStoreSanitizesTheRendering(t *testing.T) {
	s := NewStore(10)
	e := s.Append(line("boom\x1b[2J\x07"))
	require.Contains(t, strip(e.Head), `\e[2J`)
	require.Contains(t, strip(e.Head), `\a`)
	require.NotContains(t, e.Head, "\x1b[2J")
	require.NotContains(t, e.Head, "\x07")
}

// strip removes the color escapes telescope itself writes, leaving what it
// says.
func strip(s string) string {
	b := &strings.Builder{}
	for i := 0; i < len(s); {
		if n := sgrLen(s[i:]); n > 0 {
			i += n
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// FuzzEscape: what comes out is drawable — one row, no sequence a terminal
// would obey — whatever went in.
func FuzzEscape(f *testing.F) {
	for _, s := range []string{
		"", "plain", "a\nb", "\x1b[31mred\x1b[0m", "\x1b[2J", "a\x9b", "héllo", "\x00\x7f\xff",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		escaped := Escape(s)
		require.True(t, utf8.ValidString(escaped))
		require.NotContains(t, strip(escaped), "\x1b", "no escape survives escaping")
		for _, r := range strip(escaped) {
			require.False(t, isControl(r), "control %q survived: %q", r, s)
		}

		sane := Sanitize(s)
		require.True(t, utf8.ValidString(sane))
		for _, r := range strip(sane) {
			require.True(t, r == '\n' || !isControl(r), "control %q survived: %q", r, s)
		}
	})
}
