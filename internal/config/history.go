package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"

	"github.com/oteldb/telescope/internal/source"
)

// historyLimit is how many entries are kept per list.
const historyLimit = 20

// History is what telescope remembers between runs. It is written by the
// program, not by hand, which is why it lives apart from the config file.
type History struct {
	Hosts       []string `yaml:"hosts,omitempty"`
	KubeConfigs []string `yaml:"kubeconfigs,omitempty"`
	// Targets holds the last units, pods and containers, keyed by collector.
	Targets map[string][]string `yaml:"targets,omitempty"`
}

// HistoryPath is where history is stored, honoring XDG_STATE_HOME.
func HistoryPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, appDir, "history.yaml")
}

// LoadHistory reads remembered values. Anything unreadable is treated as empty:
// history is a convenience, never a reason to fail to start.
func LoadHistory() History {
	h, _ := loadHistoryFrom(HistoryPath())
	return h
}

func loadHistoryFrom(path string) (History, error) {
	if path == "" {
		return History{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return History{}, err
	}
	var h History
	if err := yaml.Unmarshal(data, &h); err != nil {
		return History{}, errors.Wrap(err, "parse history")
	}
	return h, nil
}

// Remember records what a stream used, most recent first.
func (h *History) Remember(cfg source.Config) {
	if cfg.Transport == source.TransportSSH {
		h.Hosts = push(h.Hosts, cfg.Host)
	}
	h.KubeConfigs = push(h.KubeConfigs, cfg.KubeConfig)

	if target := Target(cfg); target != "" {
		if h.Targets == nil {
			h.Targets = map[string][]string{}
		}
		key := string(cfg.Collector)
		h.Targets[key] = push(h.Targets[key], target)
	}
}

// Recent returns the remembered targets for a collector, most recent first.
func (h History) Recent(collector source.Collector) []string {
	return h.Targets[string(collector)]
}

// Save writes history, creating the directory if needed.
func (h History) Save() error {
	path := HistoryPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.Wrap(err, "create state dir")
	}
	data, err := yaml.Marshal(h)
	if err != nil {
		return errors.Wrap(err, "encode history")
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return errors.Wrap(err, "write history")
	}
	return nil
}

// push moves v to the front, dropping any earlier occurrence and trimming the
// tail to [historyLimit].
func push(list []string, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return list
	}
	out := append([]string{v}, slices.DeleteFunc(slices.Clone(list), func(s string) bool {
		return s == v
	})...)
	if len(out) > historyLimit {
		out = out[:historyLimit]
	}
	return out
}
