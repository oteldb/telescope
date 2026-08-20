package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// datasourcesPath is where Grafana lists what it can query.
const datasourcesPath = "/api/datasources"

// Grafana says where an import reads datasources from. Both sources may be
// named: an operator has the provisioning files, an ordinary user has an API
// token, and a machine that has both is not a contradiction.
type Grafana struct {
	// URL is a Grafana whose API is asked.
	URL string
	// Token is where the credential for that API is read from — and, since the
	// places written for it are queried through the same Grafana, what each one
	// will say in turn. The secret is named, never copied into the file.
	Token config.Token
	// Provisioning is a provisioning file, or a directory of them, read from
	// disk instead of over the API.
	Provisioning string
}

// datasource is one entry of either source. The API answers in JSON and a
// provisioning file is written in YAML, but they describe the same thing with
// the same names, so one struct reads both.
type datasource struct {
	Name string `json:"name" yaml:"name"`
	Type string `json:"type" yaml:"type"`
	UID  string `json:"uid"  yaml:"uid"`
	URL  string `json:"url"  yaml:"url"`
	// BasicAuth says the datasource has a credential of its own. Whatever it is
	// stays where it is: a provisioning file holds the password in the clear and
	// copying it here would put it somewhere with weaker permissions.
	BasicAuth bool `json:"basicAuth" yaml:"basicAuth"`
}

// logKinds and traceKinds are the Grafana plugin ids telescope can read, and
// what it speaks at each. A datasource of any other kind is not a mistake,
// which is why an import passes over it without a word.
var (
	logKinds = map[string]source.Collector{
		"loki":                            source.CollectorLoki,
		"victoriametrics-logs-datasource": source.CollectorVictoriaLogs,
		"victorialogs-datasource":         source.CollectorVictoriaLogs,
	}
	traceKinds = map[string]source.Collector{
		"tempo":                     source.CollectorTempo,
		"jaeger":                    source.CollectorJaeger,
		"victoriatraces-datasource": source.CollectorJaeger,
	}
)

// offers reads the datasources and turns the ones telescope can read into
// places, alongside whatever it has to say about the ones it could not.
func (g Grafana) offers(ctx context.Context, client *http.Client) ([]Offer, []string, error) {
	var (
		out   []Offer
		notes []string
	)
	if path := strings.TrimSpace(g.Provisioning); path != "" {
		found, err := readProvisioning(path)
		if err != nil {
			return nil, nil, err
		}
		places, said := g.build(found, false)
		out, notes = append(out, places...), append(notes, said...)
	}
	if strings.TrimSpace(g.URL) != "" {
		found, err := g.query(ctx, client)
		if err != nil {
			return out, notes, err
		}
		places, said := g.build(found, true)
		out, notes = append(out, places...), append(notes, said...)
	}
	return out, notes, nil
}

// query asks the Grafana API what it can reach.
func (g Grafana) query(ctx context.Context, client *http.Client) ([]datasource, error) {
	token, err := g.Token.Read(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(strings.TrimSpace(g.URL), "/")+datasourcesPath, http.NoBody)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		// Listing datasources is an admin's right in Grafana, so a token that
		// works everywhere else answers 403 here; saying which status came back
		// is the difference between fixing the token and doubting the URL.
		return nil, errors.Errorf("%s answered %s", req.URL, res.Status)
	}
	var out []datasource
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, errors.Wrap(err, "read datasources")
	}
	return out, nil
}

// readProvisioning reads a provisioning file, or every file in a directory of
// them, which is how Grafana itself is configured.
func readProvisioning(path string) ([]datasource, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	files := []string{path}
	if info.IsDir() {
		if files, err = filepath.Glob(filepath.Join(path, "*.y*ml")); err != nil {
			return nil, err
		}
		slices.Sort(files)
	}

	var out []datasource
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Datasources []datasource `yaml:"datasources"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, errors.Wrapf(err, "parse %s", f)
		}
		out = append(out, doc.Datasources...)
	}
	return out, nil
}

// build turns datasources into places. Over the API the place names the Grafana
// and the datasource, so the query goes through the proxy the token already
// opens; off disk there is no token to lend and the datasource's own url is
// what telescope was given.
func (g Grafana) build(found []datasource, viaAPI bool) ([]Offer, []string) {
	var (
		out    []Offer
		notes  []string
		stores []string
	)
	for _, d := range found {
		if kind, ok := traceKinds[d.Type]; ok {
			store := config.Place{
				Name: d.Name, Type: string(kind), URL: g.datasourceURL(d, viaAPI),
			}
			if store.URL == "" {
				notes = append(notes, "grafana: "+d.Name+" names no url")
				continue
			}
			if viaAPI {
				store.URL, store.Datasource, store.Token = g.URL, d.UID, g.Token
			}
			out = append(out, Offer{Place: store, Note: "grafana datasource"})
			stores = append(stores, store.Name)
			continue
		}
		kind, ok := logKinds[d.Type]
		if !ok {
			continue
		}
		place := config.Place{Name: d.Name, Type: string(kind), URL: g.datasourceURL(d, viaAPI)}
		if place.URL == "" {
			notes = append(notes, "grafana: "+d.Name+" names no url")
			continue
		}
		if viaAPI {
			place.URL, place.Datasource, place.Token = g.URL, d.UID, g.Token
		} else if d.BasicAuth {
			notes = append(notes, "grafana: "+d.Name+
				" logs in with a password of its own; give the place a token: naming where to read it")
		}
		out = append(out, Offer{Place: place, Note: "grafana datasource"})
	}
	return attach(out, stores, notes)
}

// datasourceURL is where the datasource is queried: through the Grafana that
// listed it, since the token opens that and not necessarily the store behind
// it, or at the address the provisioning file gave when there was no Grafana to
// ask.
func (g Grafana) datasourceURL(d datasource, viaAPI bool) string {
	if viaAPI {
		return config.DatasourceURL(g.URL, d.UID)
	}
	return strings.TrimSpace(d.URL)
}

// attach points the log places at the store they belong to.
//
// Every store is a place of its own now, so nothing is lost by not knowing
// which: they are all declared, all reachable by name, and `telescope trace
// --from` opens any of them. What is left to guess is only the link, and one
// store beside a handful of log datasources from the same Grafana is a system
// whose traces are that store — there is nowhere else for them to be. Several
// is a question this has no way to answer, so it says which names are there
// and leaves the linking to whoever knows.
func attach(offers []Offer, stores, notes []string) ([]Offer, []string) {
	if len(stores) != 1 {
		if len(stores) > 1 {
			notes = append(notes, "grafana: several trace stores ("+strings.Join(stores, ", ")+
				"): name the one each place reads with traces: <name>")
		}
		return offers, notes
	}
	for i := range offers {
		if offers[i].Place.ReadsTraces() {
			continue
		}
		offers[i].Place.Traces = config.TraceStore{Name: stores[0]}
	}
	return offers, notes
}

// ParseToken reads how a token is named on the command line: "env:NAME",
// "file:PATH" or "exec:COMMAND", the three the config file accepts. A bare
// secret is refused rather than accepted and written out, since the whole point
// of the config naming one is that the file stays shareable.
func ParseToken(spec string) (config.Token, error) {
	kind, value, ok := strings.Cut(strings.TrimSpace(spec), ":")
	if !ok || strings.TrimSpace(value) == "" {
		return config.Token{}, errors.Errorf(
			"token %q names no source: write env:NAME, file:PATH or exec:COMMAND", spec)
	}
	value = strings.TrimSpace(value)
	switch kind {
	case "env":
		return config.Token{Env: value}, nil
	case "file":
		return config.Token{File: value}, nil
	case "exec":
		return config.Token{Exec: config.Argv{"sh", "-c", value}}, nil
	default:
		return config.Token{}, errors.Errorf(
			"unknown token source %q: want env, file or exec", kind)
	}
}
