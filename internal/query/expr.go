// Package query is the filter typed at the log view.
//
// It is deliberately a language about one line at a time: every expression can
// be answered by looking at a single record, so a filter selects the same
// entries whichever source produced them. A source that can answer part of a
// query itself is then an optimization and not a different result.
package query

import (
	"regexp"
	"strconv"
	"strings"

	"go.uber.org/zap/zapcore"
)

// Expr is a parsed query. A nil Expr admits every record.
//
// The set of nodes is closed: match is unexported, so anything that evaluates a
// query lives here and beside the code that compiles one.
type Expr interface {
	String() string
	match(*ctx) bool
}

// Op is how a term compares. Ordering compares only apply to a level.
type Op string

// The comparisons a term may use.
const (
	OpEq       Op = "="
	OpNe       Op = "!="
	OpMatch    Op = "~"
	OpNotMatch Op = "!~"
	OpGe       Op = ">="
	OpGt       Op = ">"
	OpLe       Op = "<="
	OpLt       Op = "<"
)

// ordering reports whether the op compares rather than equates.
func (o Op) ordering() bool {
	switch o {
	case OpGe, OpGt, OpLe, OpLt:
		return true
	default:
		return false
	}
}

// And admits a record every operand admits.
type And []Expr

// Or admits a record any operand admits.
type Or []Expr

// Not inverts what it wraps.
type Not struct{ Expr Expr }

// Text matches a case-insensitive substring of the line and of the labels
// beside it, which is what a bare word or a quoted phrase means.
type Text struct {
	Value string

	lower []byte
}

// Regexp matches the line and its labels, case-insensitively.
type Regexp struct {
	Source string

	re *regexp.Regexp
}

// Field compares one field of a record: a label the source reported, a key of a
// structured line, or one of the names a record is read under.
type Field struct {
	Key   string
	Op    Op
	Value string

	re *regexp.Regexp
}

// Level compares the severity of a record. A record with no level of its own
// never matches: an unlevelled line is not quietly an info one.
type Level struct {
	Op    Op
	Level zapcore.Level
}

func (e And) String() string { return join(e, " ") }
func (e Or) String() string  { return join(e, " or ") }

func (e Not) String() string { return "not " + group(e.Expr) }

func (e Text) String() string { return quote(e.Value) }

func (e Regexp) String() string { return delimit(e.Source) }

func (e Field) String() string {
	if e.Op == OpMatch || e.Op == OpNotMatch {
		return e.Key + string(e.Op) + delimit(e.Value)
	}
	return e.Key + string(e.Op) + quoteValue(e.Value)
}

func (e Level) String() string { return "level" + string(e.Op) + e.Level.String() }

func join(list []Expr, sep string) string {
	parts := make([]string, 0, len(list))
	for _, e := range list {
		parts = append(parts, group(e))
	}
	return strings.Join(parts, sep)
}

// group parenthesizes an operand that would otherwise re-associate when read
// back, which is the only thing [Expr.String] has to be careful about.
func group(e Expr) string {
	switch e.(type) {
	case And, Or:
		return "(" + e.String() + ")"
	default:
		return e.String()
	}
}

// quote writes a term as it would have to be typed: bare when it is a plain
// word, quoted when it holds anything the lexer would read as something else.
// A dash or a slash is punctuation only where it leads, so deploy/api needs no
// quoting and -1 does.
func quote(s string) string {
	if s == "" || keyword(s) || s[0] == '-' {
		return strconv.Quote(s)
	}
	return quoteValue(s)
}

// quoteValue is quote where a value is expected, which is everywhere a dash
// cannot have been a negation.
func quoteValue(s string) string {
	switch {
	case s == "", keyword(s),
		strings.ContainsAny(s, " \t\n\r()\""+opChars),
		s[0] == '/':
		return strconv.Quote(s)
	default:
		return s
	}
}

// delimit writes a regexp back between slashes. The source is kept as it was
// read, so nothing has to be escaped here that was not escaped there.
func delimit(src string) string { return "/" + src + "/" }

// escapeDelim rewrites a value that was not typed as a regexp into one that can
// be: a slash of its own becomes an escaped slash, which the regexp engine
// reads as the same character. Escapes already in the value are left alone,
// since they are the engine's and not the delimiter's.
func escapeDelim(s string) string {
	if !strings.ContainsAny(s, `/\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
			if i+1 == len(s) {
				b.WriteByte('\\')
				break
			}
			i++
			b.WriteByte(s[i])
		case '/':
			b.WriteString(`\/`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func keyword(s string) bool {
	switch strings.ToLower(s) {
	case "and", "or", "not":
		return true
	default:
		return false
	}
}
