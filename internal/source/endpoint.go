package source

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-faster/errors"
)

// Endpoint is a log API queried over HTTP rather than run as a command.
//
// Its URL is whatever base the API paths hang off, which is a Grafana
// datasource proxy as often as it is the database itself:
//
//	https://logs.example.com
//	https://grafana.example.com/api/datasources/proxy/uid/abc123
//
// The token is resolved when the config is read and lives in memory only: it is
// never written to history, shown in a title, or passed to a child process.
type Endpoint struct {
	// Name is what the user called the endpoint, used in titles.
	Name string
	URL  string
	// Collector is the API the endpoint speaks. A Loki and a VictoriaLogs
	// endpoint are not interchangeable: the paths, the query language and the
	// tenancy header all differ.
	Collector Collector
	// Token is sent as a bearer token, which is what a Grafana service account
	// issues.
	Token string
	// Tenant selects one tenant of a multi-tenant database, spelled the way that
	// database spells it: "AccountID:ProjectID" for VictoriaLogs.
	Tenant string
	// Header carries anything else the endpoint or the proxy in front of it
	// needs.
	Header map[string]string
	// Proxy reaches this endpoint through a proxy of its own, as
	// "http://proxy:3128" or "socks5h://127.0.0.1:1080". Empty takes the proxy
	// from the environment, which is what every other tool does.
	Proxy string
	// Insecure skips TLS verification, for an endpoint behind a private CA.
	Insecure bool
}

// Label names the endpoint in a title, falling back to its host so an endpoint
// declared without a name is still recognizable.
func (e Endpoint) Label() string {
	if n := strings.TrimSpace(e.Name); n != "" {
		return n
	}
	if u, err := url.Parse(e.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimSpace(e.URL)
}

// api resolves an API path against the endpoint, with query parameters.
func (e Endpoint) api(path string, params url.Values) (string, error) {
	u, err := url.Parse(strings.TrimSpace(e.URL))
	if err != nil {
		return "", errors.Wrap(err, "parse endpoint url")
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.Errorf("endpoint url %q needs a scheme and a host", e.URL)
	}
	u = u.JoinPath(path)
	u.RawQuery = params.Encode()
	return u.String(), nil
}

// request builds a GET for an API path. Live tailing runs for as long as the
// view is open, so the deadline belongs to ctx and never to the request.
func (e Endpoint) request(ctx context.Context, path string, params url.Values) (*http.Request, error) {
	u, err := e.api(path, params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errors.Wrap(err, "build request")
	}
	for k, v := range e.Header {
		req.Header.Set(k, v)
	}
	if t := strings.TrimSpace(e.Token); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return req, nil
}

// setTenant applies the endpoint's tenant under the header names the database
// uses for it, as in "AccountID:ProjectID". A tenant with no second half names
// only the first header.
func (e Endpoint) setTenant(req *http.Request, names ...string) {
	tenant := strings.TrimSpace(e.Tenant)
	if tenant == "" {
		return
	}
	for i, part := range strings.SplitN(tenant, ":", len(names)) {
		if part = strings.TrimSpace(part); part != "" {
			req.Header.Set(names[i], part)
		}
	}
}

// httpClient dials the endpoint. It has no overall timeout because a followed
// stream is meant to stay open; the timeouts that remain bound the parts of a
// request that should never block.
//
// The proxy is the endpoint's own when it names one, and the environment's
// otherwise: one database behind a corporate proxy should not force every other
// request through it.
func httpClient(e Endpoint) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = e.proxy()
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = 30 * time.Second
	if e.Insecure {
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
		tr.TLSClientConfig.InsecureSkipVerify = true
	}
	return &http.Client{Transport: tr}
}

// proxy resolves how the endpoint is reached. A proxy that does not parse is
// reported when the endpoint is used, since a request is where a caller can be
// told about it; an unparseable one must never silently fall back to a direct
// connection the user did not ask for.
//
// net/http dials socks5 and socks5h itself, so no scheme needs special care
// here.
func (e Endpoint) proxy() func(*http.Request) (*url.URL, error) {
	raw := strings.TrimSpace(e.Proxy)
	if raw == "" {
		return http.ProxyFromEnvironment
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return func(*http.Request) (*url.URL, error) {
			return nil, errors.Errorf("endpoint %q: cannot use proxy %q", e.Label(), raw)
		}
	}
	return http.ProxyURL(u)
}

// ProxyError reports whether the endpoint's proxy can be used at all, so a
// mistyped one is caught where it is declared.
func (e Endpoint) ProxyError() error {
	if strings.TrimSpace(e.Proxy) == "" {
		return nil
	}
	_, err := e.proxy()(&http.Request{})
	return err
}

// maxErrorBody bounds how much of a failed response is quoted back.
const maxErrorBody = 4 * 1024

// checkResponse turns a non-2xx response into an error carrying what the server
// said. A proxy in front of the database answers in its own format, so the body
// is quoted rather than parsed.
func checkResponse(resp *http.Response) error {
	if resp.StatusCode/100 == 2 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return errors.New(resp.Status)
	}
	return errors.Errorf("%s: %s", resp.Status, oneLine(msg))
}

// oneLine collapses a multi-line error body so it fits the status bar.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
