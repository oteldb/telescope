// Package config reads the places logs are read from and the groups they are
// read as, and remembers the ones the user reaches for.
package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/figureout"
	fyaml "github.com/go-faster/figureout/source/yaml"

	"github.com/oteldb/telescope/internal/source"
)

// appDir is the per-user directory telescope keeps its files in.
const appDir = "telescope"

// Defaults applied to a declared place that does not say otherwise.
const (
	defaultTail   = 1000
	defaultFollow = true
)

// Config is the contents of the config file.
type Config struct {
	// Places are where logs can be read from, declared once and referred to by
	// name.
	Places []Place
	// Groups are the places read as one stream.
	Groups []Group
}

// Descriptor describes the config file. Decoding it, saying what is wrong with
// one, and the JSON Schema an editor reads all derive from this one
// declaration, so a key cannot be documented as something it does not accept.
//
// What one key means for another — that a database needs a url and a command
// cannot have one, that a group's places must exist — is a schema's blind spot,
// so those are invariants: the same functions [New] runs, registered where the
// resolver can say which line of the file it read the offending value from.
var Descriptor = figureout.MustDerive(func(c *Config, s *figureout.Schema[Config]) {
	// Keyed by name so that what is wrong with an entry is reported as the
	// entry's name rather than as its position: counting places in a file to
	// find the broken one is work the message can do.
	figureout.List(s, &c.Places, "places", placeDescriptor).MergeByKey("name").
		Doc("Where logs are read from, declared once and referred to by name.")
	figureout.List(s, &c.Groups, "groups", groupDescriptor).MergeByKey("name").
		Doc("Places read as one stream.")

	figureout.Invariant(s, "place-is-usable", func(c *Config) error {
		var errs []error
		for _, p := range c.Places {
			if err := p.Validate(); err != nil {
				errs = append(errs, figureout.At(placePath(p.Name)).Errorf("%s", err))
			}
		}
		return errors.Join(errs...)
	})

	// A group is checked against the places as declared, before any token has
	// been read: a group whose place needs one telescope cannot find is not a
	// group written wrong, and carrying that reason is [Group.resolve]'s.
	// Which entry named which is a rule about two of them, so it is reported
	// with the line that did the naming rather than as a fact about the file.
	figureout.Invariant(s, "traces-name-places", func(c *Config) error {
		var errs []error
		byName := placeIndex(c.Places)
		for _, p := range c.Places {
			if _, err := traceStore(c.Places, byName, p); err != nil {
				errs = append(errs, figureout.At(placePath(p.Name)).Errorf("%s", err))
			}
		}
		return errors.Join(errs...)
	})

	figureout.Invariant(s, "group-names-places", func(c *Config) error {
		var errs []error
		byName := placeIndex(c.Places)
		for _, g := range c.Groups {
			if err := g.resolve(c.Places, byName); err != nil {
				errs = append(errs, figureout.At(groupPath(g.Name)).Errorf("%s", err))
			}
		}
		return errors.Join(errs...)
	})
})

// placePath and groupPath are where an entry was written, for a rule that is
// about one. The lists are keyed by name, so the path names the entry too.
func placePath(name string) string {
	return figureout.KeyedElementPath("places", "name", name)
}

func groupPath(name string) string {
	return figureout.KeyedElementPath("groups", "name", name)
}

// Path is where the config file is read from, honoring XDG_CONFIG_HOME.
func Path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, appDir, "config.yaml")
}

// Load reads the config file. A missing file is not an error: telescope works
// without one.
func Load() (Config, error) {
	return loadFrom(Path())
}

func loadFrom(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	return resolve(fyaml.File(path,
		fyaml.Optional(),
		// A key that is not a key is a mistake worth reporting: a config the
		// reader half understands opens half the places it names and says
		// nothing.
		fyaml.DisallowUnknownFields(),
	))
}

// Parse reads a config from bytes rather than from the file, which is how a
// file telescope is about to write is checked to be one it could read back.
func Parse(data []byte) (Config, error) {
	return resolve(fyaml.Bytes(data, fyaml.DisallowUnknownFields()))
}

func resolve(src figureout.Source) (Config, error) {
	c, _, err := Descriptor.Resolve(src)
	if err != nil {
		return Config{}, said(err)
	}
	return New(c.Places, c.Groups)
}

// said rewrites what the resolver reports into the sentences telescope writes
// everywhere else. A diagnostic's code is for a program reading the report, and
// the only reader here is somebody looking at a start screen that will not open.
// noValue is how the resolver words an absent field with nothing to fall back
// on, which is the one message worth rewriting: every other one is already a
// sentence about the value.
const noValue = "no value provided and no default"

func said(err error) error {
	var diags figureout.Diagnostics
	if !errors.As(err, &diags) {
		return err
	}
	var out []error
	for _, d := range diags {
		if d.Severity != figureout.SeverityError {
			continue
		}
		var sb strings.Builder
		switch {
		case d.Message == noValue:
			sb.WriteString(d.FieldPath)
			sb.WriteString(" is required")
		case strings.Contains(d.Message, d.FieldPath):
			// A diagnostic about a whole collection names the entry it is
			// about, which is more than the path can say.
			sb.WriteString(d.Message)
		default:
			sb.WriteString(d.FieldPath)
			sb.WriteString(": ")
			sb.WriteString(d.Message)
		}
		if d.Origin != nil {
			sb.WriteString(" (")
			sb.WriteString(d.Origin.String())
			sb.WriteString(")")
		}
		out = append(out, errors.New(sb.String()))
	}
	if len(out) == 0 {
		return err
	}
	return errors.Join(out...)
}

// New resolves declarations that did not come from a file, which is how a test
// says what the config file would have said. Reading the file is [Load].
func New(places []Place, groups []Group) (Config, error) {
	c := Config{Places: places, Groups: groups}
	if err := c.resolvePlaces(); err != nil {
		return Config{}, err
	}
	if err := c.resolveTraces(); err != nil {
		return Config{}, err
	}
	if err := c.resolveGroups(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// resolveTraces attaches to each place the store it named.
//
// It runs after every place has been resolved, so the store's own token is the
// one already read: a place naming a store is not a place borrowing a token,
// and the store is reached the way the store says.
func (c *Config) resolveTraces() error {
	byName := placeIndex(c.Places)
	for i, p := range c.Places {
		store, err := traceStore(c.Places, byName, p)
		switch {
		case err != nil:
			return errors.Wrapf(err, "place %q", p.Name)
		case store == nil:
			continue
		}
		c.Places[i].linked, c.Places[i].linkErr = store.resolved, store.resolveErr
	}
	return nil
}

// traceStore is the place p named for its traces, nil for one that named none,
// and why what it named cannot be a store.
//
// It says nothing about which place did the naming: [Config.resolveTraces] is
// told by the caller, and the invariant reports it against the entry it was
// read from, where the path names it already.
func traceStore(places []Place, byName map[string]int, p Place) (*Place, error) {
	if !p.Traces.Links() {
		return nil, nil
	}
	j, ok := byName[p.Traces.Name]
	if !ok {
		return nil, errors.Errorf("names undeclared place %q for its traces", p.Traces.Name)
	}
	if store := &places[j]; store.ReadsTraces() {
		return store, nil
	}
	return nil, errors.Errorf(
		"names %q for its traces, and %q reads logs: a trace store is a place of type %s",
		p.Traces.Name, p.Traces.Name, strings.Join(traceTypeNames, " or "))
}

// resolvePlaces validates each place and reads the endpoint of each one that
// has one. A token may cost a keyring prompt, so it is read once per run and
// not once per place that asks for it.
//
// Naming the same place twice is a mistake in the file; failing to read a token
// is not, and is carried on the place instead.
func (c *Config) resolvePlaces() error {
	seen := make(map[string]bool, len(c.Places))
	for i, p := range c.Places {
		if err := p.Validate(); err != nil {
			// Named rather than numbered wherever possible: counting entries in
			// a file to find the broken one is work the message can do.
			if p.Name != "" {
				return errors.Wrapf(err, "place %q", p.Name)
			}
			return errors.Wrapf(err, "place %d", i+1)
		}
		if seen[p.Name] {
			return errors.Errorf("place %q is declared twice", p.Name)
		}
		seen[p.Name] = true

		if !p.Collector().IsRemoteAPI() && !p.ReadsTraces() {
			continue
		}
		c.Places[i].resolved, c.Places[i].resolveErr = p.Endpoint()
	}
	for _, g := range c.Groups {
		if !seen[g.Name] {
			seen[g.Name] = true
			continue
		}
		if slices.ContainsFunc(c.Places, func(p Place) bool { return p.Name == g.Name }) {
			return errors.Errorf("group %q is also the name of a place", g.Name)
		}
		return errors.Errorf("group %q is declared twice", g.Name)
	}
	return nil
}

// Endpoints returns the places that are queried over HTTP, which are the ones a
// query can be written against without naming anything else. A place whose
// token is unreadable is left out rather than offered as a choice that fails on
// use, and the reason is reported instead.
func (c Config) Endpoints() ([]source.Endpoint, error) {
	var (
		out  []source.Endpoint
		errs []error
	)
	for _, p := range c.Places {
		if !p.Collector().IsRemoteAPI() {
			continue
		}
		if p.resolveErr != nil {
			errs = append(errs, p.resolveErr)
			continue
		}
		out = append(out, p.resolved)
	}
	return out, errors.Join(errs...)
}

// Target reconstructs the value that would be typed into the prompt for cfg,
// so a declared or remembered place can be offered back verbatim.
func Target(cfg source.Config) string {
	switch cfg.Collector {
	case source.CollectorJournal:
		if cfg.Unit == "" {
			return ""
		}
		if cfg.UserUnit {
			return source.UserUnitPrefix + cfg.Unit
		}
		return cfg.Unit
	case source.CollectorKubectl:
		target := cfg.Target
		if cfg.Namespace != "" {
			target = cfg.Namespace + "/" + target
		}
		if cfg.Container != "" {
			target += ":" + cfg.Container
		}
		return target
	case source.CollectorDocker:
		return cfg.Container
	case source.CollectorVictoriaLogs, source.CollectorLoki:
		return cfg.Target
	default:
		return cfg.Args
	}
}
