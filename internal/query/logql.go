package query

import (
	"strings"
)

// LogQL compiles e into a stream selector and reports whether one could be
// compiled at all.
//
// Loki has no match-all. Every query selects streams by label, so a filter that
// names none is not a wider query here but no query: the false return is what
// says there is nothing to ask yet, and the caller reads no lines rather than
// all of them.
//
// What comes back still selects a superset of what e selects, the way every
// pushdown here does — with one exception the API forces. A label a stream does
// not carry is a stream this cannot reach, so a filter naming a field that Loki
// holds inside the line rather than beside it selects nothing at all. That is
// the price of a database that will not answer without a selector.
//
// Only the label comparisons of a top-level conjunction are compiled. A bare
// word is not one of them: here it matches the labels beside a line as well as
// what the line says, and Loki's own line filter reads the line alone.
func LogQL(e Expr) (string, bool) {
	var (
		matchers []string
		selects  bool
	)
	for _, sub := range conjuncts(e) {
		f, ok := sub.(Field)
		if !ok {
			continue
		}
		m, narrows, ok := logQLMatcher(f)
		if !ok {
			continue
		}
		matchers = append(matchers, m)
		selects = selects || narrows
	}
	// Loki refuses a selector every stream matches, which is what negations
	// alone are: a stream without the label carries it as empty, and empty is
	// what they all fail to be.
	if !selects {
		return "", false
	}
	return "{" + strings.Join(matchers, ", ") + "}", true
}

// conjuncts are the terms that all have to hold, which are the only ones a
// compiler may pick from: dropping one narrows nothing, while dropping a branch
// of an or, or the operand of a not, would.
func conjuncts(e Expr) []Expr {
	switch e := e.(type) {
	case nil:
		return nil
	case And:
		return e
	default:
		return []Expr{e}
	}
}

// logQLMatcher writes one term as a label matcher, and reports whether it is a
// matcher that selects — one a stream cannot satisfy by not carrying the label,
// which is what Loki wants at least one of.
//
// Every matcher is a regexp, because equality here ignores case and Loki's does
// not. Loki anchors a matcher to the whole label value, which is what equality
// means anyway; a term that matched part of one is wrapped back out to it.
func logQLMatcher(e Field) (matcher string, selects, ok bool) {
	if !fieldPushable(e.Key) || !logQLName(e.Key) {
		return "", false, false
	}
	switch e.Op {
	case OpEq, OpNe:
		if !literalPushable(e.Value) {
			return "", false, false
		}
		return e.Key + logQLOp(e.Op) + `"(?i)` + e.Value + `"`, e.Op == OpEq, true
	case OpMatch, OpNotMatch:
		if !regexpPushable(e.Value) {
			return "", false, false
		}
		// Grouped, since an alternation would otherwise take the padding into
		// its last branch and select everything.
		matcher = e.Key + logQLOp(e.Op) + `"(?i).*(?:` + e.Value + `).*"`
		// A pattern that admits the empty string admits a stream that has no
		// such label, so only one that cannot is worth a selector.
		return matcher, e.Op == OpMatch && e.re != nil && !e.re.MatchString(""), true
	default:
		return "", false, false
	}
}

func logQLOp(op Op) string {
	if op == OpNe || op == OpNotMatch {
		return "!~"
	}
	return "=~"
}

// logQLName reports whether a key can be a Loki label name at all. They are
// Prometheus' names, and a dot is not one: a database that renamed service.name
// to service_name on the way in is asked for the name it kept, which is also the
// name its lines come back labeled with.
func logQLName(key string) bool {
	for i := range len(key) {
		switch c := key[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return key != ""
}
