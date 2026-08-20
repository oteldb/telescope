package config

import (
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/figureout"

	"github.com/oteldb/telescope/internal/source"
)

// Group is several places read as one stream: an environment, usually, whose
// logs are in more than one system but whose incidents are not.
//
// The window, tail and follow are the group's own, since it is a single view
// and a view has one timeline. Every place it names has to open as it stands:
// a group cannot stop to ask, and the prompt reads one thing at a time.
type Group struct {
	Name   string
	Places []string

	Range  string
	Tail   figureout.OptionalOf[int]
	Follow figureout.OptionalOf[bool]
	// Query pre-fills the filter, which is the one query the whole group is
	// read through.
	Query string

	// members are the places Places names, filled in by [Load], and resolveErr
	// is why one of their tokens could not be read.
	members    []source.Config
	resolveErr error
	// asks is what the places that name no target read, when any of them do
	// not. A group cannot ask once per place, but it can ask once: the same
	// deployment usually has the same name on every cluster.
	asks source.Collector
}

// groupDescriptor describes a [Group] as it is written in the file.
var groupDescriptor = figureout.MustDerive(func(g *Group, s *figureout.Schema[Group]) {
	figureout.Explicit(s, &g.Name, "name").NonEmpty().
		Doc("Shown in the picker.")
	figureout.Explicit(s, &g.Places, "places").MinItems(2).
		Doc("The places read as one timeline, by name.")

	figureout.Value(s, &g.Range, "range").
		Doc("The window read: 1h, today, 6h..1h.")
	figureout.Optional(s, &g.Tail, "tail").AtLeast(0).DocumentDefault(defaultTail).
		Doc("Lines of history to open with, 0 for all.")
	figureout.Optional(s, &g.Follow, "follow").DocumentDefault(defaultFollow).
		Doc("Keep streaming.")
	figureout.Value(s, &g.Query, "query").
		Doc("Pre-fills the filter, which is the one query the whole group is read through.")

	figureout.IgnoreRecursive(s, &g.members, figureout.Reason("resolved at load"))
	figureout.Ignore(s, &g.resolveErr, figureout.Reason("resolved at load"))
	figureout.Ignore(s, &g.asks, figureout.Reason("resolved at load"))
})

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
		Tail:      g.Tail.OrElse(defaultTail),
		Follow:    g.Follow.OrElse(defaultFollow),
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
	byName := placeIndex(c.Places)
	for i, g := range c.Groups {
		if g.Name == "" {
			return errors.Errorf("group %d: name is required", i+1)
		}
		if err := c.Groups[i].resolve(c.Places, byName); err != nil {
			return errors.Wrapf(err, "group %q", g.Name)
		}
	}
	return nil
}

// placeIndex is where each place is, for a group that names one.
func placeIndex(places []Place) map[string]int {
	byName := make(map[string]int, len(places))
	for i, p := range places {
		byName[p.Name] = i
	}
	return byName
}

// resolve attaches the places the group names, and reports what is wrong with
// the group itself.
//
// It says nothing about which group it is: the caller knows, and it is
// registered as an invariant too, where the path already names one.
func (g *Group) resolve(places []Place, byName map[string]int) error {
	for _, name := range g.Places {
		j, ok := byName[name]
		if !ok {
			return errors.Errorf("names undeclared place %q", name)
		}
		cfg, ready, err := places[j].Stream()
		switch {
		case err != nil && places[j].resolveErr != nil:
			// Whether a token can be read is environment, not declaration. The
			// group carries the reason and reports it when opened, as the place
			// worded it: that reason names the place already, and saying it
			// again here reads as two places having failed.
			g.resolveErr = err
		case err != nil:
			return errors.Wrapf(err, "names %q", name)
		case !ready:
			// A group cannot ask once per place, so every place that does not
			// say enough has to be asking for the same thing.
			if g.asks != "" && g.asks != cfg.Collector {
				return errors.Errorf(
					"names %q and a %s place, which do not ask for the same thing: "+
						"one target cannot be both",
					name, g.asks)
			}
			g.asks = cfg.Collector
		}
		cfg.Name = name
		g.members = append(g.members, cfg)
	}
	return g.shape()
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
