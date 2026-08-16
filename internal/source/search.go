package source

import (
	"context"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/jx"
)

// Trace search paths, resolved against [Endpoint.URL].
const (
	tempoSearchPath    = "/api/search"
	tempoTagsPath      = "/api/v2/search/tags"
	tempoTagValuesPath = "/api/v2/search/tag/"
	jaegerSearchPath   = "/api/traces"
	jaegerServicesPath = "/api/services"
)

// The attributes Tempo is asked to list when the form wants to complete a
// service or an operation. They are TraceQL names rather than OpenTelemetry
// ones: `name` is the span's own, not an attribute called name.
const (
	tempoServiceTag   = "resource.service.name"
	tempoOperationTag = "name"
)

// SearchTraces asks a trace store which traces match, as the bytes it answered
// with.
//
// What those bytes mean is not decided here, for the reason [Endpoint.Trace]
// gives: `source` does not know what a trace is. Which decoder reads them
// follows from [Endpoint.Collector], since the two APIs answer with different
// documents — Tempo with a summary of each trace, Jaeger with the traces
// themselves — and the caller already has to know which it asked.
func (e Endpoint) SearchTraces(ctx context.Context, q TraceQuery) ([]byte, error) {
	if err := q.Validate(e.traceAPI()); err != nil {
		return nil, err
	}
	now := time.Now()
	switch e.traceAPI() {
	case CollectorJaeger:
		return e.fetchSearch(ctx, jaegerSearchPath, q.jaegerParams(now))
	default:
		return e.fetchSearch(ctx, tempoSearchPath, q.tempoParams(now))
	}
}

// traceAPI is which API answers at this endpoint. Unset is Tempo, which is what
// `traces:` meant before it could say: a config that named a Tempo and did not
// have to spell it out keeps working.
func (e Endpoint) traceAPI() Collector {
	if e.Collector == CollectorJaeger {
		return CollectorJaeger
	}
	return CollectorTempo
}

// traceTenantHeaders name the tenant halves the way the store spells them.
//
// Tempo takes one tenant under Grafana's header, so a tenant written with a
// colon in it is one tenant whose name has a colon. The Jaeger API is served
// here by VictoriaTraces as often as by Jaeger, and that one splits a tenant
// the way its logs do; a Jaeger that has never heard of either header ignores
// both.
func (e Endpoint) traceTenantHeaders() []string {
	if e.traceAPI() == CollectorJaeger {
		return vlogsTenantHeaders
	}
	return lokiTenantHeader
}

func (e Endpoint) fetchSearch(ctx context.Context, path string, params url.Values) ([]byte, error) {
	req, err := e.request(ctx, path, params)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", acceptJSON)
	e.setTenant(req, e.traceTenantHeaders()...)

	resp, err := httpClient(e).Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "search traces")
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
		return nil, errors.Wrap(err, "read search")
	}
	return data, nil
}

// TraceServices lists the services the store has traces from, so the form can
// offer them rather than asking somebody to remember how a service is spelled.
//
// A store that will not say is not a failure worth stopping for: the field is
// typed into either way, and the suggestions only ever add to what is possible.
// That is the same bargain [Config.FieldNames] makes for the filter prompt.
func (e Endpoint) TraceServices(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, fieldsTimeout)
	defer cancel()

	if e.traceAPI() == CollectorJaeger {
		return e.jaegerList(ctx, jaegerServicesPath)
	}
	return e.tempoTagValues(ctx, tempoServiceTag)
}

// TraceOperations lists what one service was called to do. Jaeger indexes them
// per service; Tempo has no such index, so it answers with every span name it
// has seen and the service is not honored there.
func (e Endpoint) TraceOperations(ctx context.Context, service string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, fieldsTimeout)
	defer cancel()

	if e.traceAPI() == CollectorJaeger {
		service = strings.TrimSpace(service)
		if service == "" {
			return nil, nil
		}
		return e.jaegerList(ctx, jaegerServicesPath+"/"+url.PathEscape(service)+"/operations")
	}
	return e.tempoTagValues(ctx, tempoOperationTag)
}

// TraceTagKeys lists what the spans in the store are labeled with, so the tag
// field can be finished rather than remembered.
//
// Only Tempo can answer it. The Jaeger API lists services and the operations of
// one and has no endpoint for tag names at all, so there is nothing to ask
// there — and nothing is not a failure: the field is typed into either way, the
// same bargain [Endpoint.TraceServices] makes for a store that refuses.
func (e Endpoint) TraceTagKeys(ctx context.Context) ([]string, error) {
	if e.traceAPI() == CollectorJaeger {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, fieldsTimeout)
	defer cancel()

	body, err := e.fetchSearch(ctx, tempoTagsPath, url.Values{})
	if err != nil {
		return nil, err
	}
	return tagNames(body)
}

// TraceTagValues lists what one tag has been.
//
// The key is scoped on the way out by the same rule the query is compiled
// under, since Tempo answers for `.http.method` and `span.http.method`
// separately: a suggestion listed under a narrower question than the one the
// search will ask would offer values the search then does not find.
func (e Endpoint) TraceTagValues(ctx context.Context, key string) ([]string, error) {
	key = strings.TrimSpace(key)
	if key == "" || e.traceAPI() == CollectorJaeger {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, fieldsTimeout)
	defer cancel()

	return e.tempoTagValues(ctx, traceQLKey(key))
}

// tagNames reads {"scopes":[{"name":…,"tags":[…]}]} off the tag names endpoint.
//
// Which scope a tag was found in is dropped. The field these are offered under
// is typed unscoped — `http.status_code`, not `span.http.status_code` — and
// [TraceQuery.TraceQL] is what decides to look in both scopes for it, so a
// suggestion that carried one would quietly compile into the narrower question.
func tagNames(body []byte) ([]string, error) {
	var out []string
	d := jx.DecodeBytes(body)
	if err := d.ObjBytes(func(d *jx.Decoder, k []byte) error {
		if string(k) != "scopes" {
			return d.Skip()
		}
		return d.Arr(func(d *jx.Decoder) error {
			return d.ObjBytes(func(d *jx.Decoder, k []byte) error {
				if string(k) != "tags" {
					return d.Skip()
				}
				return d.Arr(func(d *jx.Decoder) error {
					v, err := d.Str()
					if err != nil {
						return err
					}
					if v != "" {
						out = append(out, v)
					}
					return nil
				})
			})
		})
	}); err != nil {
		return nil, errors.Wrap(err, "decode tag names")
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// tempoTagValues reads {"tagValues":[…]} off the tag values endpoint.
func (e Endpoint) tempoTagValues(ctx context.Context, tag string) ([]string, error) {
	body, err := e.fetchSearch(ctx, tempoTagValuesPath+url.PathEscape(tag)+"/values", url.Values{})
	if err != nil {
		return nil, err
	}
	return listOf(body, "tagValues", "value")
}

// jaegerList reads {"data":[…]} off one of the listing endpoints.
func (e Endpoint) jaegerList(ctx context.Context, path string) ([]string, error) {
	body, err := e.fetchSearch(ctx, path, url.Values{})
	if err != nil {
		return nil, err
	}
	return listOf(body, "data", "name")
}

// listOf reads a list of names out of a JSON object, sorted and deduplicated.
//
// An element is a string or an object holding one, because both APIs changed
// their minds about that and both spellings are still served: Tempo's v1 tag
// values are strings and its v2 are `{"type":"string","value":"api"}`, and
// Jaeger's operations were strings before they were `{"name":"GET /","spanKind":
// "server"}`. Reading whichever arrived costs a branch; asking for a version
// costs a round trip finding out which one the server is.
func listOf(body []byte, key, field string) ([]string, error) {
	var out []string
	d := jx.DecodeBytes(body)
	if err := d.ObjBytes(func(d *jx.Decoder, k []byte) error {
		if string(k) != key {
			return d.Skip()
		}
		return d.Arr(func(d *jx.Decoder) error {
			switch d.Next() {
			case jx.String:
				v, err := d.Str()
				if err != nil {
					return err
				}
				if v != "" {
					out = append(out, v)
				}
				return nil
			case jx.Object:
				return d.ObjBytes(func(d *jx.Decoder, k []byte) error {
					if string(k) != field {
						return d.Skip()
					}
					v, err := d.Str()
					if err != nil {
						return err
					}
					if v != "" {
						out = append(out, v)
					}
					return nil
				})
			default:
				return d.Skip()
			}
		})
	}); err != nil {
		return nil, errors.Wrap(err, "decode list")
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
