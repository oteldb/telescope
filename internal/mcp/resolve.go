package mcp

import (
	"slices"
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// stream is what a tool's place argument named, whether it named a place or a
// group. Both are read the same way once resolved, and an agent that was told
// the names by places should not have to know which list one came from.
//
// A place that does not open as it stands is still returned: what it is missing
// is a target to read, and a database asked what its fields are needs none.
func stream(cfg config.Config, name string) (source.Config, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return source.Config{}, errors.Errorf("name a place: %s", declared(cfg))
	}
	for _, p := range cfg.Places {
		if p.Name != name {
			continue
		}
		src, _, err := p.Stream()
		if err != nil {
			return source.Config{}, errors.Wrapf(err, "place %q", name)
		}
		return src, nil
	}
	for _, g := range cfg.Groups {
		if g.Name != name {
			continue
		}
		src, _, err := g.Stream()
		if err != nil {
			return source.Config{}, errors.Wrapf(err, "group %q", name)
		}
		return src, nil
	}
	return source.Config{}, errors.Errorf("no place named %q: %s", name, declared(cfg))
}

// declared is what could have been named instead, since a wrong name is most
// often a near miss and the list is short enough to write out.
func declared(cfg config.Config) string {
	var names []string
	for _, p := range cfg.Places {
		names = append(names, p.Name)
	}
	for _, g := range cfg.Groups {
		names = append(names, g.Name)
	}
	if len(names) == 0 {
		return "the config declares none"
	}
	return "the ones declared are " + strings.Join(names, ", ")
}

// withRange overrides the window a place is read over. A merge's children each
// carry their own, so the override has to reach them: the group is one view and
// one timeline, and a child left on the place's own window would answer for a
// different interval than the rest.
func withRange(src source.Config, spec string) (source.Config, error) {
	if strings.TrimSpace(spec) == "" {
		return src, nil
	}
	r, err := source.ParseRange(spec, time.Now())
	if err != nil {
		return source.Config{}, err
	}
	src.Range = r
	src.Merge = slices.Clone(src.Merge)
	for i := range src.Merge {
		src.Merge[i].Range = r
	}
	return src, nil
}
