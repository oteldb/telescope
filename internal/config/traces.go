package config

import (
	"slices"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"

	"github.com/oteldb/telescope/internal/source"
)

// TraceStore is where a place's traces are read from.
//
// The API is declared rather than discovered, for the reason a place declares
// whether it is Loki or VictoriaLogs: the paths differ, the query language
// differs, and what comes back differs — Tempo answers a search with a summary
// of each trace, Jaeger with the traces themselves. A viewer that probed for it
// would spend a round trip on every search learning what the config already
// knew, and would have to guess again the first time a proxy answered 404 for a
// reason of its own.
type TraceStore struct {
	URL string `yaml:"url,omitempty"`
	// Type is what answers there: "tempo" or "jaeger". Unset is Tempo, which is
	// what `traces:` meant before it could say.
	Type string `yaml:"type,omitempty"`
}

// traceTypeNames are the APIs a trace store may declare.
var traceTypeNames = []string{
	string(source.CollectorTempo),
	string(source.CollectorJaeger),
}

// UnmarshalYAML accepts the url on its own as well as the mapping, since a
// Tempo — which is what the key used to mean and still defaults to — has
// nothing else to say:
//
//	traces: https://tempo.example.com
//	traces:
//	  url: https://victoria.example.com/select/jaeger
//	  type: jaeger
func (t *TraceStore) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&t.URL)
	}
	type plain TraceStore
	return node.Decode((*plain)(t))
}

// MarshalYAML writes the short form back when there is nothing but a url, so a
// config telescope rewrites keeps the shape it was written in.
func (t TraceStore) MarshalYAML() (any, error) {
	if t.Type == "" {
		return t.URL, nil
	}
	type plain TraceStore
	return plain(t), nil
}

// IsZero reports whether the place reads no traces.
func (t TraceStore) IsZero() bool { return strings.TrimSpace(t.URL) == "" }

// Collector is the API spoken at the store.
func (t TraceStore) Collector() source.Collector {
	if name := strings.TrimSpace(t.Type); name != "" {
		return source.Collector(name)
	}
	return source.CollectorTempo
}

// Validate reports whether the store names an API telescope can read.
func (t TraceStore) Validate() error {
	if t.IsZero() {
		return nil
	}
	if name := strings.TrimSpace(t.Type); name != "" && !slices.Contains(traceTypeNames, name) {
		return errors.Errorf("unknown traces type %q: want one of %s",
			name, strings.Join(traceTypeNames, ", "))
	}
	return nil
}
