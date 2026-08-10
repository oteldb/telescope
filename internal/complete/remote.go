package complete

import (
	"context"
	"strings"

	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/source"
)

// maxCandidates caps a listing so a large cluster cannot flood the prompt.
const maxCandidates = 5000

// listSource is one command producing candidates.
type listSource struct {
	// cmd is run through the request's transport.
	cmd string
	// parse turns one output line into a candidate.
	parse func(line string) (Candidate, bool)
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
		{cmd: "systemctl" + unitFields, parse: parseUnit},
		{cmd: userBus + "systemctl --user" + unitFields, parse: parseUserUnit},
	}},
	source.CollectorKubectl: {sources: []listSource{{
		cmd: "kubectl get pods --all-namespaces --no-headers " +
			"-o custom-columns=:.metadata.namespace,:.metadata.name,:.status.phase",
		parse: parsePod,
	}}},
	source.CollectorDocker: {sources: []listSource{{
		cmd:   `docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.State}}'`,
		parse: parseContainer,
	}}},
}

// list merges every source of the collector's lister. An error is only
// reported when nothing at all could be listed: a host without a user session
// still completes its system units.
func list(ctx context.Context, r Request) ([]Candidate, error) {
	l, ok := listers[r.Collector]
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
		Args:      src.cmd,
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
		if c, ok := src.parse(string(line.Data)); ok {
			out = append(out, c)
		}
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
func parseUnit(line string) (Candidate, bool) {
	f := strings.Fields(line)
	if len(f) == 0 || !strings.HasSuffix(f[0], ".service") {
		return Candidate{}, false
	}
	c := Candidate{Value: strings.TrimSuffix(f[0], ".service")}
	if len(f) >= 4 {
		c.Detail = f[3]
	}
	return c, true
}

// parseUserUnit reads the same output as [parseUnit] for a user manager,
// tagging the candidate so it reaches journalctl with --user.
func parseUserUnit(line string) (Candidate, bool) {
	c, ok := parseUnit(line)
	if !ok {
		return c, false
	}
	c.Value = source.UserUnitPrefix + c.Value
	c.Detail = strings.TrimSuffix("user · "+c.Detail, " · ")
	return c, true
}

// parsePod reads "namespace name phase" and emits the compact "ns/pod" target.
func parsePod(line string) (Candidate, bool) {
	f := strings.Fields(line)
	if len(f) < 2 {
		return Candidate{}, false
	}
	c := Candidate{Value: f[0] + "/" + f[1]}
	if len(f) >= 3 {
		c.Detail = f[2]
	}
	return c, true
}

// parseContainer reads the tab-separated docker ps format.
func parseContainer(line string) (Candidate, bool) {
	f := strings.Split(line, "\t")
	if len(f) == 0 || strings.TrimSpace(f[0]) == "" {
		return Candidate{}, false
	}
	c := Candidate{Value: strings.TrimSpace(f[0])}
	if len(f) >= 3 {
		c.Detail = strings.TrimSpace(f[2]) + " · " + strings.TrimSpace(f[1])
	}
	return c, true
}
