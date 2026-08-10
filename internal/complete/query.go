package complete

import (
	"strings"

	"github.com/oteldb/telescope/internal/source"
)

// Term is one "field:value" filter parsed out of a query, in the shape GitHub
// and Sourcegraph use. A term narrows the suggestions; what is left of the
// query still matches fuzzily.
type Term struct {
	Field string
	Value string
	// Negate keeps the candidates the term does not describe, as in "-ns:kube".
	Negate bool
}

// Query fields. A term is only recognized under one of these names, so a value
// that merely contains a colon, such as a pod's "name:container", is still
// searched for literally.
const (
	fieldNamespace = "ns"
	fieldKind      = "kind"
	fieldName      = "name"
	fieldContainer = "container"
	fieldScope     = "scope"
	fieldImage     = "image"
	fieldState     = "state"
)

// queryFields maps every accepted spelling onto its field.
var queryFields = map[string]string{
	"ns":        fieldNamespace,
	"namespace": fieldNamespace,
	"kind":      fieldKind,
	"type":      fieldKind,
	"name":      fieldName,
	"unit":      fieldName,
	"pod":       fieldName,
	"container": fieldContainer,
	"c":         fieldContainer,
	"scope":     fieldScope,
	"image":     fieldImage,
	"state":     fieldState,
	"status":    fieldState,
}

// ParseQuery splits a query such as "ns:oteldb -kind:pod api" into its filter
// terms and the text left for fuzzy matching.
//
// A query with no terms is returned unchanged, spacing and all, so the common
// case matches exactly what was typed.
func ParseQuery(query string) (terms []Term, text string) {
	if !strings.Contains(query, ":") {
		return nil, query
	}
	var rest []string
	for tok := range strings.FieldsSeq(query) {
		if t, ok := parseTerm(tok); ok {
			terms = append(terms, t)
			continue
		}
		rest = append(rest, tok)
	}
	if len(terms) == 0 {
		return nil, query
	}
	return terms, strings.Join(rest, " ")
}

// QueryText is what remains of a query once its terms are removed, which is
// the part meant literally.
func QueryText(query string) string {
	_, text := ParseQuery(query)
	return strings.TrimSpace(text)
}

func parseTerm(tok string) (Term, bool) {
	var t Term
	if rest, ok := strings.CutPrefix(tok, "-"); ok {
		t.Negate, tok = true, rest
	}
	name, value, ok := strings.Cut(tok, ":")
	if !ok {
		return Term{}, false
	}
	field, known := queryFields[strings.ToLower(name)]
	if !known {
		return Term{}, false
	}
	t.Field, t.Value = field, value
	return t, true
}

// Fields names the query fields a collector's candidates answer to, so the
// prompt can advertise what is filterable.
func Fields(c source.Collector) []string {
	switch c {
	case source.CollectorKubectl:
		return []string{fieldNamespace, fieldKind, fieldName, fieldContainer, fieldState}
	case source.CollectorJournal:
		return []string{fieldName, fieldScope, fieldState}
	case source.CollectorDocker:
		return []string{fieldName, fieldImage, fieldState}
	default:
		return nil
	}
}

// Attr resolves a query field for a candidate, returning "" when the candidate
// has nothing under that field. See [AttrFor].
type Attr func(c Candidate, field string) string

// AttrFor returns the attributes of a collector's candidates. They are read
// back out of the candidate's own value, which is the compact target syntax, so
// a remembered value filters like a listed one.
func AttrFor(c source.Collector) Attr {
	switch c {
	case source.CollectorKubectl:
		return kubeAttr
	case source.CollectorJournal:
		return journalAttr
	case source.CollectorDocker:
		return dockerAttr
	default:
		return baseAttr
	}
}

func kubeAttr(c Candidate, field string) string {
	ns, target, container := source.ParseKubeTarget(c.Value)
	switch field {
	case fieldNamespace:
		return ns
	case fieldContainer:
		return container
	case fieldKind, fieldName:
		kind, name, ok := strings.Cut(target, "/")
		if !ok || !source.IsKubeKind(kind) {
			kind, name = "pod", target
		}
		if field == fieldKind {
			return kind
		}
		return name
	}
	return baseAttr(c, field)
}

func journalAttr(c Candidate, field string) string {
	unit, user := source.ParseJournalTarget(c.Value)
	switch field {
	case fieldName:
		return unit
	case fieldScope:
		if user {
			return "user"
		}
		return "system"
	}
	return baseAttr(c, field)
}

func dockerAttr(c Candidate, field string) string {
	switch field {
	case fieldName:
		return c.Value
	case fieldImage:
		return c.Detail
	}
	return baseAttr(c, field)
}

// baseAttr resolves the fields every candidate has, whatever lists it.
func baseAttr(c Candidate, field string) string {
	if field == fieldState {
		return c.State
	}
	return ""
}

// keep reports whether c satisfies every term. A term with no value, "ns:",
// asks for the candidates that have one at all.
func keep(c Candidate, terms []Term, attr Attr) bool {
	if attr == nil {
		attr = baseAttr
	}
	for _, t := range terms {
		got := attr(c, t.Field)
		ok := got != ""
		if t.Value != "" {
			ok = strings.Contains(strings.ToLower(got), strings.ToLower(t.Value))
		}
		if ok == t.Negate {
			return false
		}
	}
	return true
}
