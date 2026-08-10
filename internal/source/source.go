// Package source builds and runs the commands that produce log lines.
package source

import (
	"fmt"
	"strconv"
	"strings"
)

// Transport is how the collector command is executed.
type Transport string

// Supported transports.
const (
	TransportLocal Transport = "local"
	TransportSSH   Transport = "ssh"
)

// Collector is the program that emits the logs.
type Collector string

// Supported collectors.
const (
	CollectorJournal      Collector = "journalctl"
	CollectorKubectl      Collector = "kubectl"
	CollectorDocker       Collector = "docker"
	CollectorCommand      Collector = "command"
	CollectorVictoriaLogs Collector = "victorialogs"
	CollectorLoki         Collector = "loki"
)

// IsRemoteAPI reports whether the collector reads from a log database over
// HTTP. Such a collector runs no command, so the transport, sudo and the
// kubeconfig mean nothing to it.
func (c Collector) IsRemoteAPI() bool {
	return c == CollectorVictoriaLogs || c == CollectorLoki
}

// queryLanguage names what a collector's target is written in, for the messages
// that ask for one.
func (c Collector) queryLanguage() string {
	switch c {
	case CollectorVictoriaLogs:
		return "LogsQL"
	case CollectorLoki:
		return "LogQL"
	default:
		return "query"
	}
}

// Config describes a log stream to open.
type Config struct {
	Transport Transport
	// Host is the ssh destination, [user@]host, used when Transport is [TransportSSH].
	Host string

	Collector Collector

	// Unit is the systemd unit for [CollectorJournal]. Empty means the whole journal.
	Unit string
	// UserUnit reads the user journal rather than the system one.
	UserUnit bool
	// Namespace is the Kubernetes namespace for [CollectorKubectl].
	Namespace string
	// Target is what [CollectorKubectl] reads: a pod name, a "kind/name"
	// reference such as "deploy/api", or, when it contains "=", a label
	// selector. For [CollectorVictoriaLogs] it is the LogsQL query.
	Target string
	// Endpoint is the log API [Collector.IsRemoteAPI] collectors read from.
	Endpoint Endpoint
	// Container narrows [CollectorKubectl] to a container and names the
	// container for [CollectorDocker].
	Container string
	// Args is the verbatim shell command for [CollectorCommand].
	Args string
	// KubeConfig points [CollectorKubectl] at a specific config file, and
	// KubeContext at one context within it. A context is worth naming on its
	// own: a kubeconfig with no current-context is unusable without it.
	KubeConfig  string
	KubeContext string

	// Elevate runs the collector under sudo, for logs or configs a plain user
	// cannot read.
	Elevate bool

	Tail   int
	Follow bool
}

// Validate reports whether the config has everything needed to build a command.
func (c Config) Validate() error {
	if c.Transport == TransportSSH && strings.TrimSpace(c.Host) == "" && !c.Collector.IsRemoteAPI() {
		return fmt.Errorf("ssh transport requires a host")
	}
	switch c.Collector {
	case CollectorVictoriaLogs, CollectorLoki:
		if strings.TrimSpace(c.Endpoint.URL) == "" {
			return fmt.Errorf("%s requires an endpoint", c.Collector)
		}
		if strings.TrimSpace(c.Target) == "" {
			return fmt.Errorf("%s requires a %s query", c.Collector, c.Collector.queryLanguage())
		}
		if c.Collector == CollectorLoki && !strings.Contains(c.Target, "{") {
			// Loki has no bare form: every query selects streams by label, and
			// a query without a selector is a parse error from the server.
			return fmt.Errorf("LogQL needs a stream selector, as in {app=\"api\"}")
		}
	case CollectorKubectl:
		if strings.TrimSpace(c.Target) == "" {
			return fmt.Errorf("kubectl requires a pod, a resource such as deploy/name, or a label selector")
		}
	case CollectorDocker:
		if strings.TrimSpace(c.Container) == "" {
			return fmt.Errorf("docker requires a container")
		}
	case CollectorCommand:
		if strings.TrimSpace(c.Args) == "" {
			return fmt.Errorf("command requires a command line")
		}
	case CollectorJournal:
	default:
		return fmt.Errorf("unknown collector %q", c.Collector)
	}
	return nil
}

// Command returns the shell command producing the logs, without the transport
// wrapper. For a [Collector.IsRemoteAPI] collector there is no command, and the
// result only describes the query; it is never executed.
func (c Config) Command() string {
	switch c.Collector {
	case CollectorVictoriaLogs:
		return "logsql " + Quote(c.vlogsQuery())
	case CollectorLoki:
		return "logql " + Quote(c.lokiQuery())
	case CollectorJournal:
		args := []string{"journalctl"}
		if c.UserUnit {
			args = append(args, "--user")
		}
		args = append(args, "--no-pager", "-o", "cat")
		if u := strings.TrimSpace(c.Unit); u != "" {
			args = append(args, "-u", Quote(u))
		}
		if c.Tail > 0 {
			args = append(args, "-n", strconv.Itoa(c.Tail))
		}
		if c.Follow {
			args = append(args, "-f")
		}
		return c.elevated(args)
	case CollectorKubectl:
		args := []string{"kubectl"}
		// --kubeconfig rather than the KUBECONFIG environment variable: a
		// sudoers rule naming kubectl permits "sudo kubectl" but not
		// "sudo env ..." or "sudo sh -c ...".
		if k := strings.TrimSpace(c.KubeConfig); k != "" {
			args = append(args, "--kubeconfig="+Quote(k))
		}
		if k := strings.TrimSpace(c.KubeContext); k != "" {
			args = append(args, "--context="+Quote(k))
		}
		args = append(args, "logs")
		if ns := strings.TrimSpace(c.Namespace); ns != "" {
			args = append(args, "-n", Quote(ns))
		}
		switch t := strings.TrimSpace(c.Target); {
		case strings.Contains(t, "="):
			args = append(args, "-l", Quote(t))
		case t != "":
			args = append(args, Quote(t))
		}
		if ct := strings.TrimSpace(c.Container); ct != "" {
			args = append(args, "-c", Quote(ct))
		}
		if c.Tail > 0 {
			args = append(args, "--tail", strconv.Itoa(c.Tail))
		}
		if c.Follow {
			args = append(args, "-f")
		}
		return c.elevated(args)
	case CollectorDocker:
		args := []string{"docker", "logs"}
		if c.Tail > 0 {
			args = append(args, "--tail", strconv.Itoa(c.Tail))
		}
		if c.Follow {
			args = append(args, "-f")
		}
		if ct := strings.TrimSpace(c.Container); ct != "" {
			args = append(args, Quote(ct))
		}
		return c.elevated(args)
	default:
		cmd := strings.TrimSpace(c.Args)
		if c.Elevate {
			// A free-form command may contain pipes, so it needs a shell.
			return "sudo -n sh -c " + Quote(cmd)
		}
		return cmd
	}
}

// elevated joins argv, prefixing sudo directly onto the collector so a sudoers
// rule can name the tool itself. Wrapping it in a shell would defeat such a
// rule, and -n keeps a password prompt from stalling the view.
func (c Config) elevated(args []string) string {
	if c.Elevate {
		args = append([]string{"sudo", "-n"}, args...)
	}
	return strings.Join(args, " ")
}

// Argv returns the local process to spawn.
//
// Shelling out to ssh(1) rather than dialing ourselves keeps ~/.ssh/config,
// ProxyJump, the agent and known_hosts working without reimplementing them.
func (c Config) Argv() []string {
	if c.Collector.IsRemoteAPI() {
		return nil
	}
	cmd := c.Command()
	if c.Transport != TransportSSH {
		return []string{"sh", "-c", cmd}
	}
	args := []string{
		"ssh",
		// BatchMode never prompts: an unknown host key or a passphrase without
		// an agent fails fast on stderr instead of hanging the TUI.
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		// Notice a dead peer during follow instead of waiting forever.
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	if c.Follow {
		// Force a pty so sshd hangs up the remote command when we disconnect;
		// without one a follower like "journalctl -f" is orphaned on the host.
		// The cost is CRLF line endings and stderr folded into stdout.
		args = append(args, "-tt")
	} else {
		args = append(args, "-T")
	}
	return append(args, strings.TrimSpace(c.Host), cmd)
}

// Title is a short human-readable description of the stream.
func (c Config) Title() string {
	where := "local"
	if c.Collector.IsRemoteAPI() {
		// The endpoint stands in for the host, and carries a token that must not
		// reach the screen.
		return string(c.Collector) + "://" + c.Endpoint.Label() + " · " + c.Command()
	}
	if c.Transport == TransportSSH {
		where = "ssh://" + strings.TrimSpace(c.Host)
	}
	return where + " · " + c.Command()
}

// Quote wraps s in single quotes unless it is already a bare shell-safe word.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case strings.ContainsRune("-_./=:@,+", r):
			return false
		}
		return true
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Scope prefixes for the compact journal target syntax. A unit name can never
// contain a slash, so they are unambiguous.
const (
	UserUnitPrefix   = "user/"
	SystemUnitPrefix = "system/"
)

// ParseJournalTarget splits the compact journal target syntax "[user/]unit".
func ParseJournalTarget(s string) (unit string, user bool) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, UserUnitPrefix); ok {
		return strings.TrimSpace(rest), true
	}
	rest, _ := strings.CutPrefix(s, SystemUnitPrefix)
	return strings.TrimSpace(rest), false
}

// kubeKinds are the resource kinds "kubectl logs" reads from besides a pod,
// under every name kubectl accepts for them. A leading segment naming one is a
// resource reference rather than a namespace, which is what tells "deploy/api"
// apart from "oteldb/api".
var kubeKinds = map[string]bool{
	"pod": true, "po": true,
	"deployment": true, "deploy": true,
	"statefulset": true, "sts": true,
	"daemonset": true, "ds": true,
	"replicaset": true, "rs": true,
	"replicationcontroller": true, "rc": true,
	"job": true, "cronjob": true, "cj": true,
	"service": true, "svc": true,
}

// IsKubeKind reports whether s names a resource kind, with or without its API
// group and plural s, as in "deploy", "deployments" or "deployment.apps".
func IsKubeKind(s string) bool {
	kind, _, _ := strings.Cut(strings.ToLower(s), ".")
	return kubeKinds[kind] || kubeKinds[strings.TrimSuffix(kind, "s")]
}

// ParseKubeTarget splits the compact kubectl target syntax
// "[namespace/]pod-selector-or-kind/name[:container]".
func ParseKubeTarget(s string) (namespace, target, container string) {
	s = strings.TrimSpace(s)
	if rest, ct, ok := strings.Cut(s, ":"); ok {
		s, container = rest, ct
	}
	switch parts := strings.SplitN(s, "/", 3); {
	case len(parts) == 3:
		namespace, target = parts[0], parts[1]+"/"+parts[2]
	case len(parts) == 2 && !IsKubeKind(parts[0]):
		namespace, target = parts[0], parts[1]
	default:
		target = s
	}
	return namespace, strings.TrimPrefix(target, "-l "), container
}
