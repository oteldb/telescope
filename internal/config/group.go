package config

import (
	"time"

	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/source"
)

// Group is several places read as one stream: an environment, usually, whose
// logs are in more than one system but whose incidents are not.
//
// The window, tail and follow are the group's own, since it is a single view
// and a view has one timeline. Every place it names has to open as it stands:
// a group cannot stop to ask, and the prompt reads one thing at a time.
type Group struct {
	Name   string   `yaml:"name"`
	Places []string `yaml:"places"`

	Range  string `yaml:"range,omitempty"`
	Tail   *int   `yaml:"tail,omitempty"`
	Follow *bool  `yaml:"follow,omitempty"`
	// Query pre-fills the filter, which is the one query the whole group is
	// read through.
	Query string `yaml:"query,omitempty"`

	// members are the places Places names, filled in by [Load], and resolveErr
	// is why one of their tokens could not be read.
	members    []source.Config
	resolveErr error
}

// Stream converts a declared group into a stream config.
func (g Group) Stream() (cfg source.Config, ready bool, err error) {
	cfg = source.Config{
		Name:      g.Name,
		Collector: source.CollectorMerge,
		Merge:     g.members,
		Tail:      defaultTail,
		Follow:    defaultFollow,
	}
	if g.Tail != nil {
		cfg.Tail = *g.Tail
	}
	if g.Follow != nil {
		cfg.Follow = *g.Follow
	}
	if cfg.Range, err = source.ParseRange(g.Range, time.Now()); err != nil {
		return source.Config{}, false, err
	}
	if g.resolveErr != nil {
		return cfg, false, g.resolveErr
	}
	if err := cfg.Validate(); err != nil {
		return cfg, false, err
	}
	return cfg, true, nil
}

// resolveGroups attaches to each group the places it names. They are resolved
// at load rather than when the group is opened so that naming a place that was
// never declared, or one that cannot open on its own, is reported as what it
// is: a mistake in the file.
func (c *Config) resolveGroups() error {
	byName := make(map[string]int, len(c.Places))
	for i, p := range c.Places {
		byName[p.Name] = i
	}
	for i, g := range c.Groups {
		if g.Name == "" {
			return errors.Errorf("group %d: name is required", i+1)
		}
		for _, name := range g.Places {
			j, ok := byName[name]
			if !ok {
				return errors.Errorf("group %q names undeclared place %q", g.Name, name)
			}
			cfg, ready, err := c.Places[j].Stream()
			switch {
			case err != nil && c.Places[j].resolveErr != nil:
				// Whether a token can be read is environment, not declaration.
				// The group carries the reason and reports it when opened.
				c.Groups[i].resolveErr = errors.Wrapf(err, "place %q", name)
			case err != nil:
				return errors.Wrapf(err, "group %q names %q", g.Name, name)
			case !ready:
				return errors.Errorf(
					"group %q names %q, which does not say enough to open: %s",
					g.Name, name, missing(cfg))
			}
			cfg.Name = name
			c.Groups[i].members = append(c.Groups[i].members, cfg)
		}
		if _, _, err := c.Groups[i].Stream(); err != nil && c.Groups[i].resolveErr == nil {
			return errors.Wrapf(err, "group %q", g.Name)
		}
	}
	return nil
}

// missing says what a place would have been asked for, which is the whole of
// why it cannot be in a group.
func missing(cfg source.Config) string {
	if err := cfg.Validate(); err != nil {
		return err.Error()
	}
	return "nothing"
}
