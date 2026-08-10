package complete

import (
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
		c, ok := parseUnit("nginx.service loaded active running A web server")
		require.True(t, ok)
		require.Equal(t, Candidate{Value: "nginx", Detail: "running"}, c)

		_, ok = parseUnit("sys-devices.device loaded active plugged")
		require.False(t, ok, "only services are offered")

		_, ok = parseUnit("")
		require.False(t, ok)
	})
	t.Run("pod", func(t *testing.T) {
		c, ok := parsePod("oteldb   oteldb-0   Running")
		require.True(t, ok)
		require.Equal(t, Candidate{Value: "oteldb/oteldb-0", Detail: "Running"}, c)

		_, ok = parsePod("lonely")
		require.False(t, ok)
	})
	t.Run("container", func(t *testing.T) {
		c, ok := parseContainer("app\tapp:latest\trunning")
		require.True(t, ok)
		require.Equal(t, Candidate{Value: "app", Detail: "running · app:latest"}, c)

		_, ok = parseContainer("\t \t")
		require.False(t, ok)
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
		cmd:   "echo 'docker: command not found' >&2; exit 127",
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
		cmd:   "printf 'app\\timage:1\\trunning\\nweb\\timage:2\\texited\\n'",
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
		{cmd: "printf 'sshd.service loaded active running OpenSSH\\n'", parse: parseUnit},
		{cmd: "printf 'syncthing.service loaded active running Sync\\n'", parse: parseUserUnit},
	}}
	t.Cleanup(func() { listers[source.CollectorJournal] = prev })

	got, err := list(t.Context(), Request{Collector: source.CollectorJournal})
	require.NoError(t, err)
	require.Equal(t, []Candidate{
		{Value: "sshd", Detail: "running"},
		{Value: "user/syncthing", Detail: "user · running"},
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
		{cmd: "printf 'sshd.service loaded active running OpenSSH\\n'", parse: parseUnit},
		{cmd: "echo 'Failed to connect to user scope bus' >&2; exit 1", parse: parseUserUnit},
	}}
	t.Cleanup(func() { listers[source.CollectorJournal] = prev })

	got, err := list(t.Context(), Request{Collector: source.CollectorJournal})
	require.NoError(t, err, "one failing source must not hide the others")
	require.Equal(t, []string{"sshd"}, values(got))
}

func TestParseUserUnit(t *testing.T) {
	c, ok := parseUserUnit("syncthing.service loaded active running Sync")
	require.True(t, ok)
	require.Equal(t, Candidate{Value: "user/syncthing", Detail: "user · running"}, c)

	// A line without a state column must not leave a dangling separator.
	c, ok = parseUserUnit("syncthing.service loaded")
	require.True(t, ok)
	require.Equal(t, Candidate{Value: "user/syncthing", Detail: "user"}, c)
}

func values(items []Candidate) []string {
	var out []string
	for _, c := range items {
		out = append(out, c.Value)
	}
	return out
}
