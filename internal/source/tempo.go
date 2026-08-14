package source

import (
	"context"
	"io"
	"net/url"
	"strings"

	"github.com/go-faster/errors"
)

// maxTraceBody bounds what a trace fetch will read. A trace is a request and
// not a stream, so it has an end, but nothing in the protocol says how far away
// it is, and a viewer that read until it ran out of memory would be a way to be
// attacked by a database.
const maxTraceBody = 64 << 20

// Trace reads one trace by id, as the bytes the endpoint answered with.
//
// What those bytes mean is not decided here. `source` does not know what a
// trace is, for the same reason it dates a line through [WithTimeFunc] rather
// than importing the parser: what a stream is made of belongs above it.
// `trace.DecodeOTLP` is what reads the result, and it takes either encoding.
//
// The v2 path is asked first because it answers in JSON, which says what went
// wrong when something does; v1 answers in protobuf, and a proxy's HTML error
// page decoded as protobuf is a worse message than the one it replaced. Both
// carry the same payload, so falling back costs a round trip and nothing else.
//
// Which of the two APIs is asked is the store's own declaration, not something
// worked out from what came back. A Jaeger store serves one trace on the same
// path Tempo's older one does and answers with a different document, so there
// is nothing in the path to tell them apart and a fallback would be a guess.
func (e Endpoint) Trace(ctx context.Context, id string) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("no trace id")
	}
	// The id goes into a path, so it may not be something that decides which
	// path it is.
	if strings.ContainsAny(id, "/?#%") {
		return nil, errors.Errorf("trace id %q is not an id", id)
	}

	if e.traceAPI() == CollectorJaeger {
		return e.fetchTrace(ctx, jaegerSearchPath+"/"+id, acceptJSON)
	}

	data, err := e.fetchTrace(ctx, "/api/v2/traces/"+id, acceptJSON)
	if err == nil {
		return data, nil
	}
	// Only a refusal is worth asking a second way. A connection that could not
	// be made will not be made by another path either, and reporting the first
	// failure is more use than reporting the same one twice.
	var refused *apiError
	if !errors.As(err, &refused) {
		return nil, err
	}
	data, fallbackErr := e.fetchTrace(ctx, "/api/traces/"+id, acceptProto)
	if fallbackErr != nil {
		// The endpoint that answered is the one whose complaint is worth
		// keeping: a 404 from v2 on a server that has no v2 says nothing.
		return nil, fallbackErr
	}
	return data, nil
}

// What each path is asked for. They are asked separately and never together:
// a server was found that answers 200 to exactly "application/json;
// charset=utf-8", and 400 to the bare form and to any list of media ranges. So
// an Accept naming both formats — the obvious way to write this, and the one
// that lets the server choose — is refused by the endpoint most likely to be
// on the other end, and every fetch quietly took the fallback.
const (
	acceptJSON  = "application/json; charset=utf-8"
	acceptProto = "application/protobuf"
)

func (e Endpoint) fetchTrace(ctx context.Context, path, accept string) ([]byte, error) {
	req, err := e.request(ctx, path, url.Values{})
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	e.setTenant(req, e.traceTenantHeaders()...)

	resp, err := httpClient(e).Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "fetch trace")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
	}()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTraceBody))
	if err != nil {
		return nil, errors.Wrap(err, "read trace")
	}
	return data, nil
}
