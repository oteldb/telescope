// Package config reads the places logs are read from and the groups they are
// read as, and remembers the ones the user reaches for.
package config

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"

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
	Places []Place `yaml:"places,omitempty"`
	// Groups are the places read as one stream.
	Groups []Group `yaml:"groups,omitempty"`
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
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, errors.Wrap(err, "read config")
	}

	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	// A key that is not a key is a mistake worth reporting: a config the reader
	// half understands opens half the places it names and says nothing.
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, errors.Wrapf(err, "parse %s", path)
	}
	return New(c.Places, c.Groups)
}

// New resolves declarations that did not come from a file, which is how a test
// says what the config file would have said. Reading the file is [Load].
func New(places []Place, groups []Group) (Config, error) {
	c := Config{Places: places, Groups: groups}
	if err := c.resolvePlaces(); err != nil {
		return Config{}, err
	}
	if err := c.resolveGroups(); err != nil {
		return Config{}, err
	}
	return c, nil
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

		if !p.Collector().IsRemoteAPI() {
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
