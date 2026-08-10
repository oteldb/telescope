// Package complete suggests values for the source prompt.
package complete

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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

// Rank filters candidates by query and orders prefix matches first. Matching is
// case-insensitive and ignores the detail column.
func Rank(items []Candidate, query string) []Candidate {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}

	type scored struct {
		c    Candidate
		rank int
		idx  int
	}
	var out []scored
	for i, c := range items {
		v := strings.ToLower(c.Value)
		switch {
		case strings.HasPrefix(v, q):
			out = append(out, scored{c, 0, i})
		case strings.Contains(v, q):
			out = append(out, scored{c, 1, i})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		return out[i].idx < out[j].idx
	})

	res := make([]Candidate, len(out))
	for i, s := range out {
		res[i] = s.c
	}
	return res
}
