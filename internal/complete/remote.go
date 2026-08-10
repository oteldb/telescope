package complete

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/source"
)

// maxCandidates caps a listing so a large cluster cannot flood the prompt.
const maxCandidates = 5000

// none is what kubectl's custom-columns prints for a field an object does not
// have, which is how an absent count is told from a zero one.
const none = "<none>"

// guard makes cmd fail fast, with an explanation instead of a bare exit code,
// when check does not succeed. It runs on the host the listing targets, so a
// tool missing on a remote node costs one round trip and nothing more.
//
// Guards catch what is knowable cheaply: a tool that is not installed, a
// kubeconfig with no context. They cannot catch an unreachable cluster, which
// has a perfectly valid context and only reveals itself by hanging; that case
// is bounded by [Timeout] instead.
func guard(check, msg, cmd string) string {
	// The check's own stderr is kept: when sudo refuses or a host key is
	// unknown, that reason matters more than the guard's summary, which would
	// otherwise blame a missing context for someone else's failure.
	return fmt.Sprintf("%s >/dev/null || { echo %q >&2; exit 1; }; %s", check, msg, cmd)
}

// listSource is one command producing candidates.
type listSource struct {
	// build returns the command to run through the request's transport. It
	// takes the request because privileges and the kubeconfig change it.
	build func(r Request) string
	// parse turns one output line into candidates. A line can yield several:
	// a pod offers its containers alongside itself.
	parse func(line string) []Candidate
}

// static is a listing command that does not depend on the request.
func static(cmd string) func(Request) string {
	return func(Request) string { return cmd }
}

// sudo prefixes a command when the request asks for elevation. The tool is
// named directly so a sudoers rule for it applies; see [source.Config.Command].
func sudo(r Request, cmd string) string {
	if r.Elevate {
		return "sudo -n " + cmd
	}
	return cmd
}

// kubectl is the kubectl invocation a request implies, honoring elevation and
// the chosen kubeconfig.
func kubectl(r Request) string {
	cmd := sudo(r, "kubectl")
	if k := strings.TrimSpace(r.KubeConfig); k != "" {
		cmd += " --kubeconfig=" + source.Quote(k)
	}
	if k := strings.TrimSpace(r.KubeContext); k != "" {
		cmd += " --context=" + source.Quote(k)
	}
	return cmd
}

// lister enumerates the targets of one collector. Several sources are merged
// in order, and a source that fails is skipped as long as another produced
// something.
type lister struct {
	sources []listSource
}

// unitFields lists services in a stable, parseable shape.
const unitFields = " list-units --type=service --all --no-legend --plain --no-pager"

// userBus points systemctl at the caller's session bus. A non-interactive ssh
// command inherits neither XDG_RUNTIME_DIR nor DBUS_SESSION_BUS_ADDRESS, so
// without this the user listing always fails on a remote host.
const userBus = `XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}" `

// listers enumerates targets per collector. Collectors absent from the map
// offer no completion.
var listers = map[source.Collector]lister{
	source.CollectorJournal: {sources: []listSource{
		{
			build: func(r Request) string {
				return guard(haveSystemctl, noSystemctl, sudo(r, "systemctl")+unitFields)
			},
			parse: parseUnit,
		},
		{
			// The user manager is never elevated: sudo would reach root's
			// session, not the one whose logs are being read.
			build: static(guard(haveSystemctl, noSystemctl, userBus+"systemctl --user"+unitFields)),
			parse: parseUserUnit,
		},
	}},
	source.CollectorKubectl: {sources: []listSource{
		{
			build: func(r Request) string {
				// No guard on current-context: kubectl cannot tell a missing
				// kubeconfig from one without a context ("config current-context"
				// reports the latter for both), while "get pods" names the real
				// problem and fails just as fast, both being local checks.
				return guard("command -v kubectl", "kubectl is not installed",
					kubectl(r)+" get pods --all-namespaces --no-headers -o custom-columns="+
						":.metadata.namespace,:.metadata.name,:.status.phase,"+
						":.spec.containers[*].name,:.spec.initContainers[*].name")
			},
			parse: parsePod,
		},
		{
			// Workloads outlive the pods under them, so following one survives a
			// restart that a pod name does not.
			build: func(r Request) string {
				return kubectl(r) + " get deployments,statefulsets,daemonsets" +
					" --all-namespaces --no-headers -o custom-columns=" +
					":.kind,:.metadata.namespace,:.metadata.name," +
					":.spec.template.spec.containers[*].name," +
					":.status.readyReplicas,:.status.replicas," +
					":.status.numberReady,:.status.desiredNumberScheduled"
			},
			parse: parseWorkload,
		},
	}},
	source.CollectorDocker: {sources: []listSource{{
		build: func(r Request) string {
			return guard("command -v docker", "docker is not installed",
				sudo(r, "docker")+` ps -a --format '{{.Names}}\t{{.Image}}\t{{.State}}'`)
		},
		parse: parseContainer,
	}}},
}

const (
	haveSystemctl = "command -v systemctl"
	noSystemctl   = "systemctl is not installed"
)

// list merges every source of the collector's lister. An error is only
// reported when nothing at all could be listed: a host without a user session
// still completes its system units.
func list(ctx context.Context, r Request) ([]Candidate, error) {
	l, ok := listers[r.Collector]
	switch r.Field {
	case FieldKubeConfig:
		l, ok = kubeConfigLister, true
	case FieldKubeContext:
		l, ok = kubeContextLister, true
	}
	if !ok {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	var (
		out      []Candidate
		firstErr error
	)
	for _, src := range l.sources {
		items, err := run(ctx, r, src, maxCandidates-len(out))
		if err != nil && firstErr == nil {
			firstErr = err
		}
		out = append(out, items...)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return dedupe(out), nil
}

// run executes one listing command and parses at most limit candidates.
func run(ctx context.Context, r Request, src listSource, limit int) ([]Candidate, error) {
	s, err := source.Start(ctx, source.Config{
		Transport: r.Transport,
		Host:      r.Host,
		Collector: source.CollectorCommand,
		Args:      src.build(r),
	})
	if err != nil {
		return nil, err
	}
	defer s.Close()

	var (
		out    []Candidate
		errOut []string
	)
	for line := range s.Lines() {
		if line.Stderr {
			if len(errOut) < 4 {
				errOut = append(errOut, string(line.Data))
			}
			continue
		}
		if len(out) >= limit {
			continue
		}
		out = append(out, src.parse(string(line.Data))...)
	}

	if err := <-s.Done(); err != nil && len(out) == 0 {
		if msg := strings.TrimSpace(strings.Join(errOut, "; ")); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, errors.Wrap(err, "list")
	}
	return out, nil
}

// parseUnit reads a line of "systemctl list-units --plain" output:
// "nginx.service loaded active running A web server".
func parseUnit(line string) []Candidate {
	f := strings.Fields(line)
	if len(f) == 0 || !strings.HasSuffix(f[0], ".service") {
		return nil
	}
	c := Candidate{Value: strings.TrimSuffix(f[0], ".service")}
	if len(f) >= 4 {
		c.State = f[3]
	}
	return []Candidate{c}
}

// parseUserUnit reads the same output as [parseUnit] for a user manager,
// tagging the candidate so it reaches journalctl with --user.
func parseUserUnit(line string) []Candidate {
	out := parseUnit(line)
	for i := range out {
		out[i].Value = source.UserUnitPrefix + out[i].Value
		out[i].Detail = "user"
	}
	return out
}

// parsePod reads "namespace name phase containers initContainers" and emits the
// compact "ns/pod" target plus a "ns/pod:container" for every container worth
// naming.
//
// A pod with one container is offered on its own, since kubectl picks that
// container by default. A pod with several is offered both ways: bare for the
// default-container annotation, and per container because otherwise kubectl
// refuses to choose.
func parsePod(line string) []Candidate {
	f := strings.Fields(line)
	if len(f) < 2 {
		return nil
	}
	pod := f[0] + "/" + f[1]

	state := ""
	if len(f) >= 3 {
		state = f[2]
	}
	containers := splitColumn(get(f, 3))
	inits := splitColumn(get(f, 4))

	head := Candidate{Value: pod, State: state}
	if len(containers) > 1 {
		head.Detail = strconv.Itoa(len(containers)) + " containers"
	}
	out := []Candidate{head}

	if len(containers) > 1 {
		for _, c := range containers {
			out = append(out, Candidate{Value: pod + ":" + c, State: state, Detail: "container"})
		}
	}
	// Init containers are always named: they are never the default, and their
	// logs are what a pod stuck in Init is about.
	for _, c := range inits {
		out = append(out, Candidate{Value: pod + ":" + c, State: state, Detail: "init container"})
	}
	return out
}

// parseWorkload reads "kind namespace name containers ready replicas
// numberReady desiredNumberScheduled" and emits the "ns/kind/name" target that
// kubectl logs accepts in place of a pod.
//
// The two pairs of replica counts are the deployment and daemonset spellings of
// the same thing; whichever the kind fills in is the one reported.
func parseWorkload(line string) []Candidate {
	f := strings.Fields(line)
	if len(f) < 3 {
		return nil
	}
	kind := strings.ToLower(f[0])
	if !source.IsKubeKind(kind) {
		return nil
	}
	name := f[1] + "/" + kind + "/" + f[2]

	// The desired count decides which pair the kind filled in: a deployment
	// with nothing ready yet still reports how many it wants.
	ready, want := get(f, 4), get(f, 5)
	if want == none || want == "" {
		ready, want = get(f, 6), get(f, 7)
	}
	detail := kind
	if want != "" && want != none {
		if ready == none {
			ready = "0"
		}
		detail = kind + " · " + ready + "/" + want + " ready"
	}

	out := []Candidate{{Value: name, Detail: detail}}
	if containers := splitColumn(get(f, 3)); len(containers) > 1 {
		for _, c := range containers {
			out = append(out, Candidate{Value: name + ":" + c, Detail: kind + " container"})
		}
	}
	return out
}

// splitColumn reads a custom-columns list, which is comma separated and prints
// <none> when the field is absent.
func splitColumn(s string) []string {
	if s == "" || s == none {
		return nil
	}
	return strings.Split(s, ",")
}

func get(f []string, i int) string {
	if i < len(f) {
		return f[i]
	}
	return ""
}

// parseContainer reads the tab-separated docker ps format.
func parseContainer(line string) []Candidate {
	f := strings.Split(line, "\t")
	if len(f) == 0 || strings.TrimSpace(f[0]) == "" {
		return nil
	}
	c := Candidate{Value: strings.TrimSpace(f[0])}
	if len(f) >= 3 {
		c.State, c.Detail = strings.TrimSpace(f[2]), strings.TrimSpace(f[1])
	}
	return []Candidate{c}
}
