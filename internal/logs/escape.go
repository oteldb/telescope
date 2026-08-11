package logs

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ansiCtl marks an escaped control character, so what a line contains is never
// mistaken for what telescope wrote around it.
const ansiCtl = "\x1b[38;5;168m"

// Escape renders the control characters of a value visibly.
//
// A value out of a log database is bytes somebody else chose: a newline in it
// would break a row in half, and an escape sequence would repaint the rest of
// the screen. Neither is this value's business, so both are shown as what they
// are. A value with nothing to escape is returned untouched.
func Escape(s string) string { return escape(s, isControl, false) }

// Sanitize renders the control characters of a rendered line visibly, keeping
// the color sequences and the line breaks that are the rendering itself.
//
// A collector's own colors are worth passing through — that is what makes
// `docker logs` look like it does in a terminal — but only the colors. An
// escape sequence that moves the cursor or clears the screen is not coloring,
// and a line that carries one has no business doing it from inside a list.
func Sanitize(s string) string { return escape(s, isJunk, true) }

// escape rewrites every rune junk reports, and every byte that is not a rune at
// all, as what it stands for. With keepSGR, a color sequence is passed through
// whole rather than escaped a character at a time.
func escape(s string, junk func(rune) bool, keepSGR bool) string {
	if clean(s, junk) {
		return s
	}
	b := &strings.Builder{}
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case keepSGR && r == 0x1b:
			if n := sgrLen(s[i:]); n > 0 {
				b.WriteString(s[i : i+n])
				i += n
				continue
			}
			b.WriteString(ansiCtl + `\e` + ansiReset)
		case r == utf8.RuneError && size == 1:
			// Not a character at all. A stray 0x9b is what a terminal reads as
			// a control sequence introducer, so it is not passed on.
			b.WriteString(ansiCtl)
			fmt.Fprintf(b, `\x%02x`, s[i])
			b.WriteString(ansiReset)
		case junk(r):
			b.WriteString(ansiCtl)
			b.WriteString(escapeRune(r))
			b.WriteString(ansiReset)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// clean reports whether s can be shown as it is.
func clean(s string, junk func(rune) bool) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, junk)
}

// sgrLen returns the length of the SGR sequence s starts with, or zero if it
// starts with anything else.
//
// A control sequence is only what ECMA-48 says one is: parameter bytes, then
// intermediates, then the final byte that names it. Anything looser lets a
// sequence carrying arbitrary bytes past as if it were a color.
func sgrLen(s string) int {
	if !strings.HasPrefix(s, "\x1b[") {
		return 0
	}
	i := 2
	for ; i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f; i++ {
	}
	for ; i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f; i++ {
	}
	if i < len(s) && s[i] == 'm' {
		return i + 1
	}
	return 0
}

// isJunk reports whether a rune has no business in a rendered line. A newline
// is the rendering's own, since that is how a stacktrace has more than one.
func isJunk(r rune) bool { return r != '\n' && isControl(r) }

// isControl reports whether r is a control character: C0, DEL, or one of the C1
// characters a mangled encoding turns an escape sequence into.
func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// escapeRune spells a control character the way Go source does, so it reads as
// the character it stands for rather than as a number to look up.
func escapeRune(r rune) string {
	switch r {
	case '\a':
		return `\a`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '\v':
		return `\v`
	case 0x1b:
		return `\e`
	default:
		return fmt.Sprintf(`\x%02x`, r)
	}
}
