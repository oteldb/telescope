package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

func openConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.New([]config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
		{Name: "node", Type: "journalctl", Unit: "nginx"},
		{Name: "checkout", Type: "kubectl"},
		{Name: "prod traces", Type: "tempo", URL: "https://tempo.example.com"},
	}, []config.Group{{Name: "everything", Places: []string{"prod", "node"}}})
	require.NoError(t, err)
	return cfg
}

// TestAPlaceOrAGroupOpensTheSameWay: whoever wrote the link should not have had
// to know which of the two it named.
func TestAPlaceOrAGroupOpensTheSameWay(t *testing.T) {
	cfg := openConfig(t)

	src, ready, err := placeStream(cfg, "prod")
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, source.CollectorVictoriaLogs, src.Collector)

	src, ready, err = placeStream(cfg, "everything")
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, source.CollectorMerge, src.Collector)
}

// TestOpeningAStoreSaysWhichCommandReadsIt: a store holds no lines, and the
// command that draws its traces is the one the answer should name.
func TestOpeningAStoreSaysWhichCommandReadsIt(t *testing.T) {
	_, _, err := placeStream(openConfig(t), "prod traces")
	require.ErrorContains(t, err, "reads traces rather than lines")
	require.ErrorContains(t, err, `telescope trace --from "prod traces"`)
}

// TestOpeningAPlaceThatIsNotThereSaysWhatToDo: a name typed here is as often
// pasted as typed, so the way back is worth naming.
func TestOpeningAPlaceThatIsNotThereSaysWhatToDo(t *testing.T) {
	_, _, err := placeStream(openConfig(t), "prd")
	require.ErrorContains(t, err, `no place named "prd"`)
	require.ErrorContains(t, err, "with no argument")
}

// TestAPlaceMissingItsTargetIsUnfinishedRatherThanWrong: kubectl needs a pod,
// and the start screen is the thing that asks for one.
func TestAPlaceMissingItsTargetIsUnfinishedRatherThanWrong(t *testing.T) {
	_, ready, err := placeStream(openConfig(t), "checkout")
	require.NoError(t, err, "the place is declared and fine")
	require.False(t, ready, "it just has not been told what to read")
}

// TestATargetOnTheCommandLineFinishesThePlace: it is what a link written by
// telescope mcp carries, so what the link says has to be what the flag takes.
func TestATargetOnTheCommandLineFinishesThePlace(t *testing.T) {
	src, ready, err := placeStream(openConfig(t), "checkout")
	require.NoError(t, err)
	require.False(t, ready)

	src = src.WithTarget("app=octo-api")
	require.NoError(t, src.Validate(), "and now it opens")
	require.Contains(t, src.Command(), "app=octo-api")
}
