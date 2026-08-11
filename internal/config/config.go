// Package config reads the sources declared by the user and remembers the ones
// they reach for.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"

	"github.com/oteldb/telescope/internal/source"
)

// appDir is the per-user directory telescope keeps its files in.
const appDir = "telescope"

// collectorNames are the collectors a source may declare, for the message that
// says so when it declares something else.
var collectorNames = []string{
	string(source.CollectorJournal),
	string(source.CollectorKubectl),
	string(source.CollectorDocker),
	string(source.CollectorCommand),
	string(source.CollectorVictoriaLogs),
	string(source.CollectorLoki),
	string(source.CollectorMerge),
}

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

	// Merge names the other declared sources this one reads as a single
	// stream. Naming any is what makes the source a merge, so its collector
	// does not have to say so.
	Merge []string `yaml:"merge,omitempty"`
	// Merged is what Merge names, filled in by [Load].
	Merged []source.Config `yaml:"-"`

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

	// Range bounds the window read, as typed at the prompt: "1h", "today",
	// "6h..1h". It is resolved when the source is opened, so a relative one
	// means the same thing every run.
	Range string `yaml:"range,omitempty"`

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

	// resolved is every endpoint as read once at load, keyed by name. A token
	// may cost a keyring prompt, so it is read once per run and not once per
	// place that asks for it.
	resolved map[string]resolvedEndpoint
}

// resolvedEndpoint is one endpoint as it came out of [Endpoint.Resolve],
// including the reason its token could not be read.
type resolvedEndpoint struct {
	endpoint source.Endpoint
	err      error
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
	if err := c.resolveMerges(); err != nil {
		return Config{}, err
	}
	for i, s := range c.Sources {
		if err := s.Validate(); err != nil {
			// Named rather than numbered wherever possible: counting entries in
			// a file to find the broken one is work the message can do.
			if s.Name != "" {
				return Config{}, errors.Wrapf(err, "source %q", s.Name)
			}
			return Config{}, errors.Wrapf(err, "source %d", i+1)
		}
	}
	return c, nil
}

// resolveEndpoints attaches each source's endpoint to it. Naming an endpoint
// that was never declared is a mistake in the file; failing to read its token
// is not, and is carried on the source instead.
func (c *Config) resolveEndpoints() error {
	c.resolved = make(map[string]resolvedEndpoint, len(c.Endpoints))
	for i, e := range c.Endpoints {
		if err := e.Validate(); err != nil {
			if e.Name != "" {
				return errors.Wrapf(err, "endpoint %q", e.Name)
			}
			return errors.Wrapf(err, "endpoint %d", i+1)
		}
		if _, ok := c.resolved[e.Name]; ok {
			return errors.Errorf("endpoint %q is declared twice", e.Name)
		}
		resolved, err := e.Resolve()
		c.resolved[e.Name] = resolvedEndpoint{endpoint: resolved, err: err}
	}
	for i, s := range c.Sources {
		if s.Endpoint == "" {
			continue
		}
		got, ok := c.resolved[s.Endpoint]
		if !ok {
			return errors.Errorf("source %q names undeclared endpoint %q", s.Name, s.Endpoint)
		}
		c.Sources[i].Resolved, c.Sources[i].resolveErr = got.endpoint, got.err
	}
	return nil
}

// resolveMerges attaches to each merge the sources it names. They are resolved
// here rather than when the merge is opened so that naming a source that was
// never declared, or one that cannot open on its own, is reported as what it
// is: a mistake in the file.
func (c *Config) resolveMerges() error {
	byName := make(map[string]int, len(c.Sources))
	for i, s := range c.Sources {
		if s.Name == "" {
			continue
		}
		if _, ok := byName[s.Name]; ok {
			return errors.Errorf("source %q is declared twice", s.Name)
		}
		byName[s.Name] = i
	}
	for i, s := range c.Sources {
		for _, name := range s.Merge {
			j, ok := byName[name]
			if !ok {
				return errors.Errorf("source %q merges undeclared source %q", s.Name, name)
			}
			if j == i || len(c.Sources[j].Merge) > 0 {
				return errors.Errorf("source %q merges %q, which is itself a merge", s.Name, name)
			}
			cfg, ready, err := c.Sources[j].Stream()
			switch {
			case err != nil && c.Sources[j].resolveErr != nil:
				// Whether a token can be read is environment, not declaration.
				// The merge carries the reason and reports it when opened.
				c.Sources[i].resolveErr = errors.Wrapf(err, "merged %q", name)
			case err != nil:
				return errors.Wrapf(err, "source %q merges %q", s.Name, name)
			case !ready:
				// A merge cannot stop to ask: every source it names has to be
				// openable as it stands.
				return errors.Errorf("source %q merges %q, which does not say enough to open", s.Name, name)
			}
			cfg.Name = name
			c.Sources[i].Merged = append(c.Sources[i].Merged, cfg)
		}
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
		Name:        s.Name,
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
		Merge:       s.Merged,
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
	if cfg.Range, err = source.ParseRange(s.Range, time.Now()); err != nil {
		return source.Config{}, false, err
	}

	switch cfg.Transport {
	case source.TransportLocal, source.TransportSSH:
	default:
		return source.Config{}, false, errors.Errorf("unknown transport %q", s.Transport)
	}
	// An endpoint says which API it speaks, and merging sources is what a merge
	// is, so a source doing either does not have to repeat it.
	switch {
	case s.Collector != "":
	case len(s.Merge) > 0:
		cfg.Collector = source.CollectorMerge
	case s.Endpoint != "":
		cfg.Collector = s.Resolved.Collector
	}
	switch cfg.Collector {
	case source.CollectorJournal, source.CollectorKubectl, source.CollectorDocker,
		source.CollectorCommand, source.CollectorVictoriaLogs, source.CollectorLoki,
		source.CollectorMerge:
	case "":
		return source.Config{}, false, errors.Errorf(
			"collector is required: one of %s — or name an endpoint, which says which it is",
			strings.Join(collectorNames, ", "))
	default:
		return source.Config{}, false, errors.Errorf(
			"unknown collector %q: want one of %s", s.Collector, strings.Join(collectorNames, ", "))
	}
	if cfg.Collector == source.CollectorMerge && s.resolveErr != nil {
		return cfg, false, s.resolveErr
	}
	if cfg.Collector.IsRemoteAPI() {
		if s.Endpoint == "" {
			return source.Config{}, false, errors.Errorf("collector %q requires an endpoint", s.Collector)
		}
		if s.resolveErr != nil {
			return cfg, false, s.resolveErr
		}
		if k := s.Resolved.Collector; k != "" && k != cfg.Collector {
			return source.Config{}, false, errors.Errorf(
				"endpoint %q speaks %s, not %s", s.Endpoint, k, cfg.Collector)
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
	if cfg.Collector == source.CollectorMerge {
		// Everything else can be filled in at the prompt; a merge cannot, since
		// the prompt reads one thing at a time.
		if err := cfg.Validate(); err != nil {
			return cfg, false, err
		}
		return cfg, true, nil
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
	case source.CollectorVictoriaLogs, source.CollectorLoki:
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
