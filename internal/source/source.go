// Package source builds and runs the commands that produce log lines.
package source

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oteldb/telescope/internal/query"
)

// journalStamp is the timestamp journalctl reads, in the local time it is
// written in.
const journalStamp = "2006-01-02 15:04:05"

// Transport is how the collector command is executed.
type Transport string

// Supported transports.
const (
	TransportLocal Transport = "local"
	TransportSSH   Transport = "ssh"
)

// Collector is the program that emits the logs, or the API they are read
// from. A trace store is named the same way and by the same type: it is one
// more API an [Endpoint] can speak, and an endpoint has one field for what
// speaks there.
type Collector string

// Supported collectors.
const (
	CollectorJournal      Collector = "journalctl"
	CollectorKubectl      Collector = "kubectl"
	CollectorDocker       Collector = "docker"
	CollectorCommand      Collector = "command"
	CollectorVictoriaLogs Collector = "victorialogs"
	CollectorLoki         Collector = "loki"
	CollectorMerge        Collector = "merge"
)

// The trace APIs. Neither streams anything, so neither is a collector a
// [Config] may name: they say what answers at a `traces:` url and nothing else.
const (
	CollectorTempo  Collector = "tempo"
	CollectorJaeger Collector = "jaeger"
)

// IsTraceAPI reports whether the collector reads traces rather than logs.
func (c Collector) IsTraceAPI() bool {
	return c == CollectorTempo || c == CollectorJaeger
}

// IsRemoteAPI reports whether the collector reads from a log database over
// HTTP. Such a collector runs no command, so the transport, sudo and the
// kubeconfig mean nothing to it.
func (c Collector) IsRemoteAPI() bool {
	return c == CollectorVictoriaLogs || c == CollectorLoki
}

// Config describes a log stream to open.
type Config struct {
	// Name is what the stream is called. It is what a merge tags this stream's
	// lines with; elsewhere it is only a label.
	Name string

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
	// Traces is where this stream's traces are read from, when it has any. It
	// is not a collector and nothing streams from it: a line carrying a trace
	// id is the only reason it is here, and a stream that has none never asks.
	//
	// It travels with the stream rather than being looked up again because the
	// view has the stream and not the config file it came from, and a merge's
	// children may each answer to a different one.
	Traces Endpoint
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

	// Merge is what [CollectorMerge] reads: several streams shown as one. The
	// window, tail and follow of the merge apply to all of them, since a merge
	// is a single view and has a single timeline.
	Merge []Config

	// Elevate runs the collector under sudo, for logs or configs a plain user
	// cannot read.
	Elevate bool

	// Filter is the query the view is filtering by, given to the sources that
	// can answer it themselves. It never narrows what is shown: what a source
	// cannot be asked, the view still asks of every line it gets back.
	Filter query.Expr

	// Stamp asks the collector to report the time of each line out of band, in
	// [Line.At]. It costs a flag the collector may not have, so it is only
	// asked for where it is needed: ordering a merge.
	Stamp bool

	// Range bounds the window read. The zero value reads up to now, which is
	// what a plain tail does.
	Range Range

	Tail   int
	Follow bool
}

// Stamps reports whether the collector was asked to timestamp its lines and can
// do it. journalctl is read with -o cat, which is the message and nothing else,
// so a merged journal is ordered by when its lines arrive.
func (c Config) Stamps() bool {
	return c.Stamp && (c.Collector == CollectorDocker || c.Collector == CollectorKubectl)
}

// following reports whether the stream stays open. A range with an end is a
// window that has already happened: there is nothing left to arrive in it.
func (c Config) following() bool { return c.Follow && !c.Range.Closed() }

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
		if err := c.Endpoint.ProxyError(); err != nil {
			return err
		}
		// Neither names a query of its own beyond the endpoint: LogsQL has a
		// match-all, and LogQL is compiled from the filter, which belongs to
		// the view and not to the place. What a Loki stream cannot open without
		// is a label to select by, and that is [startAPI]'s to refuse, since
		// the filter changes while the place does not.
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
	case CollectorMerge:
		if len(c.Merge) < 2 {
			return fmt.Errorf("a merge reads two or more sources")
		}
		for _, sub := range c.Children() {
			if sub.Collector == CollectorMerge {
				return fmt.Errorf("merged %s: a merge cannot contain a merge", sub.Label())
			}
			if err := sub.Validate(); err != nil {
				return fmt.Errorf("merged %s: %w", sub.Label(), err)
			}
		}
		return nil
	case CollectorJournal:
	default:
		return fmt.Errorf("unknown collector %q", c.Collector)
	}
	if !c.Range.IsZero() {
		switch c.Collector {
		case CollectorCommand:
			return fmt.Errorf("a command has no time range: bound it in the command itself")
		case CollectorKubectl:
			if c.Range.Closed() {
				// kubectl logs takes --since-time and nothing that ends it.
				return fmt.Errorf("kubectl has no end bound: use an open range, such as 1h")
			}
		}
	}
	return nil
}

// WithFilter returns the config as it would be opened while the view filters by
// f, which is what a source able to answer part of the filter is asked.
func (c Config) WithFilter(f query.Expr) Config {
	c.Filter = f
	return c
}

// WithTarget writes what was typed at the prompt into the field the collector
// reads it from.
//
// For a merge it answers every child that was still being asked, and only
// those: a group of clusters running the same deployment is asked once, while a
// place that already named a pod keeps it.
//
// A kubectl target names up to three things — namespace, workload, container —
// and what it does not name is left as the config declared it. A place that
// says "namespace: octo" and is then asked for "app=octo-api" is being told
// which workload and not being told to forget which namespace; naming one in
// the target still wins, since the argument is more specific than the file.
func (c Config) WithTarget(target string) Config {
	if c.Collector == CollectorMerge {
		merged := make([]Config, 0, len(c.Merge))
		for _, sub := range c.Merge {
			if sub.Validate() != nil {
				sub = sub.WithTarget(target)
			}
			merged = append(merged, sub)
		}
		c.Merge = merged
		return c
	}
	switch c.Collector {
	case CollectorJournal:
		c.Unit, c.UserUnit = ParseJournalTarget(target)
	case CollectorKubectl:
		namespace, workload, container := ParseKubeTarget(target)
		c.Target = workload
		if namespace != "" {
			c.Namespace = namespace
		}
		if container != "" {
			c.Container = container
		}
	case CollectorDocker:
		c.Container = target
	case CollectorCommand:
		c.Args = target
	default:
		c.Target = target
	}
	return c
}

// Pushed is the query each source is actually sent, one per stream and in the
// order they are read, so a view can tell whether changing the filter would ask
// anything different. A source that answers no query of its own contributes an
// empty string.
func (c Config) Pushed() []string {
	switch c.Collector {
	case CollectorMerge:
		var out []string
		for _, sub := range c.Children() {
			out = append(out, sub.Pushed()...)
		}
		return out
	case CollectorVictoriaLogs:
		return []string{c.vlogsQuery()}
	case CollectorLoki:
		// The empty string when the filter names no label, which is how a view
		// with nothing to ask yet tells itself apart from one that has asked.
		q, _ := c.lokiQuery()
		return []string{q}
	default:
		return []string{""}
	}
}

// Command returns the shell command producing the logs, without the transport
// wrapper. For a [Collector.IsRemoteAPI] collector there is no command, and the
// result only describes the query; it is never executed.
func (c Config) Command() string {
	switch c.Collector {
	case CollectorMerge:
		return "merge " + strings.Join(c.Labels(), " + ")
	case CollectorVictoriaLogs:
		return "logsql " + Quote(c.vlogsQuery())
	case CollectorLoki:
		q, ok := c.lokiQuery()
		if !ok {
			// The filter selects no stream yet. Why that is worth saying is the
			// view's to say, once, where it says everything else that failed.
			return "logql"
		}
		return "logql " + Quote(q)
	case CollectorJournal:
		args := []string{"journalctl"}
		if c.UserUnit {
			args = append(args, "--user")
		}
		args = append(args, "--no-pager", "-o", "cat")
		if u := strings.TrimSpace(c.Unit); u != "" {
			args = append(args, "-u", Quote(u))
		}
		// journalctl reads a local timestamp, which is what the range resolves
		// to, and needs it quoted for the space in the middle.
		if t := c.Range.Since; !t.IsZero() {
			args = append(args, "--since", Quote(t.Format(journalStamp)))
		}
		if t := c.Range.Until; !t.IsZero() {
			args = append(args, "--until", Quote(t.Format(journalStamp)))
		}
		if c.Tail > 0 {
			args = append(args, "-n", strconv.Itoa(c.Tail))
		}
		if c.following() {
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
		if c.Prefixes() {
			args = append(args, "--prefix")
		}
		if c.Stamps() {
			args = append(args, "--timestamps")
		}
		if t := c.Range.Since; !t.IsZero() {
			args = append(args, "--since-time="+t.Format(time.RFC3339))
		}
		// Written whenever it is set rather than only when it is positive: a
		// page asks for [allLines], which kubectl spells -1 and needs told,
		// since a selector it was not told about defaults to ten.
		if c.Tail != 0 {
			args = append(args, "--tail", strconv.Itoa(c.Tail))
		}
		if c.following() {
			args = append(args, "-f")
		}
		return c.elevated(args)
	case CollectorDocker:
		args := []string{"docker", "logs"}
		if c.Stamps() {
			args = append(args, "--timestamps")
		}
		// To the nanosecond, which docker reads and a whole second does not: a
		// page ends just short of the line the reader is holding, and rounded
		// down to the second that end would fall below lines it must not lose.
		if t := c.Range.Since; !t.IsZero() {
			args = append(args, "--since", t.Format(time.RFC3339Nano))
		}
		if t := c.Range.Until; !t.IsZero() {
			args = append(args, "--until", t.Format(time.RFC3339Nano))
		}
		if c.Tail > 0 {
			args = append(args, "--tail", strconv.Itoa(c.Tail))
		}
		if c.following() {
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
func (c Config) Argv() []string { return c.argvFor(c.Command()) }

// argvFor wraps one command in the transport that runs it, which is the half of
// [Config.Argv] that does not depend on what the command is: the pod watch is
// reached the same way the logs are.
func (c Config) argvFor(cmd string) []string {
	if c.Collector.IsRemoteAPI() || c.Collector == CollectorMerge {
		// Neither runs a command: one is HTTP, the other is several streams.
		return nil
	}
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
	if c.following() {
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
	if c.Collector == CollectorMerge {
		// A merge has no single where: each source carries its own, and the
		// labels naming them are the whole of it.
		title := c.Command()
		if !c.Range.IsZero() {
			title += " · " + c.Range.Label()
		}
		return title
	}
	if c.Collector.IsRemoteAPI() {
		// The endpoint stands in for the host, and carries a token that must not
		// reach the screen. The range is not in the query either, since it is
		// sent beside it rather than in it.
		title := string(c.Collector) + "://" + c.Endpoint.Label() + " · " + c.Command()
		if !c.Range.IsZero() {
			title += " · " + c.Range.Label()
		}
		return title
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
