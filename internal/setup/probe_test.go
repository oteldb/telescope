package setup

import (
	"context"
	"strconv"
	"testing"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// machine stands in for the host being set up. Nothing here shells out: a test
// that ran docker would answer with whatever the developer happens to have
// running, and a test that read the ssh config would put their hosts in the
// expected output.
type machine struct {
	containers []complete.Candidate
	units      []complete.Candidate
	kubeConfig []complete.Candidate
	contexts   map[string][]complete.Candidate
	pods       map[string][]complete.Candidate
	hosts      []complete.Candidate
	// missing names the collectors this machine has no tool for, which is what a
	// listing command reports by failing.
	missing map[source.Collector]bool
}

func (m machine) options() Options {
	return Options{
		Probe: true,
		Yes:   true,
		Fetch: func(_ context.Context, r complete.Request) ([]complete.Candidate, error) {
			if m.missing[r.Collector] {
				return nil, errors.New(string(r.Collector) + " is not installed")
			}
			switch r.Field {
			case complete.FieldKubeConfig:
				return m.kubeConfig, nil
			case complete.FieldKubeContext:
				return m.contexts[r.KubeConfig], nil
			case complete.FieldHost:
				return m.hosts, nil
			}
			switch r.Collector {
			case source.CollectorDocker:
				return m.containers, nil
			case source.CollectorJournal:
				return m.units, nil
			case source.CollectorKubectl:
				return m.pods[r.KubeContext], nil
			}
			return nil, nil
		},
		Hosts: func() []complete.Candidate { return m.hosts },
	}
}

func names(offers []Offer) []string {
	out := make([]string, 0, len(offers))
	for _, o := range offers {
		out = append(out, o.Place.Name)
	}
	return out
}

// TestProbeOffersWhatIsRunning: a container that exited and a unit that is dead
// have logs, but they are not what somebody installing a log viewer is looking
// at, and offering them is what turns a short list into a long one.
func TestProbeOffersWhatIsRunning(t *testing.T) {
	m := machine{
		containers: []complete.Candidate{
			{Value: "api", State: "running", Detail: "example/api"},
			{Value: "migrate", State: "exited", Detail: "example/api"},
		},
		units: []complete.Candidate{
			{Value: "web", State: "running"},
			{Value: "user/sync", State: "running", Detail: "user"},
			{Value: "cleanup", State: "dead"},
		},
	}
	offers := m.options().probe(t.Context())
	require.Equal(t, []string{"api", "sync", "web"}, names(offers))

	require.Equal(t, "container running example/api", offers[0].Note)
	require.Equal(t, "user/sync", offers[1].Place.Unit,
		"the prefix is what sends journalctl to the user manager")
}

// TestProbeOffersAPlacePerContext: a context is the cluster, so a kubeconfig
// naming three of them is three places rather than one.
func TestProbeOffersAPlacePerContext(t *testing.T) {
	m := machine{
		kubeConfig: []complete.Candidate{{Value: "/tmp/kube.yaml"}},
		contexts: map[string][]complete.Candidate{
			"/tmp/kube.yaml": {{Value: "dev"}, {Value: "prod"}},
		},
		pods: map[string][]complete.Candidate{
			"prod": {{Value: "payments/api-0"}, {Value: "payments/api-1"}, {Value: "web/nginx-0"}},
		},
	}
	offers := m.options().probe(t.Context())
	require.Equal(t, []string{"dev", "prod"}, names(offers))
	require.Equal(t, "/tmp/kube.yaml", offers[1].Place.KubeConfig)
	require.Equal(t, "prod", offers[1].Place.Context)
	require.Equal(t, []string{"payments", "web"}, offers[1].Namespaces,
		"read off the same listing the prompt completes against")
	require.Empty(t, offers[0].Namespaces, "a cluster that answered nothing pins nothing")
}

// TestProbePassesOverAToolThatIsNotThere: the listings are the prompt's own, so
// a machine without docker answers here exactly as it does there.
func TestProbePassesOverAToolThatIsNotThere(t *testing.T) {
	m := machine{
		containers: []complete.Candidate{{Value: "api", State: "running"}},
		units:      []complete.Candidate{{Value: "web", State: "running"}},
		missing:    map[source.Collector]bool{source.CollectorDocker: true},
	}
	require.Equal(t, []string{"web"}, names(m.options().probe(t.Context())))
}

// TestProbeOffersAnSSHHostAsAJournal: a host named in the ssh config and
// nothing else is a journal to read over ssh, and it names no unit — picking it
// opens the prompt with the host already dialed.
func TestProbeOffersAnSSHHostAsAJournal(t *testing.T) {
	m := machine{hosts: []complete.Candidate{{Value: "node-1"}}}
	offers := m.options().probe(t.Context())
	require.Len(t, offers, 1)
	require.Equal(t, "journalctl", offers[0].Place.Type)
	require.Equal(t, "ssh://node-1", offers[0].Place.Via)
	require.Empty(t, offers[0].Place.Unit)
}

// TestProbeStopsAtTwentyOfAKind: a desktop runs eighty services, and a hundred
// questions is not a setup flow.
func TestProbeStopsAtTwentyOfAKind(t *testing.T) {
	var m machine
	for i := range 50 {
		m.units = append(m.units, complete.Candidate{
			Value: "unit-" + strconv.Itoa(i), State: "running",
		})
	}
	require.Len(t, m.options().probe(t.Context()), maxPerKind)
}

// TestNamesAreMadeTheirOwn: the config rejects a name declared twice, and a
// container and a unit may well be called the same thing.
func TestNamesAreMadeTheirOwn(t *testing.T) {
	offers := []Offer{
		{Place: config.Place{Name: "api"}},
		{Place: config.Place{Name: "api"}},
		{Place: config.Place{Name: "web"}},
	}
	name(offers)
	require.Equal(t, []string{"api", "api 2", "web"}, names(offers))
}
