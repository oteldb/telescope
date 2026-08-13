package source

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAbsentIsWhatTheToolSaidAndNothingElse: the difference between a place
// that has nothing to give and one that could not be read is the wording, and
// the wording is the tool's.
func TestAbsentIsWhatTheToolSaidAndNothingElse(t *testing.T) {
	for _, tt := range []struct {
		name string
		said string
		want bool
	}{
		{
			"kubectl has no such deployment",
			`error: error from server (NotFound): deployments.apps "api" not found in namespace "octo"`,
			true,
		},
		{"kubectl has no such pod", `Error from server (NotFound): pods "api-0" not found`, true},
		{"kubectl matches no selector", "error: no matching resources found", true},
		{"docker has no such container", "Error response from daemon: No such container: api", true},
		{"docker has no such object", "Error: No such object: api", true},

		{"the collector is missing", "sh: 1: kubectl: not found", false},
		{"the host is not answering", "ssh: connect to host 10.0.0.1 port 22: Connection refused", false},
		{"the cluster is not answering", "Unable to connect to the server: dial tcp: i/o timeout", false},
		{"nobody said anything", "", false},
		{"a log line about a lookup", `{"msg":"user not found","level":"warn"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, absent([]Line{{Data: []byte(tt.said), Stderr: true}}))
		})
	}

	require.False(t, absent(nil))
	require.True(t, absent([]Line{
		{Data: []byte(`Error from server (NotFound): pods "api-0" not found`), Stderr: true},
		{Data: []byte("Connection to 10.0.0.1 closed."), Stderr: true},
	}), "the transport writing after it is still the same refusal")
}
