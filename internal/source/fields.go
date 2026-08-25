package source

import (
	"context"
	"io"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/jx"
)

// Field listing paths, resolved against [Endpoint.URL].
const (
	vlogsFieldNamesPath  = "/select/logsql/field_names"
	vlogsFieldValuesPath = "/select/logsql/field_values"
	lokiLabelsPath       = "/loki/api/v1/labels"
	lokiLabelValuesPath  = "/loki/api/v1/label/"
)

// fieldsTimeout bounds a listing. It is a suggestion and not the logs: a
// database that takes longer than this to say what its fields are has already
// cost more than the answer is worth.
const fieldsTimeout = 5 * time.Second

// FieldValuesLimit bounds how many values one field is asked for. A prompt
// cannot show more than a screenful, and a field with more distinct values than
// this was not going to be completed by reading them all.
//
// It is exported because a reader that is not a prompt has to be told that the
// answer was cut: a screen shows the cut by scrolling to the end of a list,
// and an agent reading exactly this many values would otherwise conclude that
// it had seen them all.
const FieldValuesLimit = 200

// FieldNames asks the source which names its lines are labeled with, so the
// filter prompt can offer them before a line carrying one has arrived.
//
// Only a log database can answer: a collector is a process writing to a pipe and
// knows nothing about its own output until it produces some. That is not a
// failure — the prompt completes by what it has read either way, and this only
// ever adds to it.
//
// A merge asks each of its children and offers the union, since a filter over a
// merge is one filter over all of them.
func (c Config) FieldNames(ctx context.Context) ([]string, error) {
	return c.askFields(ctx, func(ctx context.Context, c Config) ([]string, error) {
		switch c.Collector {
		case CollectorVictoriaLogs:
			return c.vlogsFieldList(ctx, vlogsFieldNamesPath, url.Values{"query": {c.vlogsSelector()}})
		case CollectorLoki:
			return c.lokiLabelList(ctx, lokiLabelsPath)
		default:
			return nil, nil
		}
	})
}

// FieldValues asks the source what values it has seen under one name. See
// [Config.FieldNames].
func (c Config) FieldValues(ctx context.Context, field string) ([]string, error) {
	if field == "" {
		return nil, nil
	}
	return c.askFields(ctx, func(ctx context.Context, c Config) ([]string, error) {
		switch c.Collector {
		case CollectorVictoriaLogs:
			return c.vlogsFieldList(ctx, vlogsFieldValuesPath, url.Values{
				"query": {c.vlogsSelector()},
				"field": {vlogsFieldName(field)},
				"limit": {strconv.Itoa(FieldValuesLimit)},
			})
		case CollectorLoki:
			return c.lokiLabelList(ctx, lokiLabelValuesPath+url.PathEscape(field)+"/values")
		default:
			return nil, nil
		}
	})
}

// askFields runs one listing, over a merge's children when it is one. A child
// that cannot answer contributes nothing rather than failing the lot: a merge of
// a database and a container should still complete by the database.
func (c Config) askFields(
	ctx context.Context,
	ask func(context.Context, Config) ([]string, error),
) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, fieldsTimeout)
	defer cancel()

	if c.Collector != CollectorMerge {
		return ask(ctx, c)
	}
	var (
		out     []string
		lastErr error
	)
	for _, sub := range c.Merge {
		got, err := ask(ctx, sub)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, got...)
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// vlogsFieldName is a name as VictoriaLogs holds it, undoing the renaming an
// entry goes through on the way in. What the prompt completes has to be what a
// line will turn out to be called, and what the database is asked has to be what
// it stored; see [vlogsFields].
func vlogsFieldName(field string) string {
	for stored, shown := range vlogsFields {
		if shown == field {
			return stored
		}
	}
	return field
}

// vlogsFieldList reads one of the endpoints that answer {"values":[{"value":…}]}.
func (c Config) vlogsFieldList(ctx context.Context, path string, params url.Values) ([]string, error) {
	if t := c.Range.Since; !t.IsZero() {
		params.Set("start", t.Format(time.RFC3339Nano))
	}
	if t := c.Range.Until; !t.IsZero() {
		params.Set("end", t.Format(time.RFC3339Nano))
	}
	body, err := c.fieldsRequest(ctx, path, params, vlogsTenantHeaders)
	if err != nil {
		return nil, err
	}

	var out []string
	d := jx.DecodeBytes(body)
	if err := d.ObjBytes(func(d *jx.Decoder, key []byte) error {
		if string(key) != "values" {
			return d.Skip()
		}
		return d.Arr(func(d *jx.Decoder) error {
			return d.ObjBytes(func(d *jx.Decoder, key []byte) error {
				if string(key) != "value" {
					return d.Skip()
				}
				v, err := d.Str()
				if err != nil {
					return err
				}
				if name := vlogsFields[v]; name != "" {
					v = name
				}
				out = append(out, v)
				return nil
			})
		})
	}); err != nil {
		return nil, errors.Wrap(err, "decode field list")
	}
	return out, nil
}

// lokiLabelList reads one of the endpoints that answer {"data":[…]}.
func (c Config) lokiLabelList(ctx context.Context, path string) ([]string, error) {
	params := url.Values{}
	switch {
	case c.Range.IsZero():
		params.Set("since", lokiSince.String())
	default:
		if t := c.Range.Since; !t.IsZero() {
			params.Set("start", lokiNanos(t))
		}
		if t := c.Range.Until; !t.IsZero() {
			params.Set("end", lokiNanos(t))
		}
	}
	body, err := c.fieldsRequest(ctx, path, params, lokiTenantHeader)
	if err != nil {
		return nil, err
	}

	var out []string
	d := jx.DecodeBytes(body)
	if err := d.ObjBytes(func(d *jx.Decoder, key []byte) error {
		if string(key) != "data" {
			return d.Skip()
		}
		return d.Arr(func(d *jx.Decoder) error {
			v, err := d.Str()
			if err != nil {
				return err
			}
			out = append(out, v)
			return nil
		})
	}); err != nil {
		return nil, errors.Wrap(err, "decode label list")
	}
	return out, nil
}

// maxFieldsBody bounds a listing response, which is read whole rather than
// scanned: it is one JSON document and not a stream of lines.
const maxFieldsBody = 4 * 1024 * 1024

func (c Config) fieldsRequest(ctx context.Context, path string, params url.Values, tenant []string) ([]byte, error) {
	req, err := c.Endpoint.request(ctx, path, params)
	if err != nil {
		return nil, err
	}
	c.Endpoint.setTenant(req, tenant...)

	resp, err := httpClient(c.Endpoint).Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "list fields")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
	}()
	if err := checkResponse(resp); err != nil {
		return nil, errors.Wrapf(err, "list fields %s", path)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFieldsBody))
}
