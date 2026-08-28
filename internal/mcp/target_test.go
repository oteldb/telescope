package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

func targetConfig(t *testing.T) config.Config {
	t.Helper()
	return testConfig(t, []config.Place{
		{Name: "atompve-operations", Type: "kubectl", Namespace: "octo"},
		{Name: "humo observer", Type: "victorialogs", URL: "https://logs.example.com"},
		{Name: "node", Type: "journalctl"},
	}, nil)
}

// TestAPlaceIsToldWhatToReadThere: a kubectl place is half a place until it is
// told which workload — the config declares the parts that stay still, and the
// pod is what changes between one question and the next.
func TestAPlaceIsToldWhatToReadThere(t *testing.T) {
	src, err := streamOf(targetConfig(t), "atompve-operations", "app=octo-api")
	require.NoError(t, err)
	require.NoError(t, src.Validate())
	require.Equal(t, "octo", src.Namespace, "what the config declared is kept")
	require.Contains(t, src.Command(), "app=octo-api")

	src, err = streamOf(targetConfig(t), "node", "nginx.service")
	require.NoError(t, err)
	require.Equal(t, "nginx.service", src.Unit)
}

// TestAPlaceWithNothingToReadSaysSoAsATool: what a collector says when it is
// run without a target is about its own command line, and the caller has a
// place name and a tool schema rather than a kubectl invocation.
func TestAPlaceWithNothingToReadSaysSoAsATool(t *testing.T) {
	_, err := streamOf(targetConfig(t), "atompve-operations", "")
	require.ErrorContains(t, err, `"atompve-operations" needs a target`)
	require.ErrorContains(t, err, "as the target argument")
}

// TestAPlaceAndItsTargetRunTogetherIsRecognised: the start screen takes the two
// that way, so a name arriving like it is a habit carried over rather than a
// name that is simply wrong.
func TestAPlaceAndItsTargetRunTogetherIsRecognised(t *testing.T) {
	_, err := streamOf(targetConfig(t), "atompve-operations octo/app=octo-api", "")
	require.ErrorContains(t, err, `"atompve-operations" is one`)
	require.ErrorContains(t, err, `"octo/app=octo-api" is probably what to read there`)
	require.ErrorContains(t, err, "as the target argument")
}

// TestANameWithASpaceIsStillItsOwnName: a place is named by a person, so the
// split cannot simply be on the space.
func TestANameWithASpaceIsStillItsOwnName(t *testing.T) {
	src, err := streamOf(targetConfig(t), "humo observer", "")
	require.NoError(t, err)
	require.Equal(t, source.CollectorVictoriaLogs, src.Collector)
}

// TestADatabaseNeedsNoTarget: it is asked a query, not pointed at a process.
func TestADatabaseNeedsNoTarget(t *testing.T) {
	src, err := streamOf(targetConfig(t), "humo observer", "")
	require.NoError(t, err)
	require.NoError(t, src.Validate())
}

// TestAGroupIsToldOnceForAllOfIt: a group of four clusters running the same
// workload is one question, and the target is the part they share.
func TestAGroupIsToldOnceForAllOfIt(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "east", Type: "kubectl", Namespace: "octo", Context: "east"},
		{Name: "west", Type: "kubectl", Namespace: "octo", Context: "west"},
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	}, []config.Group{
		{Name: "clusters", Places: []string{"east", "west"}},
		{Name: "everything", Places: []string{"east", "prod"}},
	})

	src, err := streamOf(cfg, "clusters", "app=octo-api")
	require.NoError(t, err)
	require.NoError(t, src.Validate())
	require.Len(t, src.Merge, 2)
	for _, sub := range src.Merge {
		require.Equal(t, "octo", sub.Namespace, "each keeps the namespace it declared")
		require.Contains(t, sub.Command(), "app=octo-api")
	}

	// The database in a mixed group was never the one asking.
	src, err = streamOf(cfg, "everything", "app=octo-api")
	require.NoError(t, err)
	require.NoError(t, src.Validate())
	for _, sub := range src.Merge {
		if sub.Collector == source.CollectorVictoriaLogs {
			require.Empty(t, sub.Target, "a database is asked a query, not pointed at a pod")
		}
	}
}

// TestANamespaceInTheTargetStillWins: the argument is more specific than the
// file, and the file is where the default belongs.
func TestANamespaceInTheTargetStillWins(t *testing.T) {
	src, err := streamOf(targetConfig(t), "atompve-operations", "kube-system/app=coredns")
	require.NoError(t, err)
	require.Equal(t, "kube-system", src.Namespace)
}
