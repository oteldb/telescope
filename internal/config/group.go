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
	// asks is what the places that name no target read, when any of them do
	// not. A group cannot ask once per place, but it can ask once: the same
	// deployment usually has the same name on every cluster.
	asks source.Collector
}

// Asks is what the group must be given before it can open, and whether it must
// be given anything at all. It is a collector rather than a value because what
// is typed means different things to different ones: a pod to kubectl, a
// container to docker, a query to a log database.
func (g Group) Asks() (source.Collector, bool) { return g.asks, g.asks != "" }

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
	if _, asks := g.Asks(); asks {
		// Not ready, and not broken either: what is missing is what the prompt
		// is for. The shape of the group was checked when it was read.
		return cfg, false, nil
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
				// A group cannot ask once per place, so every place that does
				// not say enough has to be asking for the same thing.
				if asks := c.Groups[i].asks; asks != "" && asks != cfg.Collector {
					return errors.Errorf(
						"group %q names %q and a %s place, which do not ask for the same thing: "+
							"one target cannot be both",
						g.Name, name, asks)
				}
				c.Groups[i].asks = cfg.Collector
			}
			cfg.Name = name
			c.Groups[i].members = append(c.Groups[i].members, cfg)
		}
		if err := c.Groups[i].shape(); err != nil {
			return errors.Wrapf(err, "group %q", g.Name)
		}
	}
	return nil
}

// shape checks what is wrong with the group itself rather than with any place
// it names, which is the only thing a place that is still being asked about
// cannot excuse.
func (g Group) shape() error {
	if len(g.members) < 2 {
		return errors.New("a group reads two or more places")
	}
	if _, asks := g.Asks(); asks {
		return nil
	}
	_, _, err := g.Stream()
	if g.resolveErr != nil {
		return nil
	}
	return err
}
