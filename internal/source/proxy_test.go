package source

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEndpointProxy: a corporate endpoint may be reachable only through a proxy
// of its own, and pointing at one must not depend on the environment every
// other request shares.
func TestEndpointProxy(t *testing.T) {
	var (
		mu  sync.Mutex
		via []string
	)
	// A proxy is asked for an absolute URI, which is how it knows where the
	// request was really going.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		via = append(via, r.Host)
		mu.Unlock()
		_, _ = w.Write([]byte(vlogsEntry("2026-08-11T10:00:01Z", "through the proxy")))
	}))
	defer proxy.Close()

	cfg := vlogsConfig("http://logs.internal.example.com", false)
	cfg.Endpoint.Proxy = proxy.URL

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	lines, err := drain(t, s)
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], "through the proxy")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"logs.internal.example.com"}, via,
		"the request went to the proxy, addressed to the endpoint")
}

// TestEndpointProxyErrors: a proxy that cannot be used is reported rather than
// quietly replaced by a direct connection the user did not ask for.
func TestEndpointProxyErrors(t *testing.T) {
	bad := vlogsConfig("https://logs.example.com", false)
	bad.Endpoint.Proxy = "://nonsense"
	require.ErrorContains(t, bad.Validate(), "cannot use proxy")

	_, err := Start(t.Context(), bad)
	require.ErrorContains(t, err, "cannot use proxy")
}

// TestEndpointProxyIsResolved: what a declared proxy resolves to, without
// asking the environment — net/http reads that once per process, so a test that
// set it would depend on which tests ran before it.
func TestEndpointProxyIsResolved(t *testing.T) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://logs.example.com", nil)
	require.NoError(t, err)

	e := Endpoint{URL: "https://logs.example.com", Proxy: "socks5h://127.0.0.1:1080"}
	require.NoError(t, e.ProxyError())
	got, err := e.proxy()(req)
	require.NoError(t, err)
	require.Equal(t, &url.URL{Scheme: "socks5h", Host: "127.0.0.1:1080"}, got)

	// Naming none is not an error: the environment answers instead.
	require.NoError(t, Endpoint{URL: e.URL}.ProxyError())
}
