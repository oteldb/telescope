package config

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/source"
)

// grafanaProxyPath is where Grafana proxies a datasource's own API, so a place
// can name a Grafana and a datasource rather than the full URL.
const grafanaProxyPath = "/api/datasources/proxy/uid/"

// localVia is what a place says when it is read from this machine, which is
// also what saying nothing means.
const localVia = "local"

// Place is somewhere logs can be read from: what speaks there, how to get to
// it, and what it takes to be let in.
//
// What to read is separate: a place may name it, and then it opens as it
// stands, or leave it out and be asked at the prompt. A [Group] can only name
// places that need no asking, since it opens all of them at once.
//
// The token is named, not written: telescope reads it from the environment, a
// file or a command, so the config stays shareable and the secret keeps
// whatever permissions it already has. See [Token].
type Place struct {
	Name string `yaml:"name"`
	// Type is what speaks here: journalctl, kubectl, docker, command,
	// victorialogs or loki.
	Type string `yaml:"type"`
	// Via is how the place is reached: "local", or "ssh://[user@]host" to run
	// the collector over ssh. A place read over HTTP is dialed rather than
	// entered, so it says Proxy instead.
	Via string `yaml:"via,omitempty"`
	// Sudo runs the collector under sudo -n.
	Sudo bool `yaml:"sudo,omitempty"`

	Unit       string `yaml:"unit,omitempty"`
	UserUnit   bool   `yaml:"user_unit,omitempty"`
	Namespace  string `yaml:"namespace,omitempty"`
	Target     string `yaml:"target,omitempty"`
	Container  string `yaml:"container,omitempty"`
	Args       string `yaml:"args,omitempty"`
	KubeConfig string `yaml:"kubeconfig,omitempty"`
	Context    string `yaml:"context,omitempty"`

	URL string `yaml:"url,omitempty"`
	// Datasource is a Grafana datasource uid. When set, URL is the Grafana
	// itself and the datasource proxy path is appended to it.
	Datasource string `yaml:"datasource,omitempty"`
	// Token says where the bearer token is read from: an environment variable,
	// a file, or a command such as a keyring lookup.
	Token Token `yaml:"token,omitempty"`
	// Tenant selects one tenant of a multi-tenant database,
	// "AccountID:ProjectID" for VictoriaLogs.
	Tenant string            `yaml:"tenant,omitempty"`
	Header map[string]string `yaml:"headers,omitempty"`
	// Proxy reaches this place through a proxy of its own:
	// "http://proxy.corp:3128" or "socks5h://127.0.0.1:1080". Unset takes the
	// proxy from the environment.
	Proxy string `yaml:"proxy,omitempty"`
	// Insecure skips TLS verification, for a place behind a private CA.
	Insecure bool `yaml:"insecure,omitempty"`

	// Traces is where this place's traces are read from, and which API answers
	// there. It is separate from URL because logs and traces are rarely the same
	// server even when they are the same system, and a place is named after the
	// system.
	//
	// It is not a `type`: nothing streams from it and it cannot be opened on
	// its own, so it is a property of a place rather than another kind of one.
	Traces TraceStore `yaml:"traces,omitempty"`
	// Range bounds the window read, as typed at the prompt: "1h", "today",
	// "6h..1h". It is resolved when the place is opened, so a relative one means
	// the same thing every run.
	Range string `yaml:"range,omitempty"`
	// Tail and Follow are pointers so that "tail: 0" and "follow: false" are
	// distinguishable from being unset.
	Tail   *int  `yaml:"tail,omitempty"`
	Follow *bool `yaml:"follow,omitempty"`
	// Query pre-fills the filter.
	Query string `yaml:"query,omitempty"`

	// resolved is the endpoint this place queries, filled in by [Load], and
	// resolveErr is why its token could not be read. The reason is kept rather
	// than returned so one unreadable token does not take the whole config down.
	resolved   source.Endpoint
	resolveErr error
}

// typeNames are the types a place may declare, for the message that says so
// when it declares something else.
var typeNames = []string{
	string(source.CollectorJournal),
	string(source.CollectorKubectl),
	string(source.CollectorDocker),
	string(source.CollectorCommand),
	string(source.CollectorVictoriaLogs),
	string(source.CollectorLoki),
}

// Collector is what speaks at this place.
func (p Place) Collector() source.Collector { return source.Collector(strings.TrimSpace(p.Type)) }

// Validate reports whether the place is usable as declared. A place that names
// no target is valid: it pre-fills the prompt rather than opening straight away.
//
// Whether its token can be read is left to [Place.Resolve]: a config naming a
// place the user has no token for should still open every other one.
func (p Place) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	p.resolveErr = nil
	_, _, err := p.Stream()
	return err
}

// Stream converts a declared place into a stream config.
//
// Ready reports whether it names everything needed to open. When it does not,
// the config is still returned so the prompt can start from it, which is how a
// place that pins a cluster and a namespace but leaves the pod open behaves.
func (p Place) Stream() (cfg source.Config, ready bool, err error) {
	collector := p.Collector()
	if collector == "" {
		return source.Config{}, false, errors.Errorf(
			"type is required: one of %s", strings.Join(typeNames, ", "))
	}
	if !slices.Contains(typeNames, string(collector)) {
		return source.Config{}, false, errors.Errorf(
			"unknown type %q: want one of %s", p.Type, strings.Join(typeNames, ", "))
	}

	traces, _, traceErr := p.TraceEndpoint()
	if traceErr != nil {
		return source.Config{}, false, traceErr
	}
	cfg = source.Config{
		Name:        p.Name,
		Traces:      traces,
		Collector:   collector,
		Unit:        p.Unit,
		UserUnit:    p.UserUnit,
		Namespace:   p.Namespace,
		Target:      p.Target,
		Container:   p.Container,
		Args:        p.Args,
		KubeConfig:  p.KubeConfig,
		KubeContext: p.Context,
		Elevate:     p.Sudo,
		Tail:        defaultTail,
		Follow:      defaultFollow,
	}
	if p.Tail != nil {
		cfg.Tail = *p.Tail
	}
	if p.Follow != nil {
		cfg.Follow = *p.Follow
	}
	if cfg.Range, err = source.ParseRange(p.Range, time.Now()); err != nil {
		return source.Config{}, false, err
	}

	if collector.IsRemoteAPI() {
		if err := p.validateHTTP(); err != nil {
			return source.Config{}, false, err
		}
		cfg.Endpoint = p.resolved
		if p.resolveErr != nil {
			return cfg, false, p.resolveErr
		}
	} else {
		if err := p.validateExec(); err != nil {
			return source.Config{}, false, err
		}
		if cfg.Transport, cfg.Host, err = parseVia(p.Via); err != nil {
			return source.Config{}, false, err
		}
	}

	// A journal unit may carry the compact user/ prefix, as in the prompt.
	if collector == source.CollectorJournal && cfg.Unit != "" {
		cfg.Unit, cfg.UserUnit = parseUnit(cfg.Unit, cfg.UserUnit)
	}
	// The kubectl target may carry the compact ns/pod:container form.
	if collector == source.CollectorKubectl && cfg.Namespace == "" && cfg.Container == "" {
		if ns, target, container := source.ParseKubeTarget(cfg.Target); ns != "" || container != "" {
			cfg.Namespace, cfg.Target, cfg.Container = ns, target, container
		}
	}
	// Anything still missing is something the prompt can ask for.
	return cfg, cfg.Validate() == nil, nil
}

// validateHTTP rejects what a place read over HTTP cannot mean.
func (p Place) validateHTTP() error {
	if strings.TrimSpace(p.URL) == "" {
		return errors.Errorf("%s requires a url", p.Type)
	}
	// LogQL was written here once. It is compiled from the filter now, so a
	// place that still names one is told where the filter lives rather than
	// opened on a query nothing reads.
	if p.Collector() == source.CollectorLoki && strings.TrimSpace(p.Target) != "" {
		return errors.New(
			"loki has no query of its own: filter by a label instead, as in query: app=api")
	}
	if strings.TrimSpace(p.Via) != "" {
		return errors.Errorf(
			"%s is reached over HTTP, not over %q: name a proxy instead", p.Type, p.Via)
	}
	if err := p.Token.Validate(); err != nil {
		return err
	}
	return (source.Endpoint{Proxy: p.Proxy}).ProxyError()
}

// validateExec rejects the ways in that only a log database has.
func (p Place) validateExec() error {
	for _, named := range []struct {
		field string
		set   bool
	}{
		{"url", strings.TrimSpace(p.URL) != ""},
		{"datasource", strings.TrimSpace(p.Datasource) != ""},
		{"token", !p.Token.IsZero()},
		{"tenant", strings.TrimSpace(p.Tenant) != ""},
		{"headers", len(p.Header) > 0},
		{"proxy", strings.TrimSpace(p.Proxy) != ""},
		{"insecure", p.Insecure},
	} {
		if named.set {
			return errors.Errorf("%s is a command, not an HTTP endpoint: %s means nothing to it",
				p.Type, named.field)
		}
	}
	return nil
}

// Endpoint returns the endpoint this place queries, and why its token could not
// be read.
//
// The endpoint is returned even then: what it is and which API it speaks is in
// the file, and only the secret is missing. A caller that needs the token
// checks the error; one that only needs to know what was declared does not.
func (p Place) Endpoint() (source.Endpoint, error) {
	out := source.Endpoint{
		Name:      p.Name,
		Collector: p.Collector(),
		URL:       strings.TrimRight(strings.TrimSpace(p.URL), "/"),
		Tenant:    p.Tenant,
		Header:    p.Header,
		Proxy:     strings.TrimSpace(p.Proxy),
		Insecure:  p.Insecure,
	}
	if ds := strings.TrimSpace(p.Datasource); ds != "" {
		out.URL += grafanaProxyPath + ds
	}

	token, err := p.Token.Read(context.Background())
	if err != nil {
		return out, errors.Wrapf(err, "place %q", p.Name)
	}
	out.Token = token
	return out, nil
}

// TraceEndpoint returns where this place's traces are read from, and whether it
// says.
//
// It is the place's own endpoint with another URL: a system's traces sit behind
// the same token, tenant and proxy as its logs often enough that asking for
// them twice would be a way to get one of them wrong. A place that reaches its
// logs by running a command has none of those to lend, and the trace endpoint
// is then the URL alone.
func (p Place) TraceEndpoint() (source.Endpoint, bool, error) {
	if p.Traces.IsZero() {
		return source.Endpoint{}, false, nil
	}
	if err := p.Traces.Validate(); err != nil {
		return source.Endpoint{}, false, err
	}
	out, err := p.Endpoint()
	out.URL = strings.TrimRight(strings.TrimSpace(p.Traces.URL), "/")
	// What speaks at the log endpoint says nothing about this one: a place whose
	// logs are in VictoriaLogs may keep its traces in either store, or in
	// neither's neighbor.
	out.Collector = p.Traces.Collector()
	return out, true, err
}

// parseVia reads how a place is reached. Saying nothing is saying local, which
// is where most logs are read from.
func parseVia(via string) (source.Transport, string, error) {
	via = strings.TrimSpace(via)
	switch {
	case via == "", via == localVia:
		return source.TransportLocal, "", nil
	case strings.HasPrefix(via, "ssh://"):
		host := strings.TrimSpace(strings.TrimPrefix(via, "ssh://"))
		if host == "" {
			return "", "", errors.New("via ssh:// needs a host, as in ssh://ops@node-1")
		}
		return source.TransportSSH, host, nil
	default:
		return "", "", errors.Errorf(
			"unknown via %q: want %s or ssh://[user@]host", via, localVia)
	}
}

func parseUnit(unit string, user bool) (string, bool) {
	name, isUser := source.ParseJournalTarget(unit)
	return name, user || isUser
}
