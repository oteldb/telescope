package complete

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func TestRank(t *testing.T) {
	items := []Candidate{
		{Value: "oteldb-0"},
		{Value: "clickhouse-0"},
		{Value: "otel-collector"},
		{Value: "my-oteldb"},
	}
	for _, tt := range []struct {
		name  string
		query string
		want  []string
	}{
		{"empty keeps order", "", []string{"oteldb-0", "clickhouse-0", "otel-collector", "my-oteldb"}},
		{"prefix before substring", "otel", []string{"oteldb-0", "otel-collector", "my-oteldb"}},
		{"case insensitive", "OTELDB", []string{"oteldb-0", "my-oteldb"}},
		{"substring only", "house", []string{"clickhouse-0"}},
		{"no match", "zzz", nil},
		{"spaces trimmed", "  otel  ", []string{"oteldb-0", "otel-collector", "my-oteldb"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, values(Rank(items, tt.query)))
		})
	}
}

func TestRequestKey(t *testing.T) {
	host := Request{Field: FieldHost}
	require.Equal(t, "host", host.Key())
	// The host is part of the key: the same collector on another node is a
	// different result set.
	a := Request{Field: FieldTarget, Transport: source.TransportSSH, Host: "n1", Collector: source.CollectorDocker}
	b := a
	b.Host = "n2"
	require.NotEqual(t, a.Key(), b.Key())
}

func TestParsers(t *testing.T) {
	t.Run("unit", func(t *testing.T) {
		require.Equal(t, []Candidate{{Value: "nginx", State: "running"}},
			parseUnit("nginx.service loaded active running A web server"))
		require.Empty(t, parseUnit("sys-devices.device loaded active plugged"), "only services are offered")
		require.Empty(t, parseUnit(""))
	})
	t.Run("pod", func(t *testing.T) {
		require.Equal(t, []Candidate{{Value: "oteldb/oteldb-0", State: "Running"}},
			parsePod("oteldb oteldb-0 Running oteldb <none>"))
		require.Empty(t, parsePod("lonely"))
	})
	t.Run("container", func(t *testing.T) {
		require.Equal(t, []Candidate{{Value: "app", State: "running", Detail: "app:latest"}},
			parseContainer("app\tapp:latest\trunning"))
		require.Empty(t, parseContainer("\t \t"))
	})
}

func TestListUnknownCollectorIsEmpty(t *testing.T) {
	got, err := list(t.Context(), Request{Collector: source.CollectorCommand})
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestListReportsStderr checks that a missing tool surfaces its own message
// rather than a bare exit status.
func TestListReportsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("listing runs through sh")
	}
	prev := listers[source.CollectorDocker]
	listers[source.CollectorDocker] = lister{sources: []listSource{{
		build: static("echo 'docker: command not found' >&2; exit 127"),
		parse: parseContainer,
	}}}
	t.Cleanup(func() { listers[source.CollectorDocker] = prev })

	_, err := list(t.Context(), Request{Collector: source.CollectorDocker})
	require.ErrorContains(t, err, "command not found")
}

func TestListParsesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("listing runs through sh")
	}
	prev := listers[source.CollectorDocker]
	listers[source.CollectorDocker] = lister{sources: []listSource{{
		build: static("printf 'app\\timage:1\\trunning\\nweb\\timage:2\\texited\\n'"),
		parse: parseContainer,
	}}}
	t.Cleanup(func() { listers[source.CollectorDocker] = prev })

	got, err := list(t.Context(), Request{Collector: source.CollectorDocker})
	require.NoError(t, err)
	require.Equal(t, []string{"app", "web"}, values(got))
}

// TestListMergesSources checks the journal case: user units are listed
// alongside system ones and tagged with the user/ prefix.
func TestListMergesSources(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("listing runs through sh")
	}
	prev := listers[source.CollectorJournal]
	listers[source.CollectorJournal] = lister{sources: []listSource{
		{build: static("printf 'sshd.service loaded active running OpenSSH\\n'"), parse: parseUnit},
		{build: static("printf 'syncthing.service loaded active running Sync\\n'"), parse: parseUserUnit},
	}}
	t.Cleanup(func() { listers[source.CollectorJournal] = prev })

	got, err := list(t.Context(), Request{Collector: source.CollectorJournal})
	require.NoError(t, err)
	require.Equal(t, []Candidate{
		{Value: "sshd", State: "running"},
		{Value: "user/syncthing", State: "running", Detail: "user"},
	}, got)
}

// TestListToleratesFailingSource covers a host with no user session: the user
// manager cannot be reached, but system units still complete.
func TestListToleratesFailingSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("listing runs through sh")
	}
	prev := listers[source.CollectorJournal]
	listers[source.CollectorJournal] = lister{sources: []listSource{
		{build: static("printf 'sshd.service loaded active running OpenSSH\\n'"), parse: parseUnit},
		{build: static("echo 'Failed to connect to user scope bus' >&2; exit 1"), parse: parseUserUnit},
	}}
	t.Cleanup(func() { listers[source.CollectorJournal] = prev })

	got, err := list(t.Context(), Request{Collector: source.CollectorJournal})
	require.NoError(t, err, "one failing source must not hide the others")
	require.Equal(t, []string{"sshd"}, values(got))
}

// TestParsePodContainers: a pod with several containers has to name them,
// because kubectl refuses to pick one, and init containers are what a pod
// stuck in Init is about.
func TestParsePodContainers(t *testing.T) {
	for _, tt := range []struct {
		name string
		line string
		want []Candidate
	}{
		{
			name: "single container is offered bare",
			line: "flux-system helm-controller-7c4 Running manager <none>",
			want: []Candidate{{Value: "flux-system/helm-controller-7c4", State: "Running"}},
		},
		{
			name: "several containers are named",
			line: "cert-manager cert-manager-b77 Running cert-manager-controller,cloudflared-doh <none>",
			want: []Candidate{
				{Value: "cert-manager/cert-manager-b77", State: "Running", Detail: "2 containers"},
				{Value: "cert-manager/cert-manager-b77:cert-manager-controller", State: "Running", Detail: "container"},
				{Value: "cert-manager/cert-manager-b77:cloudflared-doh", State: "Running", Detail: "container"},
			},
		},
		{
			name: "init containers are always named",
			line: "ns pod Init:0/1 app init-db",
			want: []Candidate{
				{Value: "ns/pod", State: "Init:0/1"},
				{Value: "ns/pod:init-db", State: "Init:0/1", Detail: "init container"},
			},
		},
		{
			name: "columns may be missing entirely",
			line: "ns pod Running",
			want: []Candidate{{Value: "ns/pod", State: "Running"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parsePod(tt.line))
		})
	}
}

// TestGuardKeepsTheRealReason: a guard explains what it wanted, but must not
// hide why the check actually failed.
func TestGuardKeepsTheRealReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("listing runs through sh")
	}
	prev := listers[source.CollectorKubectl]
	listers[source.CollectorKubectl] = lister{sources: []listSource{{
		build: static(guard(
			"echo 'sudo: a password is required' >&2; false",
			"no kubernetes context is configured",
			"echo unreachable")),
		parse: parsePod,
	}}}
	t.Cleanup(func() { listers[source.CollectorKubectl] = prev })

	_, err := list(t.Context(), Request{Collector: source.CollectorKubectl})
	require.ErrorContains(t, err, "sudo: a password is required")
	require.ErrorContains(t, err, "no kubernetes context is configured")
}

func TestKubectlCommand(t *testing.T) {
	for _, tt := range []struct {
		name string
		req  Request
		want string
	}{
		{"plain", Request{}, "kubectl"},
		{"elevated", Request{Elevate: true}, "sudo -n kubectl"},
		{
			"with kubeconfig",
			Request{KubeConfig: "/etc/rancher/k3s/k3s.yaml"},
			"kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml",
		},
		{
			"elevated with kubeconfig",
			Request{Elevate: true, KubeConfig: "/etc/rancher/k3s/k3s.yaml"},
			"sudo -n kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, kubectl(tt.req))
		})
	}
}

// TestKubectlListingUsesRequest checks that the pod listing runs with the same
// privileges and config as the stream it is completing for.
// TestKubeContextListing: naming a context is what makes a kubeconfig without
// a current-context usable, so the listing must not require one.
func TestKubeContextListing(t *testing.T) {
	cmd := kubeContextLister.sources[0].build(Request{
		Elevate:     true,
		KubeConfig:  "/root/.kube/reader.kubeconfig",
		KubeContext: "stale",
	})
	require.Contains(t, cmd,
		"sudo -n kubectl --kubeconfig=/root/.kube/reader.kubeconfig config get-contexts -o name")
	require.NotContains(t, cmd, "current-context", "the guard would need the answer being asked for")
	require.NotContains(t, cmd, "--context=stale", "contexts are listed from the file, not through one")

	require.Equal(t, []Candidate{{Value: "reader"}}, parseKubeContext(" reader \n"))
	require.Empty(t, parseKubeContext("  "))
}

func TestKubectlListingUsesRequest(t *testing.T) {
	build := listers[source.CollectorKubectl].sources[0].build
	cmd := build(Request{Elevate: true, KubeConfig: "/etc/rancher/k3s/k3s.yaml"})

	require.Contains(t, cmd, "sudo -n kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml get pods")
	require.Contains(t, cmd, "sudo -n kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml config current-context")
	// Presence of the binary is checked unprivileged.
	require.Contains(t, cmd, "command -v kubectl")
}

// TestUserUnitsAreNeverElevated: sudo would reach root's session bus, not the
// one whose logs are being listed.
func TestUserUnitsAreNeverElevated(t *testing.T) {
	sources := listers[source.CollectorJournal].sources
	require.Contains(t, sources[0].build(Request{Elevate: true}), "sudo -n systemctl")
	require.NotContains(t, sources[1].build(Request{Elevate: true}), "sudo")
}

func TestRequestKeySeparatesPrivilegeAndConfig(t *testing.T) {
	base := Request{Field: FieldTarget, Collector: source.CollectorKubectl}
	elevated, other, ctx := base, base, base
	elevated.Elevate = true
	other.KubeConfig = "/etc/rancher/k3s/k3s.yaml"
	ctx.KubeContext = "reader"

	require.NotEqual(t, base.Key(), elevated.Key())
	require.NotEqual(t, base.Key(), other.Key())
	require.NotEqual(t, elevated.Key(), other.Key())
	require.NotEqual(t, base.Key(), ctx.Key(), "pods differ per context")
}

func TestParseKubeConfig(t *testing.T) {
	require.Equal(t, []Candidate{{Value: "/etc/rancher/k3s/k3s.yaml", Detail: "default"}},
		parseKubeConfig("/etc/rancher/k3s/k3s.yaml\tdefault"))
	require.Equal(t, []Candidate{{Value: "/home/me/.kube/config"}},
		parseKubeConfig("/home/me/.kube/config\t"))
	require.Empty(t, parseKubeConfig("  \t "))
}

// TestKubeConfigProbe runs the real probe against a fake home, checking it
// finds a config and reads its context without touching the system paths.
func TestKubeConfigProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe is a shell loop")
	}
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".kube"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".kube", "config"),
		[]byte("apiVersion: v1\ncurrent-context: my-cluster\n"), 0o600))
	t.Setenv("HOME", home)

	got, err := list(t.Context(), Request{Field: FieldKubeConfig})
	require.NoError(t, err)
	require.Contains(t, values(got), filepath.Join(home, ".kube", "config"))
	for _, c := range got {
		if c.Value == filepath.Join(home, ".kube", "config") {
			require.Equal(t, "my-cluster", c.Detail)
		}
	}
}

func TestParseUserUnit(t *testing.T) {
	require.Equal(t, []Candidate{{Value: "user/syncthing", State: "running", Detail: "user"}},
		parseUserUnit("syncthing.service loaded active running Sync"))

	// A line without a state column still says where the unit lives.
	require.Equal(t, []Candidate{{Value: "user/syncthing", Detail: "user"}},
		parseUserUnit("syncthing.service loaded"))
}

func values(items []Candidate) []string {
	var out []string
	for _, c := range items {
		out = append(out, c.Value)
	}
	return out
}
