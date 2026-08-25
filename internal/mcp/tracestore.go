package mcp

import (
	"strings"

	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// traceStore resolves what a trace tool's place argument named, and what it meant by
// naming nothing.
//
// Either name works: a store, or a log place that names one. An agent that
// lifted a trace id off a log line has the log place in hand and no reason to
// know which store the ids go to, and making it ask twice to find out would be
// charging it for a link the config already wrote down.
//
// Naming nothing works too, where the config leaves no room for doubt. A trace
// id is a global thing — it identifies the request and not the place it was
// stored — so an agent holding one has already said everything that identifies
// it. Where a single store is declared there is only one door to try, and
// making the id useless without a name it could not have guessed is a round
// trip that answers a question the config already answered.
func traceStore(cfg config.Config, name string) (source.Endpoint, error) {
	if name = strings.TrimSpace(name); name == "" {
		return theStore(cfg)
	}
	for _, p := range cfg.Places {
		if p.Name != name {
			continue
		}
		at, ok, err := p.TraceEndpoint()
		if err != nil {
			// Unwrapped: what a place says about itself already names it.
			return source.Endpoint{}, err
		}
		if !ok {
			return source.Endpoint{}, errors.Errorf(
				"%q reads no traces: %s", name, storesAre(cfg))
		}
		return at, nil
	}
	if _, ok := groupOf(cfg, name); ok {
		return source.Endpoint{}, errors.Errorf(
			"%q is a group, and a trace is read from one store rather than several: %s",
			name, storesAre(cfg))
	}
	return source.Endpoint{}, errors.Errorf("no place named %q: %s", name, storesAre(cfg))
}

// theStore is the one store there is, where there is one. Several is not a
// thing to guess between: a trace id read at the wrong store comes back "not
// found", which reads as the trace having aged out rather than as the question
// having gone to the wrong place.
func theStore(cfg config.Config) (source.Endpoint, error) {
	var (
		at    source.Endpoint
		found int
	)
	for _, p := range cfg.Places {
		if !p.ReadsTraces() {
			continue
		}
		e, ok, err := p.TraceEndpoint()
		if err != nil || !ok {
			continue
		}
		found++
		at = e
	}
	switch found {
	case 0:
		return source.Endpoint{}, errors.Errorf("no place reads traces: %s", storesAre(cfg))
	case 1:
		return at, nil
	default:
		return source.Endpoint{}, errors.Errorf(
			"name which store to read: %s. A trace id is not enough to tell them "+
				"apart, and asking the wrong one answers that the trace is not there",
			storesAre(cfg))
	}
}

func groupOf(cfg config.Config, name string) (config.Group, bool) {
	for _, g := range cfg.Groups {
		if g.Name == name {
			return g, true
		}
	}
	return config.Group{}, false
}

// storesAre is what could have been named instead. It lists the stores and the
// places that reach one, since either is a name a trace tool takes.
func storesAre(cfg config.Config) string {
	var stores, through []string
	for _, p := range cfg.Places {
		switch {
		case p.ReadsTraces():
			stores = append(stores, p.Name)
		default:
			if _, ok, err := p.TraceEndpoint(); ok && err == nil {
				through = append(through, p.Name)
			}
		}
	}
	if len(stores) == 0 {
		return "the config declares none, and a trace store is a place of type tempo or jaeger"
	}
	said := "the stores are " + strings.Join(stores, ", ")
	if len(through) > 0 {
		said += ", also reachable by naming " + strings.Join(through, ", ")
	}
	return said
}
