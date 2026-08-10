package complete

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSSHConfig(t *testing.T) {
	for _, tt := range []struct {
		name     string
		in       string
		want     []Candidate
		includes []string
	}{
		{
			name: "alias with hostname and user",
			in: `Host node1
  HostName 10.0.0.1
  User root`,
			want: []Candidate{{Value: "node1", Detail: "root@10.0.0.1"}},
		},
		{
			name: "several aliases share the block",
			in: `Host a b
  HostName example.com`,
			want: []Candidate{
				{Value: "a", Detail: "example.com"},
				{Value: "b", Detail: "example.com"},
			},
		},
		{
			name: "patterns are skipped",
			in: `Host *
  User root
Host web-?
  User root
Host !nope
  User root
Host real`,
			want: []Candidate{{Value: "real"}},
		},
		{
			name: "equals and comments",
			in: `Host=node2 # trailing
  HostName=1.2.3.4`,
			want: []Candidate{{Value: "node2", Detail: "1.2.3.4"}},
		},
		{
			name:     "include is reported not followed",
			in:       "Include conf.d/*.conf extra\nHost after",
			want:     []Candidate{{Value: "after"}},
			includes: []string{"conf.d/*.conf", "extra"},
		},
		{
			name: "match block ends the previous host",
			in: `Host node3
  HostName h3
Match host anything
  User root`,
			want: []Candidate{{Value: "node3", Detail: "h3"}},
		},
		{
			name: "directives without a host block yield nothing",
			in:   "HostName orphan\nUser nobody",
			want: nil,
		},
		{name: "empty", in: "", want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var includes []string
			got := parseSSHConfig(strings.NewReader(tt.in), func(inc []string) {
				includes = append(includes, inc...)
			})
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.includes, includes)
		})
	}
}

func TestConfigHostsFollowsIncludes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755))
	write := func(name, content string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}

	write("config", "Include conf.d/*.conf\nHost direct\n  HostName direct.example\n")
	write("conf.d/a.conf", "Host from-include\n  HostName a.example\n")
	write("conf.d/b.conf", "Host another\n")

	got := configHosts(filepath.Join(dir, "config"), dir)
	require.Equal(t, []Candidate{
		{Value: "from-include", Detail: "a.example"},
		{Value: "another"},
		{Value: "direct", Detail: "direct.example"},
	}, got)
}

// TestConfigHostsIncludeCycle checks that a config including itself terminates.
func TestConfigHostsIncludeCycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	require.NoError(t, os.WriteFile(path, []byte("Include config\nHost loop\n"), 0o600))

	require.Equal(t, []Candidate{{Value: "loop"}}, configHosts(path, dir))
}

func TestConfigHostsMissingFile(t *testing.T) {
	require.Empty(t, configHosts(filepath.Join(t.TempDir(), "nope"), t.TempDir()))
}

func TestKnownHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join([]string{
		"# a comment",
		"node1,10.0.0.1 ssh-ed25519 AAAA",
		"[node2]:2222 ssh-rsa AAAA",
		"|1|hashedhashed|hash= ssh-ed25519 AAAA",
		"malformed",
		"",
	}, "\n")), 0o600))

	require.Equal(t, []Candidate{
		{Value: "node1", Detail: "known_hosts"},
		{Value: "10.0.0.1", Detail: "known_hosts"},
		{Value: "node2", Detail: "known_hosts"},
	}, knownHosts(path))
}

func TestKnownHostsMissingFile(t *testing.T) {
	require.Empty(t, knownHosts(filepath.Join(t.TempDir(), "nope")))
}

func TestDedupeKeepsFirst(t *testing.T) {
	got := dedupe([]Candidate{
		{Value: "a", Detail: "config"},
		{Value: "b"},
		{Value: "a", Detail: "known_hosts"},
	})
	require.Equal(t, []Candidate{{Value: "a", Detail: "config"}, {Value: "b"}}, got)
}

func FuzzParseSSHConfig(f *testing.F) {
	for _, s := range []string{
		"Host a\n  HostName h\n  User u\n",
		"Host *\nInclude x\nMatch host y\n",
		"Host=a#c\n",
		"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		for _, c := range parseSSHConfig(strings.NewReader(in), nil) {
			require.NotEmpty(t, c.Value)
			require.False(t, isPattern(c.Value))
		}
	})
}
