// Package setup writes a first config file: what this machine already runs and
// what a Grafana already knows about, offered one at a time as places.
package setup

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/config"
)

// Offer is one place found, and the sentence saying where it was found. The
// sentence is asked at the prompt and then written above the place in the file,
// since a config nobody can trace back to a machine is one nobody edits.
type Offer struct {
	Place config.Place
	Note  string
	// Namespaces are the ones a cluster turned out to have, so accepting it can
	// pin one. A place that pins nothing is still valid: it opens the prompt
	// with the cluster filled in.
	Namespaces []string
}

// Options is what init was asked for.
type Options struct {
	// Probe looks at this machine: its containers, its units, its clusters and
	// the hosts its ssh config names.
	Probe bool
	// Grafana imports datasources, over the API or off disk.
	Grafana Grafana
	// Yes takes every offer without asking, which is the path a script takes and
	// the one a test can drive.
	Yes bool

	In  io.Reader
	Out io.Writer

	// Fetch lists what a collector can read, and Hosts what the ssh config
	// names. They are fields rather than direct calls so a test never shells out
	// to a real docker or reads the developer's own ssh config.
	Fetch  func(context.Context, complete.Request) ([]complete.Candidate, error)
	Hosts  func() []complete.Candidate
	Client *http.Client
}

func (o *Options) setDefaults() {
	if o.Fetch == nil {
		o.Fetch = complete.Fetch
	}
	if o.Hosts == nil {
		o.Hosts = complete.Hosts
	}
	if o.Client == nil {
		o.Client = http.DefaultClient
	}
	if o.In == nil {
		o.In = strings.NewReader("")
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
}

// ErrNothingFound is what init has to say when neither the machine nor a
// Grafana offered anything: there is no file worth writing, and an empty one
// would only look like a failure that went unreported.
var ErrNothingFound = errors.New("found nothing to offer")

// Run gathers the offers, asks about each one and returns the file to write.
// Writing it is the caller's, so that what would be written can be printed
// instead.
func Run(ctx context.Context, o Options) ([]byte, error) {
	o.setDefaults()

	offers, notes := o.gather(ctx)
	for _, note := range notes {
		fprintf(o.Out, "%s\n", note)
	}
	if len(offers) == 0 {
		return nil, ErrNothingFound
	}
	name(offers)

	a := asker{in: o.In, out: o.Out, yes: o.Yes}
	var kept []Offer
	for _, offer := range offers {
		ok, err := a.confirm(offer.Note+": add "+offer.Place.Name+"?", true)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if err := a.pinNamespace(&offer); err != nil {
			return nil, err
		}
		kept = append(kept, offer)
	}
	if len(kept) == 0 {
		return nil, errors.New("nothing was accepted, so there is nothing to write")
	}
	return Render(kept)
}

// gather collects every offer, in the order the questions are asked: what is
// running here first, since that is what somebody installing a log viewer is
// most likely looking at.
func (o Options) gather(ctx context.Context) ([]Offer, []string) {
	var (
		offers []Offer
		notes  []string
	)
	if o.Probe {
		offers = append(offers, o.probe(ctx)...)
	}
	found, said, err := o.Grafana.offers(ctx, o.Client)
	if err != nil {
		notes = append(notes, "grafana: "+err.Error())
	}
	return append(offers, found...), append(notes, said...)
}

// name makes every place's name its own. A container and a unit may be called
// the same thing, and two kubeconfigs may hold the same context; the config
// rejects a name declared twice, so the second one is numbered rather than
// dropped.
func name(offers []Offer) {
	seen := map[string]int{}
	for i := range offers {
		n := strings.TrimSpace(offers[i].Place.Name)
		if n == "" {
			n = "place"
		}
		seen[n]++
		if count := seen[n]; count > 1 {
			n += " " + strconv.Itoa(count)
		}
		offers[i].Place.Name = n
	}
}

// namespaces reads the namespaces a listing of pods and workloads reveals. It
// is the same listing the prompt completes against, so a cluster is asked once
// for both.
func namespaces(items []complete.Candidate) []string {
	var out []string
	for _, c := range items {
		ns, _, ok := strings.Cut(c.Value, "/")
		if !ok || ns == "" || slices.Contains(out, ns) {
			continue
		}
		out = append(out, ns)
	}
	slices.Sort(out)
	return out
}
