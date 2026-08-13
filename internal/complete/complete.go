// Package complete suggests values for the source prompt.
package complete

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sahilm/fuzzy"

	"github.com/oteldb/telescope/internal/source"
)

// Timeout bounds how long a listing command may run.
const Timeout = 5 * time.Second

// Candidate is one suggestion.
type Candidate struct {
	Value string
	// State is the lifecycle word a collector reports for this candidate, kept
	// apart from Detail so the view can color it. Empty when there is none.
	State string
	// Detail is shown dimmed next to the value, never inserted.
	Detail string
	// Matched holds the rune offsets in Value that the query matched, so the
	// view can show why a candidate is in the list. Set by [Rank].
	Matched []int
}

// Field is what is being completed.
type Field int

// Completable fields.
const (
	// FieldHost completes ssh destinations from the local ssh config.
	FieldHost Field = iota
	// FieldTarget completes the collector's target: a unit, a pod or a container.
	FieldTarget
	// FieldKubeConfig completes kubeconfig paths found on the host.
	FieldKubeConfig
	// FieldKubeContext completes the contexts inside the chosen kubeconfig.
	FieldKubeContext
)

// Request describes what to complete and where to look for it.
type Request struct {
	Field     Field
	Transport source.Transport
	Host      string
	Collector source.Collector

	// Elevate and KubeConfig mirror the stream config, so a listing runs with
	// the same privileges and against the same cluster as the logs will.
	Elevate     bool
	KubeConfig  string
	KubeContext string
}

// Key identifies the result set, so it can be cached and stale replies dropped.
func (r Request) Key() string {
	if r.Field == FieldHost {
		return "host"
	}
	return fmt.Sprintf("%d|%s|%s|%s|%t|%s|%s",
		r.Field, r.Transport, r.Host, r.Collector, r.Elevate, r.KubeConfig, r.KubeContext)
}

// Fetch collects the candidates for r. Hosts are read locally; everything else
// is listed by running a command through the request's transport.
func Fetch(ctx context.Context, r Request) ([]Candidate, error) {
	if r.Field == FieldHost {
		return Hosts(), nil
	}
	return list(ctx, r)
}

// Match tiers, best first. What was typed literally beats the same letters in
// another case, which beats letters merely appearing in order.
const (
	tierExact = iota
	tierFold
	tierFuzzy
)

// Rank filters candidates by query and orders the survivors.
//
// A query may carry "field:value" terms, which narrow the set before anything
// is ranked; attr resolves them. A nil attr means the candidates have no fields
// to filter on, and the whole query is then taken literally — which is what a
// prompt holding a query language of its own, such as LogsQL, needs. See
// [ParseQuery].
//
// The rest of the query matches fuzzily: its characters have to appear in order
// but need not be adjacent, so "otdb0" finds "oteldb-0". Ordering is by tier
// first, so a literal substring is never buried under a scattered match, and by
// the matcher's score within a tier. A prefix beats a hit further along. Only
// the value is matched, never the detail.
//
// An empty query keeps the given order, which is how recently used values stay
// on top.
func Rank(items []Candidate, query string, attr Attr) []Candidate {
	terms, text := []Term(nil), query
	if attr != nil {
		terms, text = ParseQuery(query)
	}
	if len(terms) > 0 {
		kept := make([]Candidate, 0, len(items))
		for _, c := range items {
			if keep(c, terms, attr) {
				kept = append(kept, c)
			}
		}
		items = kept
	}

	q := strings.TrimSpace(text)
	if q == "" {
		return items
	}
	lower := strings.ToLower(q)

	// Every substring match is also a fuzzy match, so this is the full set,
	// already ordered by how close each one is.
	matches := fuzzy.FindFrom(q, candidateNames(items))

	type ranked struct {
		c    Candidate
		tier int
		// atStart sorts a prefix above a hit in the middle.
		atStart int
	}
	out := make([]ranked, 0, len(matches))
	for _, m := range matches {
		v := items[m.Index].Value
		folded := strings.ToLower(v)

		c := items[m.Index]
		c.Matched = m.MatchedIndexes

		r := ranked{c: c, tier: tierFuzzy, atStart: 1}
		switch {
		case strings.Contains(v, q):
			r.tier = tierExact
		case strings.Contains(folded, lower):
			r.tier = tierFold
		}
		if strings.HasPrefix(folded, lower) {
			r.atStart = 0
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b ranked) int {
		if c := cmp.Compare(a.tier, b.tier); c != 0 {
			return c
		}
		return cmp.Compare(a.atStart, b.atStart)
	})

	res := make([]Candidate, len(out))
	for i, r := range out {
		res[i] = r.c
	}
	return res
}

// candidateNames adapts candidates to the matcher.
type candidateNames []Candidate

func (c candidateNames) String(i int) string { return c[i].Value }
func (c candidateNames) Len() int            { return len(c) }
