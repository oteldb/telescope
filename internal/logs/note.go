package logs

import (
	"strings"

	"github.com/oteldb/telescope/internal/source"
)

// noteText is what telescope's own words about a source read as.
//
// The wording lives here rather than where the note is made because a note has
// to reach the screen and the filter as one sentence: it is what the reader
// sees, and, having no fields and no severity, the only thing a query can match
// it by.
func noteText(l source.Line) string {
	var b strings.Builder
	b.WriteString("telescope: ")
	if l.Source != "" {
		b.WriteString(l.Source)
		b.WriteString(": ")
	}
	b.WriteString(noteVerb(l.Kind))
	if r := strings.TrimSpace(l.Reason); r != "" {
		b.WriteString(": ")
		b.WriteString(r)
	}
	return b.String()
}

// noteVerb says what happened, in the words the reader needs to tell one note
// from another: a place that never opened is a place to fix, and one that
// stopped an hour in is a timeline that ends early.
func noteVerb(k source.Kind) string {
	switch k {
	case source.KindOpenFailed:
		return "could not open"
	case source.KindReadFailed:
		return "read failed"
	case source.KindExited:
		return "stopped"
	case source.KindRestarted:
		return "restarted"
	default:
		return "note"
	}
}
