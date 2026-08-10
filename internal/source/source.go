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
	CollectorJournal Collector = "journalctl"
	CollectorKubectl Collector = "kubectl"
	CollectorDocker  Collector = "docker"
	CollectorCommand Collector = "command"
)

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
	// Target is the pod name for [CollectorKubectl]. A value containing "=" is
	// used as a label selector instead.
	Target string
	// Container narrows [CollectorKubectl] to a container and names the
	// container for [CollectorDocker].
	Container string
	// Args is the verbatim shell command for [CollectorCommand].
	Args string
	// KubeConfig points [CollectorKubectl] at a specific config file.
	KubeConfig string

	// Elevate runs the collector under sudo, for logs or configs a plain user
	// cannot read.
	Elevate bool

	Tail   int
	Follow bool
}

// Validate reports whether the config has everything needed to build a command.
func (c Config) Validate() error {
	if c.Transport == TransportSSH && strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("ssh transport requires a host")
	}
	switch c.Collector {
	case CollectorKubectl:
		if strings.TrimSpace(c.Target) == "" {
			return fmt.Errorf("kubectl requires a pod name or label selector")
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

// Command returns the shell command producing the logs, without the transport wrapper.
func (c Config) Command() string {
	switch c.Collector {
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

// ParseKubeTarget splits the compact kubectl target syntax
// "[namespace/]pod-or-selector[:container]".
func ParseKubeTarget(s string) (namespace, target, container string) {
	s = strings.TrimSpace(s)
	if ns, rest, ok := strings.Cut(s, "/"); ok {
		namespace, s = ns, rest
	}
	if rest, ct, ok := strings.Cut(s, ":"); ok {
		s, container = rest, ct
	}
	return namespace, strings.TrimPrefix(s, "-l "), container
}
