package logs

import (
	"strings"

	"github.com/go-faster/jx"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/source"
)

// levelFromLabels reads the severity a source reported beside a line.
//
// A log database indexes the level whether or not the message repeats it, and a
// line that says only "Got request" is otherwise a line with no level at all.
func levelFromLabels(labels []source.Label) (zapcore.Level, bool) {
	for _, l := range labels {
		if !matches(l.Key, levelKeys) {
			continue
		}
		if lvl, ok := parseLevel(jx.Raw(l.Value)); ok {
			return lvl, true
		}
	}
	return 0, false
}

// labelText renders a label set for grepping, so a filter reaches what the list
// does not show.
func labelText(labels []source.Label) string {
	if len(labels) == 0 {
		return ""
	}
	b := &strings.Builder{}
	for i, l := range labels {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(l.Key)
		b.WriteByte('=')
		b.WriteString(l.Value)
	}
	return b.String()
}
