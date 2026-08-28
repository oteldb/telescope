package view

import (
	"strings"

	"github.com/go-faster/errors"
)

// splitShell undoes [shellQuote] the way a POSIX shell would, so a test can
// check that what a link says is what a terminal will pass on. It understands
// exactly what shellQuote writes — single quotes and bare words — and refuses
// anything else rather than growing into a shell.
func splitShell(s string) ([]string, error) {
	var (
		out   []string
		word  strings.Builder
		open  bool
		began bool
	)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			open, began = !open, true
		case c == '\\' && !open && i+1 < len(s) && s[i+1] == '\'':
			// The splice shellQuote writes for a quote inside a quoted word.
			word.WriteByte('\'')
			i++
			began = true
		case c == ' ' && !open:
			if began || word.Len() > 0 {
				out = append(out, word.String())
				word.Reset()
				began = false
			}
		default:
			word.WriteByte(c)
			began = true
		}
	}
	if open {
		return nil, errors.New("unbalanced quote")
	}
	if began || word.Len() > 0 {
		out = append(out, word.String())
	}
	return out, nil
}
