package source

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigCommand(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "journal all",
			cfg:  Config{Collector: CollectorJournal, Follow: true},
			want: "journalctl --no-pager -o cat -f",
		},
		{
			name: "journal unit tail",
			cfg:  Config{Collector: CollectorJournal, Unit: "kubelet", Tail: 100},
			want: "journalctl --no-pager -o cat -u kubelet -n 100",
		},
		{
			name: "journal user unit",
			cfg:  Config{Collector: CollectorJournal, Unit: "syncthing", UserUnit: true, Follow: true},
			want: "journalctl --user --no-pager -o cat -u syncthing -f",
		},
		{
			name: "whole user journal",
			cfg:  Config{Collector: CollectorJournal, UserUnit: true},
			want: "journalctl --user --no-pager -o cat",
		},
		{
			name: "kubectl pod",
			cfg:  Config{Collector: CollectorKubectl, Namespace: "oteldb", Target: "oteldb-0", Follow: true},
			want: "kubectl logs -n oteldb oteldb-0 -f",
		},
		{
			name: "kubectl selector and container",
			cfg:  Config{Collector: CollectorKubectl, Target: "app=oteldb", Container: "ch", Tail: 10},
			want: "kubectl logs -l app=oteldb -c ch --tail 10",
		},
		{
			name: "docker",
			cfg:  Config{Collector: CollectorDocker, Container: "clickhouse", Tail: 5, Follow: true},
			want: "docker logs --tail 5 -f clickhouse",
		},
		{
			name: "command verbatim",
			cfg:  Config{Collector: CollectorCommand, Args: "tail -F /var/log/app.log"},
			want: "tail -F /var/log/app.log",
		},
		{
			name: "quotes unsafe unit",
			cfg:  Config{Collector: CollectorJournal, Unit: "weird unit"},
			want: "journalctl --no-pager -o cat -u 'weird unit'",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.cfg.Command())
		})
	}
}

func TestConfigArgv(t *testing.T) {
	cfg := Config{Collector: CollectorDocker, Container: "app"}
	require.Equal(t, []string{"sh", "-c", "docker logs app"}, cfg.Argv())

	cfg.Transport, cfg.Host = TransportSSH, "user@node1"
	argv := cfg.Argv()
	require.Equal(t, "ssh", argv[0])
	require.Contains(t, argv, "BatchMode=yes")
	require.Contains(t, argv, "-T")
	require.Equal(t, []string{"user@node1", "docker logs app"}, argv[len(argv)-2:])

	// Follow forces a pty so the remote command dies with the connection.
	cfg.Follow = true
	require.Contains(t, cfg.Argv(), "-tt")
	require.NotContains(t, cfg.Argv(), "-T")
}

func TestConfigValidate(t *testing.T) {
	for _, tt := range []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"journal needs nothing", Config{Collector: CollectorJournal}, false},
		{"ssh needs host", Config{Transport: TransportSSH, Collector: CollectorJournal}, true},
		{"kubectl needs target", Config{Collector: CollectorKubectl}, true},
		{"docker needs container", Config{Collector: CollectorDocker}, true},
		{"command needs args", Config{Collector: CollectorCommand}, true},
		{"unknown collector", Config{Collector: "nope"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestParseJournalTarget(t *testing.T) {
	for _, tt := range []struct {
		in       string
		unit     string
		userUnit bool
	}{
		{"kubelet", "kubelet", false},
		{"user/syncthing", "syncthing", true},
		{"system/kubelet", "kubelet", false},
		{"  user/syncthing  ", "syncthing", true},
		{"", "", false},
		// A template unit keeps its @; only a leading scope is stripped.
		{"user/getty@tty1", "getty@tty1", true},
	} {
		t.Run(tt.in, func(t *testing.T) {
			unit, user := ParseJournalTarget(tt.in)
			require.Equal(t, tt.unit, unit)
			require.Equal(t, tt.userUnit, user)
		})
	}
}

func TestParseKubeTarget(t *testing.T) {
	for _, tt := range []struct {
		in                        string
		ns, target, containerWant string
	}{
		{"oteldb-0", "", "oteldb-0", ""},
		{"oteldb/oteldb-0", "oteldb", "oteldb-0", ""},
		{"oteldb/oteldb-0:clickhouse", "oteldb", "oteldb-0", "clickhouse"},
		{"oteldb/app=oteldb", "oteldb", "app=oteldb", ""},
		{"oteldb/-l app=oteldb", "oteldb", "app=oteldb", ""},
		{"  spaced  ", "", "spaced", ""},
	} {
		t.Run(tt.in, func(t *testing.T) {
			ns, target, container := ParseKubeTarget(tt.in)
			require.Equal(t, tt.ns, ns)
			require.Equal(t, tt.target, target)
			require.Equal(t, tt.containerWant, container)
		})
	}
}
