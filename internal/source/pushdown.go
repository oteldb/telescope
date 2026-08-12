package source

import (
	"net/http"
	"sync"

	"github.com/go-faster/errors"
)

// refusedPushdown remembers the endpoints that would not read a query telescope
// compiled for them, so the round trip that discovers it is paid once rather
// than on every filter.
//
// It is keyed by URL and not by place, because two places over one database are
// one server: what either of them learns holds for both. It lives for as long
// as the process does, which is as long as the server is the version it is.
var refusedPushdown sync.Map

// refusesPushdown reports whether e has already refused a compiled query.
func refusesPushdown(e Endpoint) bool {
	_, ok := refusedPushdown.Load(e.URL)
	return ok
}

// refusePushdown records that e will not read what was compiled for it.
func refusePushdown(e Endpoint) { refusedPushdown.Store(e.URL, struct{}{}) }

// forgetPushdown is for tests, where one address is served in turn by servers
// that do not agree about what they can parse.
func forgetPushdown(e Endpoint) { refusedPushdown.Delete(e.URL) }

// retryUnpushed answers with the query to ask instead when a server would not
// read the one it was given, and with err when there is nothing else to ask.
//
// Pushing a filter down is an optimization, so a server that rejects it has to
// cost the optimization and not the stream. LogsQL only grew the "*:filter"
// form a bare word compiles to in 2026, and an endpoint older than that is
// still an endpoint; the same holds for whatever the compilers learn next.
// What comes back is filtered by the view either way, which is why asking for
// more than was wanted is always an answer and asking wrongly is never one.
//
// Only a 400 is read this way. A server that is unreachable, unauthorized or
// merely unwell would answer a narrower query no better, and retrying would
// hide why.
func (c Config) retryUnpushed(asked, plain string, err error) (string, error) {
	var api *apiError
	if !errors.As(err, &api) || api.Code != http.StatusBadRequest || plain == asked {
		return "", err
	}
	refusePushdown(c.Endpoint)
	return plain, nil
}
