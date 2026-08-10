// Package config reads the sources declared by the user and remembers the ones
// they reach for.
package config

import (
	"os"
	"path/filepath"
	"strings"

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

	// Endpoint names one of the declared endpoints, for a collector that reads
	// from a log database rather than a host.
	Endpoint string `yaml:"endpoint,omitempty"`
	// Resolved is the endpoint Endpoint names, filled in by [Load].
	Resolved source.Endpoint `yaml:"-"`
	// resolveErr is why the endpoint's token could not be read. It is kept
	// rather than returned so one unreadable token does not take the whole
	// config down with it.
	resolveErr error

	Unit       string `yaml:"unit,omitempty"`
	UserUnit   bool   `yaml:"user_unit,omitempty"`
	Namespace  string `yaml:"namespace,omitempty"`
	Target     string `yaml:"target,omitempty"`
	Container  string `yaml:"container,omitempty"`
	Args       string `yaml:"args,omitempty"`
	KubeConfig string `yaml:"kubeconfig,omitempty"`
	Context    string `yaml:"context,omitempty"`

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
	// Endpoints are the log APIs sources may read from, declared once and
	// referred to by name.
	Endpoints []Endpoint `yaml:"endpoints,omitempty"`
	Sources   []Source   `yaml:"sources"`
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
	if err := c.resolveEndpoints(); err != nil {
		return Config{}, err
	}
	for i, s := range c.Sources {
		if err := s.Validate(); err != nil {
			return Config{}, errors.Wrapf(err, "source %d", i+1)
		}
	}
	return c, nil
}

// resolveEndpoints attaches each source's endpoint to it. Naming an endpoint
// that was never declared is a mistake in the file; failing to read its token
// is not, and is carried on the source instead.
func (c *Config) resolveEndpoints() error {
	byName := make(map[string]Endpoint, len(c.Endpoints))
	for i, e := range c.Endpoints {
		if err := e.Validate(); err != nil {
			return errors.Wrapf(err, "endpoint %d", i+1)
		}
		if _, ok := byName[e.Name]; ok {
			return errors.Errorf("endpoint %q is declared twice", e.Name)
		}
		byName[e.Name] = e
	}
	for i, s := range c.Sources {
		if s.Endpoint == "" {
			continue
		}
		e, ok := byName[s.Endpoint]
		if !ok {
			return errors.Errorf("source %q names undeclared endpoint %q", s.Name, s.Endpoint)
		}
		c.Sources[i].Resolved, c.Sources[i].resolveErr = e.Resolve()
	}
	return nil
}

// Validate reports whether the declared source is usable. A source that names
// a cluster but no pod is valid: it pre-fills the prompt rather than opening
// straight away.
func (s Source) Validate() error {
	if s.Name == "" {
		return errors.New("name is required")
	}
	// Whether a token can be read is environment, not declaration: it is
	// reported when the source is opened, not when the file is parsed.
	s.resolveErr = nil
	_, _, err := s.Stream()
	return err
}

// Stream converts a declared source into a stream config.
//
// Ready reports whether it names everything needed to open. When it does not,
// the config is still returned so the prompt can start from it, which is how a
// source that pins a host and a kubeconfig but leaves the pod open behaves.
func (s Source) Stream() (cfg source.Config, ready bool, err error) {
	cfg = source.Config{
		Transport:   source.Transport(or(s.Transport, string(source.TransportLocal))),
		Host:        s.Host,
		Collector:   source.Collector(s.Collector),
		Unit:        s.Unit,
		UserUnit:    s.UserUnit,
		Namespace:   s.Namespace,
		Target:      s.Target,
		Container:   s.Container,
		Args:        s.Args,
		KubeConfig:  s.KubeConfig,
		KubeContext: s.Context,
		Endpoint:    s.Resolved,
		Elevate:     s.Sudo,
		Tail:        defaultTail,
		Follow:      defaultFollow,
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
		return source.Config{}, false, errors.Errorf("unknown transport %q", s.Transport)
	}
	switch cfg.Collector {
	case source.CollectorJournal, source.CollectorKubectl, source.CollectorDocker,
		source.CollectorCommand, source.CollectorVictoriaLogs:
	default:
		return source.Config{}, false, errors.Errorf("unknown collector %q", s.Collector)
	}
	if cfg.Collector.IsRemoteAPI() {
		if s.Endpoint == "" {
			return source.Config{}, false, errors.Errorf("collector %q requires an endpoint", s.Collector)
		}
		if s.resolveErr != nil {
			return cfg, false, s.resolveErr
		}
	}
	if cfg.Transport == source.TransportSSH && strings.TrimSpace(cfg.Host) == "" && !cfg.Collector.IsRemoteAPI() {
		return source.Config{}, false, errors.New("ssh transport requires a host")
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
	// Anything still missing is something the prompt can ask for.
	return cfg, cfg.Validate() == nil, nil
}

// Target reconstructs the value that would be typed into the prompt for cfg,
// so a declared or remembered source can be offered back verbatim.
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
	case source.CollectorVictoriaLogs:
		return cfg.Target
	default:
		return cfg.Args
	}
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
