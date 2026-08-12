package query

import (
	"bytes"
	"slices"
	"strings"

	"go.uber.org/zap/zapcore"
)

// Record is one line as a query sees it.
type Record interface {
	// Haystack is what a bare word matches: the line, and the labels beside it
	// that the list has no room to show.
	Haystack() [][]byte
	// Field returns one named value: a label, a key of a structured line, or a
	// name the record is read under, such as msg or trace_id.
	Field(key string) (string, bool)
	// Level is the severity of the record, if it reported one.
	Level() (zapcore.Level, bool)
}

// Match reports whether r passes e. A nil e admits everything.
func Match(e Expr, r Record) bool {
	if e == nil {
		return true
	}
	c := ctx{rec: r}
	return e.match(&c)
}

// ctx carries the record through one evaluation, holding the case-folded text
// so that a query of several terms folds the line once rather than per term.
type ctx struct {
	rec    Record
	lower  [][]byte
	folded bool
}

func (c *ctx) text() [][]byte {
	if !c.folded {
		for _, part := range c.rec.Haystack() {
			c.lower = append(c.lower, bytes.ToLower(part))
		}
		c.folded = true
	}
	return c.lower
}

func (e And) match(c *ctx) bool {
	for _, sub := range e {
		if !sub.match(c) {
			return false
		}
	}
	return true
}

func (e Or) match(c *ctx) bool {
	for _, sub := range e {
		if sub.match(c) {
			return true
		}
	}
	return false
}

func (e Not) match(c *ctx) bool { return !e.Expr.match(c) }

func (e Text) match(c *ctx) bool {
	for _, part := range c.text() {
		if bytes.Contains(part, e.lower) {
			return true
		}
	}
	return false
}

func (e Regexp) match(c *ctx) bool {
	return slices.ContainsFunc(c.text(), e.re.Match)
}

// match compares one field. A record that does not carry the field fails every
// comparison and passes every negation: not having a pod is not being that pod.
func (e Field) match(c *ctx) bool {
	value, ok := c.rec.Field(e.Key)
	if !ok {
		return e.Op == OpNe || e.Op == OpNotMatch
	}
	switch e.Op {
	case OpEq:
		return strings.EqualFold(value, e.Value)
	case OpNe:
		return !strings.EqualFold(value, e.Value)
	case OpMatch:
		return e.re.MatchString(value)
	case OpNotMatch:
		return !e.re.MatchString(value)
	default:
		return false
	}
}

// match compares severity. A line that reported none is not silently an info
// line, so it passes no comparison at all.
func (e Level) match(c *ctx) bool {
	level, ok := c.rec.Level()
	if !ok {
		return false
	}
	switch e.Op {
	case OpEq:
		return level == e.Level
	case OpNe:
		return level != e.Level
	case OpGe:
		return level >= e.Level
	case OpGt:
		return level > e.Level
	case OpLe:
		return level <= e.Level
	case OpLt:
		return level < e.Level
	default:
		return false
	}
}
