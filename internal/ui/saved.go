package ui

import (
	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// saved is what the config file declares, as the start screen offers it: the
// groups first, since a group is most of the reason for declaring places, and
// then the places themselves.
//
// One index runs over both, because the list is one list.
type saved struct {
	groups []config.Group
	places []config.Place
}

func (s saved) len() int { return len(s.groups) + len(s.places) }

// group returns the entry at i when it is a group.
func (s saved) group(i int) (config.Group, bool) {
	if i < 0 || i >= len(s.groups) {
		return config.Group{}, false
	}
	return s.groups[i], true
}

// place returns the entry at i when it is a place, which is the only kind that
// can be picked to be read alongside others.
func (s saved) place(i int) (config.Place, bool) {
	i -= len(s.groups)
	if i < 0 || i >= len(s.places) {
		return config.Place{}, false
	}
	return s.places[i], true
}

// readsTraces reports whether anything here declares a trace store, which is
// what decides whether the key that searches one is worth offering.
func (s saved) readsTraces() bool {
	for _, p := range s.places {
		if !p.Traces.IsZero() {
			return true
		}
	}
	return false
}

func (s saved) name(i int) string {
	if g, ok := s.group(i); ok {
		return g.Name
	}
	if p, ok := s.place(i); ok {
		return p.Name
	}
	return ""
}

// query is what the entry pre-fills the filter with.
func (s saved) query(i int) string {
	if g, ok := s.group(i); ok {
		return g.Query
	}
	if p, ok := s.place(i); ok {
		return p.Query
	}
	return ""
}

// asks is what the entry must be given before it can open, for a group whose
// places name no target. A place asks for itself, through the manual flow.
func (s saved) asks(i int) (source.Collector, bool) {
	g, ok := s.group(i)
	if !ok {
		return "", false
	}
	return g.Asks()
}

// stream is the entry as a stream to open, and whether it says enough to open
// without asking anything.
func (s saved) stream(i int) (source.Config, bool, error) {
	if g, ok := s.group(i); ok {
		return g.Stream()
	}
	if p, ok := s.place(i); ok {
		return p.Stream()
	}
	return source.Config{}, false, nil
}
