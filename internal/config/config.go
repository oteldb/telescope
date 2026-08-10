// Package config reads the sources declared by the user and remembers the ones
// they reach for.
package config

import (
	"os"
	"path/filepath"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"

	"github.com/oteldb/telescope/internal/source"
)

// appDir is the per-user directory telescope keeps its files in.
const appDir = "telescope"

// Defaults applied to a declared source that does not say otherwise.
const (
	defaultTail   = 1000
	defaultFollow = true
)

// Source is a log stream declared in the config file.
type Source struct {
	Name string `yaml:"name"`

	Transport string `yaml:"transport,omitempty"`
	Host      string `yaml:"host,omitempty"`
	Collector string `yaml:"collector"`

	Unit       string `yaml:"unit,omitempty"`
	UserUnit   bool   `yaml:"user_unit,omitempty"`
	Namespace  string `yaml:"namespace,omitempty"`
	Target     string `yaml:"target,omitempty"`
	Container  string `yaml:"container,omitempty"`
	Args       string `yaml:"args,omitempty"`
	KubeConfig string `yaml:"kubeconfig,omitempty"`

	// Sudo runs the collector under sudo -n.
	Sudo bool `yaml:"sudo,omitempty"`

	// Tail and Follow are pointers so that "tail: 0" and "follow: false" are
	// distinguishable from being unset.
	Tail   *int  `yaml:"tail,omitempty"`
	Follow *bool `yaml:"follow,omitempty"`

	// Query pre-fills the grep filter.
	Query string `yaml:"query,omitempty"`
}

// Config is the contents of the config file.
type Config struct {
	Sources []Source `yaml:"sources"`
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
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Config{}, errors.Wrapf(err, "parse %s", path)
	}
	for i, s := range c.Sources {
		if err := s.Validate(); err != nil {
			return Config{}, errors.Wrapf(err, "source %d", i+1)
		}
	}
	return c, nil
}

// Validate reports whether the declared source can be turned into a stream.
func (s Source) Validate() error {
	if s.Name == "" {
		return errors.New("name is required")
	}
	_, err := s.Stream()
	return err
}

// Stream converts a declared source into a runnable stream config.
func (s Source) Stream() (source.Config, error) {
	cfg := source.Config{
		Transport:  source.Transport(or(s.Transport, string(source.TransportLocal))),
		Host:       s.Host,
		Collector:  source.Collector(s.Collector),
		Unit:       s.Unit,
		UserUnit:   s.UserUnit,
		Namespace:  s.Namespace,
		Target:     s.Target,
		Container:  s.Container,
		Args:       s.Args,
		KubeConfig: s.KubeConfig,
		Elevate:    s.Sudo,
		Tail:       defaultTail,
		Follow:     defaultFollow,
	}
	if s.Tail != nil {
		cfg.Tail = *s.Tail
	}
	if s.Follow != nil {
		cfg.Follow = *s.Follow
	}

	switch cfg.Transport {
	case source.TransportLocal, source.TransportSSH:
	default:
		return source.Config{}, errors.Errorf("unknown transport %q", s.Transport)
	}
	// A journal unit may carry the compact user/ prefix, as in the prompt.
	if cfg.Collector == source.CollectorJournal && cfg.Unit != "" {
		unit, user := source.ParseJournalTarget(cfg.Unit)
		cfg.Unit = unit
		cfg.UserUnit = cfg.UserUnit || user
	}
	// The kubectl target may carry the compact ns/pod:container form.
	if cfg.Collector == source.CollectorKubectl && cfg.Namespace == "" && cfg.Container == "" {
		if ns, target, container := source.ParseKubeTarget(cfg.Target); ns != "" || container != "" {
			cfg.Namespace, cfg.Target, cfg.Container = ns, target, container
		}
	}
	if err := cfg.Validate(); err != nil {
		return source.Config{}, err
	}
	return cfg, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
