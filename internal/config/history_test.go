package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func TestHistoryRemember(t *testing.T) {
	pods := source.Config{
		Transport: source.TransportSSH, Host: "node1",
		Collector: source.CollectorKubectl, Namespace: "oteldb", Target: "oteldb-0",
		KubeConfig: "/etc/rancher/k3s/k3s.yaml",
	}
	var h History
	h.Remember(pods)

	require.Equal(t, []string{"node1"}, h.Hosts)
	require.Equal(t, []string{"/etc/rancher/k3s/k3s.yaml"}, h.KubeConfigs)
	// The target is stored as typed, so it can be offered back verbatim.
	require.Equal(t, []string{"oteldb/oteldb-0"}, h.Recent(pods))

	// A local stream contributes no host.
	docker := source.Config{Collector: source.CollectorDocker, Container: "app"}
	h.Remember(docker)
	require.Equal(t, []string{"node1"}, h.Hosts)
	require.Equal(t, []string{"app"}, h.Recent(docker))
}

// TestHistoryIsScoped: a pod remembered against one cluster must not be
// offered against another, where it does not exist.
func TestHistoryIsScoped(t *testing.T) {
	ops := source.Config{
		Transport: source.TransportSSH, Host: "node1", Collector: source.CollectorKubectl,
		KubeConfig: "/root/.kube/operations.yml", Namespace: "oteldb", Target: "api-1",
	}
	var h History
	h.Remember(ops)
	require.Equal(t, []string{"oteldb/api-1"}, h.Recent(ops))

	other := ops
	other.KubeConfig = "/root/.kube/staging.yml"
	require.Empty(t, h.Recent(other), "another kubeconfig is another cluster")

	byContext := ops
	byContext.KubeContext = "admin@2"
	require.Empty(t, h.Recent(byContext), "so is another context")

	byHost := ops
	byHost.Host = "node2"
	require.Empty(t, h.Recent(byHost), "and another node")

	// Containers are scoped by host, where the kubeconfig plays no part.
	app := source.Config{Transport: source.TransportSSH, Host: "node1", Collector: source.CollectorDocker, Container: "app"}
	h.Remember(app)
	elsewhere := app
	elsewhere.Host = "node2"
	require.Equal(t, []string{"app"}, h.Recent(app))
	require.Empty(t, h.Recent(elsewhere))
}

func TestHistoryTargetRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  source.Config
		want string
	}{
		{"user unit", source.Config{Collector: source.CollectorJournal, Unit: "syncthing", UserUnit: true}, "user/syncthing"},
		{"system unit", source.Config{Collector: source.CollectorJournal, Unit: "kubelet"}, "kubelet"},
		{"whole journal", source.Config{Collector: source.CollectorJournal}, ""},
		{
			"pod with container",
			source.Config{Collector: source.CollectorKubectl, Namespace: "ns", Target: "pod", Container: "c"},
			"ns/pod:c",
		},
		{"command", source.Config{Collector: source.CollectorCommand, Args: "tail -F x"}, "tail -F x"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var h History
			h.Remember(tt.cfg)
			if tt.want == "" {
				require.Empty(t, h.Recent(tt.cfg))
				return
			}
			require.Equal(t, []string{tt.want}, h.Recent(tt.cfg))
		})
	}
}

// TestHistoryMostRecentFirst: reusing a value moves it back to the front
// rather than duplicating it.
func TestHistoryMostRecentFirst(t *testing.T) {
	var h History
	for _, host := range []string{"a", "b", "c", "b"} {
		h.Remember(source.Config{Transport: source.TransportSSH, Host: host})
	}
	require.Equal(t, []string{"b", "c", "a"}, h.Hosts)
}

func TestHistoryCap(t *testing.T) {
	var h History
	for i := range historyLimit + 10 {
		h.Remember(source.Config{Transport: source.TransportSSH, Host: "h" + strconv.Itoa(i)})
	}
	require.Len(t, h.Hosts, historyLimit)
	require.Equal(t, "h"+strconv.Itoa(historyLimit+9), h.Hosts[0], "newest is kept")
}

func TestHistorySaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	var h History
	h.Remember(source.Config{
		Transport: source.TransportSSH, Host: "node1",
		Collector: source.CollectorDocker, Container: "app",
	})
	require.NoError(t, h.Save())

	path := filepath.Join(dir, appDir, "history.yaml")
	require.FileExists(t, path)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "history may name internal hosts")

	require.Equal(t, h, LoadHistory())
}

func TestLoadHistoryMissingIsEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	require.Equal(t, History{}, LoadHistory())
}
