package config

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/figureout"

	"github.com/oteldb/telescope/internal/source"
)

// grafanaProxyPath is where Grafana proxies a datasource's own API, so a place
// can name a Grafana and a datasource rather than the full URL.
const grafanaProxyPath = "/api/datasources/proxy/uid/"

// DatasourceURL is where a Grafana proxies the datasource with this uid. A
// place says the two separately, but anything that only has a URL to hand — a
// trace store, which has no datasource key of its own — builds it here rather
// than spelling the path out a second time.
func DatasourceURL(grafana, uid string) string {
	return strings.TrimRight(strings.TrimSpace(grafana), "/") + grafanaProxyPath + strings.TrimSpace(uid)
}

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
	Name string
	// Type is what speaks here: journalctl, kubectl, docker, command,
	// victorialogs or loki.
	Type string
	// Via is how the place is reached: "local", or "ssh://[user@]host" to run
	// the collector over ssh. A place read over HTTP is dialed rather than
	// entered, so it says Proxy instead.
	Via string
	// Sudo runs the collector under sudo -n.
	Sudo bool

	Unit       string
	UserUnit   bool
	Namespace  string
	Target     string
	Container  string
	Args       string
	KubeConfig string
	Context    string

	URL string
	// Datasource is a Grafana datasource uid. When set, URL is the Grafana
	// itself and the datasource proxy path is appended to it.
	Datasource string
	// Token says where the bearer token is read from: an environment variable,
	// a file, or a command such as a keyring lookup.
	Token Token
	// Tenant selects one tenant of a multi-tenant database,
	// "AccountID:ProjectID" for VictoriaLogs.
	Tenant string
	Header map[string]string
	// Proxy reaches this place through a proxy of its own:
	// "http://proxy.corp:3128" or "socks5h://127.0.0.1:1080". Unset takes the
	// proxy from the environment.
	Proxy string
	// Insecure skips TLS verification, for a place behind a private CA.
	Insecure bool

	// Traces is where this place's traces are read from, and which API answers
	// there. It is separate from URL because logs and traces are rarely the same
	// server even when they are the same system, and a place is named after the
	// system.
	//
	// It is not a `type`: nothing streams from it and it cannot be opened on
	// its own, so it is a property of a place rather than another kind of one.
	Traces TraceStore
	// Range bounds the window read, as typed at the prompt: "1h", "today",
	// "6h..1h". It is resolved when the place is opened, so a relative one means
	// the same thing every run.
	Range string
	// Tail and Follow carry their presence so that "tail: 0" and
	// "follow: false" are distinguishable from being unset. The defaults are
	// applied by [Place.Stream] rather than by the descriptor, since a place
	// built by [New] never passed through one.
	Tail   figureout.OptionalOf[int]
	Follow figureout.OptionalOf[bool]
	// Query pre-fills the filter.
	Query string

	// resolved is the endpoint this place queries, filled in by [Load], and
	// resolveErr is why its token could not be read. The reason is kept rather
	// than returned so one unreadable token does not take the whole config down.
	resolved   source.Endpoint
	resolveErr error
	// linked is the trace store this place named, and linkErr why that store's
	// token could not be read. Both are the named place's own: a store declared
	// once is reached the way it says, not the way whoever named it is.
	linked  source.Endpoint
	linkErr error
}

// placeDescriptor describes a [Place] as it is written in the file: what each
// key accepts, what it means and what it falls back to. Decoding, validation
// and the published JSON Schema all come out of it, so a key is declared once.
var placeDescriptor = figureout.MustDerive(func(p *Place, s *figureout.Schema[Place]) {
	figureout.Explicit(s, &p.Name, "name").NonEmpty().
		Doc("Shown in the picker, and how a group names it.")
	figureout.Explicit(s, &p.Type, "type").Enum(typeNames...).
		Doc("What speaks here.")
	figureout.Value(s, &p.Via, "via").DocumentDefault(localVia).
		Doc(`How the place is reached: "local", or "ssh://[user@]host".`)
	figureout.Value(s, &p.Sudo, "sudo").
		Doc("Run the collector under sudo -n.")

	figureout.Value(s, &p.Unit, "unit").
		Doc(`systemd unit, the "user/" prefix accepted.`)
	figureout.Value(s, &p.UserUnit, "user_unit").
		Doc("Read the user journal.")
	figureout.Value(s, &p.Namespace, "namespace").
		Doc("Kubernetes namespace.")
	figureout.Value(s, &p.Target, "target").
		Doc("Pod name or label selector, ns/pod:container accepted; " +
			"the LogsQL query for VictoriaLogs.")
	figureout.Value(s, &p.Container, "container").
		Doc("Container, for kubectl or docker.")
	figureout.Value(s, &p.Args, "args").
		Doc("Command line, for type: command.")
	figureout.Value(s, &p.KubeConfig, "kubeconfig").
		Doc("Passed to kubectl as --kubeconfig.")
	figureout.Value(s, &p.Context, "context").
		Doc("Passed to kubectl as --context.")

	figureout.Value(s, &p.URL, "url").
		Doc("The base the database's API paths hang off. Required for victorialogs and loki.")
	figureout.Value(s, &p.Datasource, "datasource").
		Doc("Grafana datasource uid, appended to url as a proxy path.")
	figureout.Object(s, &p.Token, "token", tokenDescriptor).
		Doc("Where the bearer token is read from. It is named rather than written, " +
			"so the file stays shareable.")
	figureout.Value(s, &p.Tenant, "tenant").
		Doc("AccountID:ProjectID for VictoriaLogs, the org id for Loki.")
	// Secret, because this is where a credential the token cannot express ends
	// up: an Authorization header a proxy wants written out, a signed cookie.
	figureout.Value(s, &p.Header, "headers", figureout.Secret()).
		Doc("Anything else the database or its proxy needs.")
	figureout.Value(s, &p.Proxy, "proxy").
		Doc("Reach this database through http://… or socks5h://…. " +
			"Unset takes the proxy from the environment.")
	figureout.Value(s, &p.Insecure, "insecure").
		Doc("Skip TLS verification, for a place behind a private CA.")

	figureout.ScalarOr(s, &p.Traces, "traces", traceStoreDescriptor, traceScalar).
		Doc("Where this place's traces are read from: the name of a place that reads " +
			"them, or a url of one, which on its own is a Tempo.")
	figureout.Value(s, &p.Range, "range").
		Doc("The window read: 1h, today, 6h..1h.")
	figureout.Optional(s, &p.Tail, "tail").AtLeast(0).DocumentDefault(defaultTail).
		Doc("Lines of history to open with, 0 for all; " +
			"over a database it is also the size of a page.")
	figureout.Optional(s, &p.Follow, "follow").DocumentDefault(defaultFollow).
		Doc("Keep streaming.")
	figureout.Value(s, &p.Query, "query").
		Doc("Pre-fills the filter, and is what selects a Loki stream.")

	figureout.IgnoreRecursive(s, &p.resolved, figureout.Reason("resolved at load"))
	figureout.Ignore(s, &p.resolveErr, figureout.Reason("resolved at load"))
	figureout.IgnoreRecursive(s, &p.linked, figureout.Reason("resolved at load"))
	figureout.Ignore(s, &p.linkErr, figureout.Reason("resolved at load"))
})

// logTypes are the types a place may declare to read logs, and traceTypeNames
// the ones it may declare to read traces. They are separate lists because a
// stream can only be opened on the first kind: what a place of the second kind
// is for is being named by the others, and by `telescope trace --from`.
var logTypes = []string{
	string(source.CollectorJournal),
	string(source.CollectorKubectl),
	string(source.CollectorDocker),
	string(source.CollectorCommand),
	string(source.CollectorVictoriaLogs),
	string(source.CollectorLoki),
}

// typeNames is every type a place may declare.
var typeNames = append(append([]string{}, logTypes...), traceTypeNames...)

// traceScalar reads what `traces:` says when it says one thing. A url is a
// store written out, and anything else is the name of a place that is one —
// the same rule `telescope trace --from` reads its argument by, since the two
// are the same question asked in two places.
func traceScalar(s string) TraceStore {
	if isURL(s) {
		return TraceStore{URL: s}
	}
	return TraceStore{Name: s}
}

// isURL reports whether a bound was written as an address rather than a name.
func isURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// ReadsTraces reports whether the place is a trace store rather than a stream
// of lines.
func (p Place) ReadsTraces() bool { return p.Collector().IsTraceAPI() }

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
	if p.ReadsTraces() {
		return p.validateTraceStore()
	}
	p.resolveErr = nil
	_, _, err := p.Stream()
	return err
}

// validateTraceStore rejects what a place that reads traces cannot mean.
//
// It is checked here rather than through [Place.Stream] because there is no
// stream to build: nothing is tailed from a trace store, and what would be
// wrong with it is what it says about reading lines it does not read.
func (p Place) validateTraceStore() error {
	if strings.TrimSpace(p.URL) == "" {
		return errors.Errorf("%s requires a url", p.Type)
	}
	if !p.Traces.IsZero() {
		return errors.New("traces reads traces already: it is what other places name")
	}
	for _, named := range []struct {
		field string
		set   bool
	}{
		{"unit", strings.TrimSpace(p.Unit) != ""},
		{"namespace", strings.TrimSpace(p.Namespace) != ""},
		{"target", strings.TrimSpace(p.Target) != ""},
		{"container", strings.TrimSpace(p.Container) != ""},
		{"args", strings.TrimSpace(p.Args) != ""},
		{"via", strings.TrimSpace(p.Via) != ""},
		{"query", strings.TrimSpace(p.Query) != ""},
	} {
		if named.set {
			return errors.Errorf("%s reads traces, not lines: %s means nothing to it",
				p.Type, named.field)
		}
	}
	if err := p.Token.Validate(); err != nil {
		return err
	}
	return (source.Endpoint{Proxy: p.Proxy}).ProxyError()
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
	if collector.IsTraceAPI() {
		return source.Config{}, false, errors.Errorf(
			"%s reads traces, not logs: name it from a place whose lines carry trace ids, "+
				"or open it with telescope trace --from %s", p.Type, p.Name)
	}
	if !slices.Contains(logTypes, string(collector)) {
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
		Tail:        p.Tail.OrElse(defaultTail),
		Follow:      p.Follow.OrElse(defaultFollow),
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
		out.URL = DatasourceURL(out.URL, ds)
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
//
// The token is the one already read for the logs endpoint rather than a second
// read of its own: a keyring is unlocked once per run, and reading it here too
// would prompt twice for a place that declares both.
func (p Place) TraceEndpoint() (source.Endpoint, bool, error) {
	if p.ReadsTraces() {
		// A place that reads traces is the store, and reads it through its own
		// door rather than through anybody's.
		return p.resolved, true, p.resolveErr
	}
	if p.Traces.IsZero() {
		return source.Endpoint{}, false, nil
	}
	if err := p.Traces.Validate(); err != nil {
		return source.Endpoint{}, false, err
	}
	if p.Traces.Links() {
		// Resolved when the file was read, since the store is another entry of
		// it and a place cannot see its neighbors.
		return p.linked, true, p.linkErr
	}
	return source.Endpoint{
		Name: p.Name,
		// What speaks at the log endpoint says nothing about this one: a place
		// whose logs are in VictoriaLogs may keep its traces in either store,
		// or in neither's neighbor.
		Collector: p.Traces.Collector(),
		URL:       strings.TrimRight(strings.TrimSpace(p.Traces.URL), "/"),
		Token:     p.resolved.Token,
		Tenant:    p.Tenant,
		Header:    p.Header,
		Proxy:     strings.TrimSpace(p.Proxy),
		Insecure:  p.Insecure,
	}, true, nil
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
