package logs

import (
	"strings"

	"github.com/go-faster/jx"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/source"
)

// Kind is what a key says its value is, which is how a value is rendered
// without having to guess from the value alone: "266" is a line number and
// "DEBUG" is a severity only because of the key beside them.
type Kind int

// Kinds of value a key is known to carry.
const (
	KindOther Kind = iota
	KindTime
	KindLevel
	KindTrace
)

// KindOf classifies a key, by the same aliases the parser reads.
func KindOf(key string) Kind {
	switch {
	case matches(key, timeKeys):
		return KindTime
	case matches(key, levelKeys):
		return KindLevel
	case matches(key, traceKeys), matches(key, spanKeys):
		return KindTrace
	default:
		return KindOther
	}
}

// LevelOf reads a severity written as a word or a number, as a label carries it.
func LevelOf(value string) (zapcore.Level, bool) { return parseLevel(jx.Raw(value)) }

// labelValue finds the first label whose key is one of keys.
func labelValue(labels []source.Label, keys []string) string {
	for _, l := range labels {
		if matches(l.Key, keys) {
			return l.Value
		}
	}
	return ""
}

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
