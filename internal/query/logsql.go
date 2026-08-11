package query

import (
	"strings"
)

// LogsQL compiles e into a LogsQL filter and reports whether anything could be
// compiled at all.
//
// What comes back selects a superset of what e selects: a term that cannot be
// translated with certainty is dropped rather than approximated, and the filter
// still runs over everything that comes back. Pushing a query down is therefore
// an optimization and never a different answer — which is what lets one query
// mean the same thing over a group of databases and shell commands.
//
// A term is dropped rather than escaped wherever escaping would have to guess
// how LogsQL reads a backslash. Being conservative costs a wider query; being
// wrong costs lines that are never shown.
func LogsQL(e Expr) (string, bool) {
	if e == nil {
		return "", false
	}
	// Only a conjunction may lose a term: dropping one narrows nothing, while
	// dropping a branch of an or, or the operand of a not, would.
	if and, ok := e.(And); ok {
		var kept []string
		for _, sub := range and {
			if s, ok := logsQL(sub); ok {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			return "", false
		}
		return strings.Join(kept, " "), true
	}
	return logsQL(e)
}

// logsQL translates e exactly, or not at all.
func logsQL(e Expr) (string, bool) {
	switch e := e.(type) {
	case And:
		return logsQLAll(e, " ")
	case Or:
		return logsQLAll(e, " OR ")
	case Not:
		s, ok := logsQL(e.Expr)
		if !ok {
			return "", false
		}
		return "-" + s, true
	case Text:
		if !literalPushable(e.Value) {
			return "", false
		}
		// Every field, since that is what a bare term matches here too, and a
		// regexp, since a word filter there is a token and a term here is a
		// substring.
		return `*:~"(?i)` + e.Value + `"`, true
	case Regexp:
		if !regexpPushable(e.Source) {
			return "", false
		}
		return `*:~"(?i)` + e.Source + `"`, true
	case Field:
		return logsQLField(e)
	default:
		// A level is read from any of a dozen spellings and from severity
		// numbers, and no one field holds it.
		return "", false
	}
}

func logsQLAll(list []Expr, sep string) (string, bool) {
	parts := make([]string, 0, len(list))
	for _, sub := range list {
		s, ok := logsQL(sub)
		if !ok {
			return "", false
		}
		parts = append(parts, s)
	}
	return "(" + strings.Join(parts, sep) + ")", true
}

func logsQLField(e Field) (string, bool) {
	if !fieldPushable(e.Key) {
		return "", false
	}
	var filter string
	switch e.Op {
	case OpEq, OpNe:
		if !literalPushable(e.Value) {
			return "", false
		}
		// Anchored, because equality here is the whole value, and
		// case-insensitive, because equality here ignores case.
		filter = e.Key + `:~"(?i)^` + e.Value + `$"`
	case OpMatch, OpNotMatch:
		if !regexpPushable(e.Value) {
			return "", false
		}
		filter = e.Key + `:~"(?i)` + e.Value + `"`
	default:
		return "", false
	}
	// A line without the field matches neither the filter nor, therefore, its
	// negation — which is what a missing field does here as well.
	if e.Op == OpNe || e.Op == OpNotMatch {
		return "-" + filter, true
	}
	return filter, true
}

// fieldPushable reports whether a key means the same thing to a database as it
// does here. The names a record is read under do not: msg is whatever the
// shipper called the message, and source and stream are telescope's own.
func fieldPushable(key string) bool {
	switch strings.ToLower(key) {
	case "level", "msg", "message", "body", "trace_id", "span_id", "source", "stream", "time", "timestamp":
		return false
	}
	for i := range len(key) {
		switch c := key[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '.', c == '-':
		default:
			return false
		}
	}
	return key != ""
}

// literalPushable reports whether a literal can be written into a LogsQL regexp
// as itself. A regexp metacharacter would have to be escaped, and a backslash
// or a quote would have to survive being read as a string first.
func literalPushable(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, "\\\"^$.|?*+()[]{}")
}

// regexpPushable reports whether a regexp can be written into a LogsQL string
// unchanged. Its own metacharacters are the point; only the string's are not.
func regexpPushable(src string) bool {
	return src != "" && !strings.ContainsAny(src, "\\\"")
}
