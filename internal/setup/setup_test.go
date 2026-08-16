package setup

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/config"
)

func host() machine {
	return machine{
		containers: []complete.Candidate{
			{Value: "api", State: "running", Detail: "example/api"},
			{Value: "cache", State: "running", Detail: "example/cache"},
		},
		units: []complete.Candidate{{Value: "web", State: "running"}},
	}
}

// TestYesTakesEveryOffer: the path a provisioning script takes, and the one
// that needs no terminal at all.
func TestYesTakesEveryOffer(t *testing.T) {
	data, err := Run(t.Context(), host().options())
	require.NoError(t, err)

	cfg, err := config.Parse(data)
	require.NoError(t, err)
	require.Len(t, cfg.Places, 3)
}

// TestEachOfferIsAskedAbout: a machine is offered one thing at a time, and the
// answers decide what the file holds.
func TestEachOfferIsAskedAbout(t *testing.T) {
	opts := host().options()
	opts.Yes = false
	opts.In = strings.NewReader("y\nn\n\n")
	var out bytes.Buffer
	opts.Out = &out

	data, err := Run(t.Context(), opts)
	require.NoError(t, err)
	require.Contains(t, out.String(), "add api?")

	cfg, err := config.Parse(data)
	require.NoError(t, err)
	require.Len(t, cfg.Places, 2)
	require.Equal(t, "api", cfg.Places[0].Name)
	require.Equal(t, "web", cfg.Places[1].Name, "an empty answer takes the offer")
}

// TestACalledOffPromptSaysWhichFlagWouldHaveAnswered: init reads its answers
// from a pipe, and a pipe that has run out is somebody who meant to answer.
func TestACalledOffPromptSaysWhichFlagWouldHaveAnswered(t *testing.T) {
	opts := host().options()
	opts.Yes = false
	opts.In = strings.NewReader("")

	_, err := Run(t.Context(), opts)
	require.ErrorContains(t, err, "--yes")
}

// TestAnAcceptedClusterCanPinANamespace: the namespaces come off the listing
// the cluster was probed with, and pinning none is a place that opens the
// prompt with the cluster filled in.
func TestAnAcceptedClusterCanPinANamespace(t *testing.T) {
	m := machine{
		kubeConfig: []complete.Candidate{{Value: "/tmp/kube.yaml"}},
		contexts:   map[string][]complete.Candidate{"/tmp/kube.yaml": {{Value: "prod"}}},
		pods: map[string][]complete.Candidate{
			"prod": {{Value: "payments/api-0"}, {Value: "web/nginx-0"}},
		},
	}
	opts := m.options()
	opts.Yes = false
	opts.In = strings.NewReader("y\npayments\n")

	data, err := Run(t.Context(), opts)
	require.NoError(t, err)

	cfg, err := config.Parse(data)
	require.NoError(t, err)
	require.Equal(t, "payments", cfg.Places[0].Namespace)
}

// TestNothingFoundIsSaidRatherThanWritten: an empty config file looks like a
// failure that went unreported.
func TestNothingFoundIsSaidRatherThanWritten(t *testing.T) {
	_, err := Run(t.Context(), machine{}.options())
	require.ErrorIs(t, err, ErrNothingFound)
}

// TestRefusingEveryOfferWritesNothing: a config with no places in it is not
// what somebody who said no to each one asked for.
func TestRefusingEveryOfferWritesNothing(t *testing.T) {
	opts := host().options()
	opts.Yes = false
	opts.In = strings.NewReader("n\nn\nn\n")

	_, err := Run(t.Context(), opts)
	require.ErrorContains(t, err, "nothing was accepted")
}
