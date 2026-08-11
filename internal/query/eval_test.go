package query

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

// record is a line as the evaluator sees one.
type record struct {
	text     []string
	fields   map[string]string
	level    zapcore.Level
	hasLevel bool
}

func (r record) Haystack() [][]byte {
	out := make([][]byte, 0, len(r.text))
	for _, s := range r.text {
		out = append(out, []byte(s))
	}
	return out
}

func (r record) Field(key string) (string, bool) {
	v, ok := r.fields[key]
	return v, ok
}

func (r record) Level() (zapcore.Level, bool) { return r.level, r.hasLevel }

func TestMatch(t *testing.T) {
	line := record{
		text:     []string{"connection reset by peer", "pod=api-7 namespace=prod"},
		fields:   map[string]string{"pod": "api-7", "msg": "connection reset by peer"},
		level:    zapcore.ErrorLevel,
		hasLevel: true,
	}
	bare := record{text: []string{"starting up"}}

	for _, tt := range []struct {
		name  string
		query string
		rec   record
		want  bool
	}{
		{"an empty query admits everything", "", bare, true},
		{"a word matches the line", "reset", line, true},
		{"a word is case insensitive", "RESET", line, true},
		{"a word that is not there", "refused", line, false},
		{"a phrase matches across words", `"connection reset"`, line, true},
		{"a phrase is not a set of words", `"reset connection"`, line, false},
		{"a word matches the labels beside the line", "prod", line, true},
		{"terms are and-ed", "connection peer", line, true},
		{"one term missing fails the conjunction", "connection refused", line, false},
		{"one term is enough for a disjunction", "refused or reset", line, true},
		{"and binds tighter than or", "refused peer or reset", line, true},
		{"not inverts", "-refused", line, true},
		{"not inverts a hit", "-reset", line, false},
		{"a regexp matches", "/res[ei]t/", line, true},
		{"a regexp that does not", "/^reset/", line, false},
		{"a field is compared exactly", "pod=api-7", line, true},
		{"a field is not a substring", "pod=api", line, false},
		{"a field may be matched loosely", "pod~api", line, true},
		{"a missing field fails a comparison", "container=api", line, false},
		{"a missing field passes a denial", "container!=api", line, true},
		{"a level compares", "level>=warn", line, true},
		{"a level below the bound", "level>=fatal", line, false},
		{"a line with no level passes no comparison", "level>=debug", bare, false},
		{"a line with no level passes a negated one", "-level>=debug", bare, true},
		{"a group is evaluated as one", "reset (refused or peer)", line, true},
		{"a group that fails", "reset (refused or timeout)", line, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, err := Parse(tt.query)
			require.NoError(t, err)
			require.Equal(t, tt.want, Match(e, tt.rec))
		})
	}
}

// TestMatchFoldsTheLineOnce checks the evaluation cache: a query of several
// terms must not lower the same line once per term.
func TestMatchFoldsTheLineOnce(t *testing.T) {
	r := &counting{record: record{text: []string{"Connection Reset By Peer"}}}
	e, err := Parse("connection reset peer")
	require.NoError(t, err)
	require.True(t, Match(e, r))
	require.Equal(t, 1, r.reads)
}

type counting struct {
	record
	reads int
}

func (c *counting) Haystack() [][]byte {
	c.reads++
	return c.record.Haystack()
}
