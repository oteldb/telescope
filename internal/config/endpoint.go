package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/source"
)

// grafanaProxyPath is where Grafana proxies a datasource's own API, so an
// endpoint can name a Grafana and a datasource rather than the full URL.
const grafanaProxyPath = "/api/datasources/proxy/uid/"

// Endpoint is a log API declared in the config file.
//
// The token is named, not written: telescope reads it from the environment or
// from a file, so the config stays shareable and the secret keeps whatever
// permissions it already has.
type Endpoint struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	// Type is the API the endpoint speaks: "victorialogs" or "loki".
	Type string `yaml:"type"`
	// Datasource is a Grafana datasource uid. When set, URL is the Grafana
	// itself and the datasource proxy path is appended to it.
	Datasource string `yaml:"datasource,omitempty"`

	// TokenEnv names an environment variable holding a bearer token, which is
	// what a Grafana service account issues.
	TokenEnv string `yaml:"token_env,omitempty"`
	// TokenFile names a file holding one, for a secret that never enters the
	// environment.
	TokenFile string `yaml:"token_file,omitempty"`

	// Tenant selects one tenant of a multi-tenant database, "AccountID:ProjectID"
	// for VictoriaLogs.
	Tenant string            `yaml:"tenant,omitempty"`
	Header map[string]string `yaml:"headers,omitempty"`
	// Insecure skips TLS verification, for an endpoint behind a private CA.
	Insecure bool `yaml:"insecure,omitempty"`
}

// Resolved returns the endpoints that can be queried, and why the others
// cannot. An endpoint whose token is unreadable is left out rather than offered
// as a choice that fails on use, and the reason is reported instead.
func (c Config) Resolved() ([]source.Endpoint, error) {
	var (
		out  []source.Endpoint
		errs []error
	)
	for _, e := range c.Endpoints {
		resolved, err := e.Resolve()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, resolved)
	}
	return out, errors.Join(errs...)
}

// Validate reports whether the endpoint is structurally usable. Whether its
// token can be read is left to [Endpoint.Resolve]: a config naming an endpoint
// the user has no token for should still open every other source.
func (e Endpoint) Validate() error {
	if strings.TrimSpace(e.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(e.URL) == "" {
		return errors.New("url is required")
	}
	if !source.Collector(e.Type).IsRemoteAPI() {
		return errors.Errorf("type must be victorialogs or loki, not %q", e.Type)
	}
	if e.TokenEnv != "" && e.TokenFile != "" {
		return errors.New("token_env and token_file are mutually exclusive")
	}
	return nil
}

// Resolve reads the token and returns the endpoint to query.
//
// The endpoint is returned even when the token cannot be read: what it is and
// which API it speaks is in the file, and only the secret is missing. A caller
// that needs the token checks the error; one that only needs to know what was
// declared does not have to.
func (e Endpoint) Resolve() (source.Endpoint, error) {
	out := source.Endpoint{
		Name:      e.Name,
		Collector: source.Collector(e.Type),
		URL:       strings.TrimRight(strings.TrimSpace(e.URL), "/"),
		Tenant:    e.Tenant,
		Header:    e.Header,
		Insecure:  e.Insecure,
	}
	if ds := strings.TrimSpace(e.Datasource); ds != "" {
		out.URL += grafanaProxyPath + ds
	}

	switch {
	case e.TokenEnv != "":
		token := strings.TrimSpace(os.Getenv(e.TokenEnv))
		if token == "" {
			return out, errors.Errorf("endpoint %q: $%s is not set", e.Name, e.TokenEnv)
		}
		out.Token = token
	case e.TokenFile != "":
		path, err := expandHome(e.TokenFile)
		if err != nil {
			return out, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return out, errors.Wrapf(err, "endpoint %q: read token", e.Name)
		}
		out.Token = strings.TrimSpace(string(data))
	}
	return out, nil
}

// expandHome resolves a leading ~, which is what a path in a config file is
// most likely to be written with.
func expandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrap(err, "home directory")
	}
	return filepath.Join(home, rest), nil
}
