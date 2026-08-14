package source

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pod writes what kubectl reports for one pod, at the restart count and last
// termination given.
func pod(name, container string, count int, reason string, exit int, at string) string {
	last := `{}`
	if reason != "" {
		last = fmt.Sprintf(`{"terminated":{"reason":%q,"exitCode":%d,"finishedAt":%q}}`, reason, exit, at)
	}
	return fmt.Sprintf(
		`{"metadata":{"name":%q},"status":{"containerStatuses":[{"name":%q,"restartCount":%d,"lastState":%s}]}}`,
		name, container, count, last,
	)
}

// TestTheFirstFrameOfAWatchIsNotARestart: a pod that had restarted twice before
// telescope opened did not restart just now, and saying so would put an old
// crash in the middle of today's timeline.
func TestTheFirstFrameOfAWatchIsNotARestart(t *testing.T) {
	var r restarts
	require.Empty(t, r.observe([]byte(pod("api-7d9f", "app", 2, "OOMKilled", 137, "2026-08-14T09:00:00Z"))))
	require.Empty(t, r.observe([]byte(pod("api-7d9f", "app", 2, "OOMKilled", 137, "2026-08-14T09:00:00Z"))),
		"and the same count again is still not one")
}

// TestARestartIsTheCountGoingUp: Kubernetes has no restart event, only a number
// and whoever was counting.
func TestARestartIsTheCountGoingUp(t *testing.T) {
	var r restarts
	r.observe([]byte(pod("api-7d9f", "app", 2, "", 0, "")))

	got := r.observe([]byte(pod("api-7d9f", "app", 3, "OOMKilled", 137, "2026-08-14T09:30:00Z")))
	require.Len(t, got, 1)
	require.Equal(t, KindRestarted, got[0].Kind)
	require.Equal(t, "api-7d9f/app: OOMKilled (exit 137)", got[0].Reason)
	require.Equal(t, time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC), got[0].At.UTC(),
		"dated when the old container died, which is where in the log it belongs")
	require.Empty(t, r.observe([]byte(pod("api-7d9f", "app", 3, "OOMKilled", 137, "2026-08-14T09:30:00Z"))),
		"and it is said once")
}

// TestAWatchThatMissedFramesSaysHowMany: a container that came back three times
// while nobody was looking is not one restart.
func TestAWatchThatMissedFramesSaysHowMany(t *testing.T) {
	var r restarts
	r.observe([]byte(pod("api-7d9f", "app", 1, "", 0, "")))
	got := r.observe([]byte(pod("api-7d9f", "app", 4, "Error", 1, "2026-08-14T09:30:00Z")))
	require.Len(t, got, 1)
	require.Contains(t, got[0].Reason, "3 restarts")
}

// TestARestartWithNothingKnownAboutItIsStillARestart: the count is the fact,
// and what killed it is what kubectl happened to still be holding.
func TestARestartWithNothingKnownAboutItIsStillARestart(t *testing.T) {
	var r restarts
	r.observe([]byte(pod("api-7d9f", "app", 0, "", 0, "")))
	got := r.observe([]byte(pod("api-7d9f", "app", 1, "", 0, "")))
	require.Len(t, got, 1)
	require.Equal(t, "api-7d9f/app", got[0].Reason)
	require.True(t, got[0].At.IsZero(), "and it lands where it arrived, having no time of its own")
}

// TestANameKubernetesGivesToANewPodCountsFromItsOwnZero: a pod is gone and the
// next one is not carrying its total.
func TestANameKubernetesGivesToANewPodCountsFromItsOwnZero(t *testing.T) {
	var r restarts
	r.observe([]byte(pod("api-7d9f", "app", 4, "", 0, "")))
	r.forget([]byte(pod("api-7d9f", "app", 4, "", 0, "")))
	require.Empty(t, r.observe([]byte(pod("api-7d9f", "app", 0, "", 0, ""))),
		"the first sighting of the new one says nothing")
	require.Len(t, r.observe([]byte(pod("api-7d9f", "app", 1, "", 0, ""))), 1)
}

// TestAFrameThatDoesNotParseIsSkipped: a watch is a long-lived stream of
// somebody else's JSON, and one bad frame is not a reason to stop counting.
func TestAFrameThatDoesNotParseIsSkipped(t *testing.T) {
	var r restarts
	r.observe([]byte(pod("api-7d9f", "app", 1, "", 0, "")))
	for _, bad := range []string{"", "{", "null", "[]", `{"metadata":{}}`, `{"status":{"containerStatuses":[{}]}}`} {
		require.Empty(t, r.observe([]byte(bad)), bad)
	}
	require.Len(t, r.observe([]byte(pod("api-7d9f", "app", 2, "", 0, ""))), 1,
		"and the count is still being kept")
}

func FuzzRestartObserve(f *testing.F) {
	f.Add(pod("api-7d9f", "app", 2, "OOMKilled", 137, "2026-08-14T09:00:00Z"))
	f.Add(pod("api-7d9f", "app", 0, "", 0, ""))
	f.Add(`{"metadata":{"name":"a"},"status":{"containerStatuses":[{"name":"c","restartCount":-1}]}}`)
	f.Add("{")
	f.Add("null")

	f.Fuzz(func(t *testing.T, obj string) {
		var r restarts
		// Twice: the second pass is where a count is compared with one it read
		// out of the same bytes.
		r.observe([]byte(obj))
		for _, l := range r.observe([]byte(obj)) {
			require.Equal(t, KindRestarted, l.Kind)
		}
		r.forget([]byte(obj))
	})
}
