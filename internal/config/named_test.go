package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// unreadable is a token whose command fails, which is what a keyring lookup is
// away from the session that can unlock it.
func unreadable() Token { return Token{Exec: Argv{"sh", "-c", "echo nope >&2; exit 1"}} }

// TestAPlaceIsNamedOnce: the reason a place could not be opened is carried up
// through the group that names it, and a reader seeing its name twice reads the
// second one as a second place.
func TestAPlaceIsNamedOnce(t *testing.T) {
	cfg, err := New([]Place{
		{Name: "one", Type: "victorialogs", URL: "https://logs.example.com", Token: unreadable()},
		{Name: "two", Type: "victorialogs", URL: "https://logs.example.com"},
	}, []Group{
		{Name: "both", Places: []string{"one", "two"}},
	})
	require.NoError(t, err, "an unreadable token is the environment, not the file")

	_, _, placeErr := cfg.Places[0].Stream()
	require.ErrorContains(t, placeErr, `place "one"`)

	_, _, groupErr := cfg.Groups[0].Stream()
	require.ErrorContains(t, groupErr, `place "one"`)
	require.Equal(t, 1, strings.Count(groupErr.Error(), `place "one"`), groupErr.Error())
}
