package config

import (
	"slices"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/figureout"

	"github.com/oteldb/telescope/internal/source"
)

// TraceStore is where a place's traces are read from: the name of a place that
// reads them, or a store written out where it is used.
//
// Naming one is the way round that scales. A trace store is a system's, not a
// stream's — every service in an environment writes into the same one, and the
// environment is several places here — so writing it out on each of them is the
// same url, token and proxy copied until one of the copies is wrong. A place
// that reads traces is a place like any other: it has a name, it declares what
// it needs to be let in, and the places whose lines carry ids into it say so by
// naming it.
//
// The written-out form stays for the one-place case, where a name would be
// ceremony: it borrows the token, tenant and proxy of the place it is written
// on, since a system's traces are usually behind the same door as its logs.
//
// The API is declared rather than discovered, for the reason a place declares
// whether it is Loki or VictoriaLogs: the paths differ, the query language
// differs, and what comes back differs — Tempo answers a search with a summary
// of each trace, Jaeger with the traces themselves. A viewer that probed for it
// would spend a round trip on every search learning what the config already
// knew, and would have to guess again the first time a proxy answered 404 for a
// reason of its own.
type TraceStore struct {
	// Name is a place that reads traces. Exclusive with the rest: a link says
	// where to look, and the place it names says everything else.
	Name string
	URL  string
	// Type is what answers there: "tempo" or "jaeger". Unset is Tempo, which is
	// what `traces:` meant before it could say.
	Type string
}

// traceTypeNames are the APIs a trace store may declare.
var traceTypeNames = []string{
	string(source.CollectorTempo),
	string(source.CollectorJaeger),
}

// traceStoreDescriptor describes the mapping form. The url on its own is the
// other one, and [figureout.ScalarOr] at the registration widens it, since a
// Tempo — which is what the key used to mean and still defaults to — has
// nothing else to say:
//
//	traces: https://tempo.example.com
//	traces:
//	  url: https://victoria.example.com/select/jaeger
//	  type: jaeger
var traceStoreDescriptor = figureout.MustDerive(
	func(t *TraceStore, s *figureout.Schema[TraceStore]) {
		figureout.Value(s, &t.Name, "place").
			Doc("The name of a place that reads traces, where the store is declared once " +
				"and named by everything whose lines carry ids into it.")
		// Not Explicit: a nested object is materialized whether or not the file
		// declares it, so requiring the url here would make every place that
		// reads no traces a broken one. A store with no url reads no traces,
		// which is what [TraceStore.IsZero] answers.
		figureout.Value(s, &t.URL, "url").
			Doc("The base the trace API's paths hang off.")
		figureout.Value(s, &t.Type, "type").
			Enum(traceTypeNames...).ApplyDefault(string(source.CollectorTempo)).
			Doc("Which API answers there.")
	},
)

// IsZero reports whether the place reads no traces.
func (t TraceStore) IsZero() bool {
	return strings.TrimSpace(t.URL) == "" && strings.TrimSpace(t.Name) == ""
}

// Links reports whether the store is a place named rather than written out.
func (t TraceStore) Links() bool { return strings.TrimSpace(t.Name) != "" }

// Collector is the API spoken at the store.
func (t TraceStore) Collector() source.Collector {
	if name := strings.TrimSpace(t.Type); name != "" {
		return source.Collector(name)
	}
	return source.CollectorTempo
}

// Validate reports whether the store names an API telescope can read.
//
// Whether the place it links to exists is not asked here: that is a rule about
// two entries of the file rather than about this one, and it is registered as
// an invariant where the resolver can say which line named what.
func (t TraceStore) Validate() error {
	if t.IsZero() {
		return nil
	}
	if t.Links() {
		if strings.TrimSpace(t.URL) != "" || strings.TrimSpace(t.Type) != "" {
			return errors.Errorf(
				"traces names the place %q and describes a store of its own: "+
					"the place it names says the rest", t.Name)
		}
		return nil
	}
	if name := strings.TrimSpace(t.Type); name != "" && !slices.Contains(traceTypeNames, name) {
		return errors.Errorf("unknown traces type %q: want one of %s",
			name, strings.Join(traceTypeNames, ", "))
	}
	return nil
}
