package source

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func kubeWatchConfig(target string) Config {
	return Config{Collector: CollectorKubectl, Namespace: "octo", Target: target, Follow: true}
}

// TestWhatIsWorthWatching: a restart happens while somebody is looking, and the
// pods behind a target have to be nameable to "kubectl get pods".
func TestWhatIsWorthWatching(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"a selector", kubeWatchConfig("app=octo-api"), "get pods -n octo -l app=octo-api -w"},
		{"a pod", kubeWatchConfig("api-7d9f"), "get pods -n octo api-7d9f -w"},
		{"a pod named as a kind", kubeWatchConfig("pod/api-7d9f"), "get pods -n octo api-7d9f -w"},
		{"a workload", kubeWatchConfig("deploy/api"), ""},
		{"nothing named", kubeWatchConfig(""), ""},
		{
			"a window that is over",
			Config{Collector: CollectorKubectl, Target: "app=api"},
			"",
		},
		{
			"another collector",
			Config{Collector: CollectorDocker, Container: "app", Follow: true},
			"",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, ok := tt.cfg.watchCommand()
			if tt.want == "" {
				require.False(t, ok, cmd)
				return
			}
			require.True(t, ok)
			require.Contains(t, cmd, tt.want)
			require.Contains(t, cmd, "--output-watch-events=true")
		})
	}
}

// TestAWatchIsReachedTheWayTheLogsAre: the pods of a cluster read over ssh are
// on the far side of the same hop.
func TestAWatchIsReachedTheWayTheLogsAre(t *testing.T) {
	cfg := kubeWatchConfig("app=octo-api")
	cfg.Transport, cfg.Host = TransportSSH, "admin@10.0.0.1"

	cmd, ok := cfg.watchCommand()
	require.True(t, ok)
	argv := cfg.argvFor(cmd)
	require.Equal(t, "ssh", argv[0])
	require.Equal(t, "admin@10.0.0.1", argv[len(argv)-2])
	require.Contains(t, argv[len(argv)-1], "get pods")
}

const framePod = `{
    "metadata": {
        "name": "api-7d9f"
    },
    "status": {
        "containerStatuses": [
            {
                "name": "app",
                "restartCount": %d
            }
        ]
    }
}`

func frames(t *testing.T, in string) [][]byte {
	t.Helper()
	return slices.Collect(func(yield func([]byte) bool) {
		podFrames(strings.NewReader(in))(yield)
	})
}

// TestAFrameIsFoundInWhateverElseIsOnTheStream: over ssh a pty folds the remote
// stderr in with the JSON, and a decoder that met one warning would never read
// another object.
func TestAFrameIsFoundInWhateverElseIsOnTheStream(t *testing.T) {
	var b strings.Builder
	b.WriteString("W0814 09:00:00.000 warning: v1beta1 is deprecated\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(framePod, "%d", "1"), "\n", "\r\n"))
	b.WriteString("\r\n")
	b.WriteString("error: unexpected extra output\n")
	b.WriteString(strings.ReplaceAll(framePod, "%d", "2"))
	b.WriteString("\n")

	got := frames(t, b.String())
	require.Len(t, got, 2, "both objects, and neither warning")
	require.Contains(t, string(got[0]), `"restartCount": 1`)
	require.Contains(t, string(got[1]), `"restartCount": 2`)
	require.NotContains(t, string(got[0]), "\r", "and the pty's line endings are gone")
}

// TestAFrameThatNeverEndsIsNotAFrame: a stream cut off mid-object has nothing
// to report about it.
func TestAFrameThatNeverEndsIsNotAFrame(t *testing.T) {
	require.Empty(t, frames(t, "{\n    \"metadata\": {\n"))
	require.Empty(t, frames(t, ""))
	require.Empty(t, frames(t, "not json at all\n"))
}

// TestAWatchEventIsUnwrapped: --output-watch-events puts the pod in an envelope,
// and what a restart is does not depend on how kubectl was asked.
func TestAWatchEventIsUnwrapped(t *testing.T) {
	kind, obj := watchEvent([]byte(`{"type":"MODIFIED","object":{"metadata":{"name":"api"}}}`))
	require.Equal(t, "MODIFIED", kind)
	require.JSONEq(t, `{"metadata":{"name":"api"}}`, string(obj))

	// A bare pod is its own object.
	bare := `{"metadata":{"name":"api"}}`
	kind, obj = watchEvent([]byte(bare))
	require.Empty(t, kind)
	require.JSONEq(t, bare, string(obj))
}

// watchFrame is one event as "kubectl get pods -w --output-watch-events -o json"
// prints it: the envelope pretty-printed, its own braces at column zero.
func watchFrame(kind string, count int) string {
	return fmt.Sprintf(`{
    "type": %q,
    "object": {
        "metadata": {
            "name": "api-7d9f"
        },
        "status": {
            "containerStatuses": [
                {
                    "name": "app",
                    "restartCount": %d,
                    "lastState": {
                        "terminated": {
                            "reason": "OOMKilled",
                            "exitCode": 137,
                            "finishedAt": "2026-08-14T09:30:00Z"
                        }
                    }
                }
            ]
        }
    }
}
`, kind, count)
}

// TestAWatchSaysWhenAContainerComesBack: the whole point, end to end from the
// bytes kubectl writes to the note a reader sees.
func TestAWatchSaysWhenAContainerComesBack(t *testing.T) {
	stream := watchFrame("ADDED", 1) + watchFrame("MODIFIED", 2)

	out := make(chan Line, 4)
	watchFrames(t.Context(), strings.NewReader(stream), out)
	close(out)

	var got []Line
	for l := range out {
		got = append(got, l)
	}
	require.Len(t, got, 1, "the first sighting is not a restart, the second is")
	require.Equal(t, KindRestarted, got[0].Kind)
	require.Equal(t, "api-7d9f/app: OOMKilled (exit 137)", got[0].Reason)
}

// TestAPodThatWentAwayIsForgotten: DELETED is why the watch is asked for events
// and not just for pods.
func TestAPodThatWentAwayIsForgotten(t *testing.T) {
	stream := watchFrame("ADDED", 4) + watchFrame("DELETED", 4) +
		watchFrame("ADDED", 0) + watchFrame("MODIFIED", 1)

	out := make(chan Line, 4)
	watchFrames(t.Context(), strings.NewReader(stream), out)
	close(out)

	var got []Line
	for l := range out {
		got = append(got, l)
	}
	require.Len(t, got, 1, "the new pod is counted from its own zero")
}
