package source

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/jx"
)

// Loki API paths, resolved against [Endpoint.URL].
const lokiQueryRangePath = "/loki/api/v1/query_range"

// lokiTenantHeader selects a tenant of a multi-tenant Loki.
var lokiTenantHeader = []string{"X-Scope-OrgID"}

const (
	// lokiSince is how far back the first query looks. A line count says
	// nothing about a time range, and Loki needs one; this is its own default
	// range, widened enough that a quiet service still shows something.
	lokiSince = 6 * time.Hour
	// lokiFollowLimit bounds one poll. A follow that hits it is behind, and the
	// next poll continues from where this one stopped.
	lokiFollowLimit = 5000
)

// lokiPoll is how often the follow query runs. Loki's own tail endpoint is a
// websocket, which a Grafana datasource proxy will not upgrade, so following is
// a query repeated against a moving start. It is a variable so tests need not
// wait out a real interval.
var lokiPoll = 2 * time.Second

// lokiQuery is the LogQL to run, which is the target as typed.
func (c Config) lokiQuery() string { return strings.TrimSpace(c.Target) }

// streamLoki reads the history, then polls for what follows it.
func (c Config) streamLoki(ctx context.Context, out func(Line) bool) error {
	client := httpClient(c.Endpoint)
	last, err := c.lokiBackfill(ctx, client, out)
	if err != nil {
		return err
	}
	if !c.following() {
		return nil
	}
	return c.lokiFollow(ctx, client, last, out)
}

// lokiBackfill emits the last Tail entries and returns the newest timestamp it
// saw. Loki answers a backward query newest first, so the entries are sorted
// before being emitted.
func (c Config) lokiBackfill(ctx context.Context, client *http.Client, out func(Line) bool) (last time.Time, err error) {
	if c.Tail <= 0 {
		return time.Time{}, nil
	}
	params := url.Values{
		"query":     {c.lokiQuery()},
		"limit":     {strconv.Itoa(c.Tail)},
		"direction": {"backward"},
	}
	// Without a range Loki still needs one, and its own default is an hour;
	// lokiSince stands in until the user picks a window.
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
	entries, err := c.lokiRequest(ctx, client, params)
	if err != nil {
		return time.Time{}, err
	}
	for _, e := range entries {
		if e.at.After(last) {
			last = e.at
		}
		if !out(e.line()) {
			break
		}
	}
	return last, nil
}

// lokiFollow repeats the query from where the last one ended, until the context
// is done.
//
// Each poll starts one nanosecond after the newest entry already shown, which
// is what Loki's own inclusive start bound needs to not repeat it.
func (c Config) lokiFollow(ctx context.Context, client *http.Client, last time.Time, out func(Line) bool) error {
	if last.IsZero() {
		// Nothing was backfilled, so the window's own start is where to pick up.
		last = c.Range.Since
	}
	if last.IsZero() {
		last = time.Now().Add(-time.Second)
	}
	ticker := time.NewTicker(lokiPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		entries, err := c.lokiRequest(ctx, client, url.Values{
			"query":     {c.lokiQuery()},
			"limit":     {strconv.Itoa(lokiFollowLimit)},
			"direction": {"forward"},
			// Inclusive on both ends, so the poll starts just past the newest
			// line already shown.
			"start": {lokiNanos(last.Add(time.Nanosecond))},
			"end":   {lokiNanos(time.Now())},
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, e := range entries {
			if e.at.After(last) {
				last = e.at
			}
			if !out(e.line()) {
				return nil
			}
		}
	}
}

// lokiNanos writes an instant the way Loki reads a bound.
func lokiNanos(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

// lokiEntry is one log line of a query result.
type lokiEntry struct {
	at   time.Time
	data []byte
}

func (e lokiEntry) line() Line { return Line{Data: e.data, At: e.at} }

// lokiRequest runs one query and returns its entries, oldest first.
//
// Unlike VictoriaLogs, Loki answers with a single JSON document holding one
// list per stream, so there is nothing to stream: the whole response is read,
// merged and sorted before anything is shown.
func (c Config) lokiRequest(ctx context.Context, client *http.Client, params url.Values) ([]lokiEntry, error) {
	req, err := c.Endpoint.request(ctx, lokiQueryRangePath, params)
	if err != nil {
		return nil, err
	}
	c.Endpoint.setTenant(req, lokiTenantHeader...)

	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "query loki")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
	}()
	if err := checkResponse(resp); err != nil {
		return nil, errors.Wrapf(err, "loki %s", lokiQueryRangePath)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, errors.Wrap(err, "read response")
	}
	entries, err := parseLokiStreams(body)
	if err != nil {
		return nil, err
	}
	// Every stream is sorted on its own; the view wants one timeline.
	slices.SortStableFunc(entries, func(a, b lokiEntry) int { return a.at.Compare(b.at) })
	return entries, nil
}

// maxResponseSize bounds one query result, which is held whole in memory.
const maxResponseSize = 128 << 20

// parseLokiStreams reads the entries of a "streams" result, ignoring the labels
// and anything else the document carries.
//
// A metric query answers with a "matrix" instead, which has no lines in it; it
// is reported rather than shown as an empty result.
func parseLokiStreams(body []byte) ([]lokiEntry, error) {
	var (
		out        []lokiEntry
		resultType string
		parseErr   error
	)
	d := jx.DecodeBytes(body)
	err := d.ObjBytes(func(d *jx.Decoder, key []byte) error {
		if string(key) != "data" {
			return d.Skip()
		}
		return d.ObjBytes(func(d *jx.Decoder, key []byte) error {
			switch string(key) {
			case "resultType":
				s, err := d.Str()
				resultType = s
				return err
			case "result":
				return arr(d, func(d *jx.Decoder) error {
					return d.ObjBytes(func(d *jx.Decoder, key []byte) error {
						if string(key) != "values" {
							return d.Skip()
						}
						return arr(d, func(d *jx.Decoder) error {
							// Read the whole entry before looking inside it, so
							// one malformed entry costs that entry and not the
							// rest of the result behind it.
							raw, err := d.Raw()
							if err != nil {
								return err
							}
							e, err := parseLokiValue(raw)
							if err != nil {
								parseErr = err
								return nil
							}
							out = append(out, e)
							return nil
						})
					})
				})
			default:
				return d.Skip()
			}
		})
	})
	if err != nil {
		return nil, errors.Wrap(err, "decode loki response")
	}
	if resultType != "" && resultType != "streams" {
		return nil, errors.Errorf("query answered with %q, which has no log lines in it", resultType)
	}
	if len(out) == 0 && parseErr != nil {
		return nil, errors.Wrap(parseErr, "decode entry")
	}
	return out, nil
}

// arr decodes an array that may have been written as null, which is what an
// empty result looks like from some of the things that answer for Loki.
func arr(d *jx.Decoder, fn func(*jx.Decoder) error) error {
	if d.Next() == jx.Null {
		return d.Skip()
	}
	return d.Arr(fn)
}

// parseLokiValue reads one ["<unix nanos>", "<line>", ...] pair. Newer Loki
// appends structured metadata as a third element, which is skipped.
func parseLokiValue(raw jx.Raw) (lokiEntry, error) {
	var (
		e lokiEntry
		i int
	)
	err := jx.DecodeBytes(raw).Arr(func(d *jx.Decoder) error {
		defer func() { i++ }()
		switch i {
		case 0:
			s, err := d.Str()
			if err != nil {
				return err
			}
			ns, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return errors.Wrapf(err, "timestamp %q", s)
			}
			e.at = time.Unix(0, ns)
			return nil
		case 1:
			s, err := d.StrBytes()
			if err != nil {
				return err
			}
			e.data = slices.Clone(s)
			return nil
		default:
			return d.Skip()
		}
	})
	if err != nil {
		return lokiEntry{}, err
	}
	if i < 2 {
		return lokiEntry{}, errors.New("entry has no line")
	}
	return e, nil
}
