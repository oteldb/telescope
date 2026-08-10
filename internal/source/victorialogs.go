package source

import (
	"bufio"
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

// VictoriaLogs API paths, resolved against [Endpoint.URL].
const (
	vlogsQueryPath = "/select/logsql/query"
	vlogsTailPath  = "/select/logsql/tail"
)

// vlogsTenantHeaders name the tenant halves, in the order they are written.
var vlogsTenantHeaders = []string{"AccountID", "ProjectID"}

// vlogsStartOffset is how far back live tailing starts. It only has to cover
// the gap between the backfill query and the tail request; entries already seen
// are dropped by timestamp.
const vlogsStartOffset = 30 * time.Second

// vlogsQuery is the LogsQL to run, which is the target as typed. An empty
// target would select the whole database, so it is rejected in Validate.
func (c Config) vlogsQuery() string { return strings.TrimSpace(c.Target) }

// streamVictoriaLogs reads the backfill, then follows.
func (c Config) streamVictoriaLogs(ctx context.Context, out func(Line) bool) error {
	client := httpClient(c.Endpoint)
	last, err := c.vlogsBackfill(ctx, client, out)
	if err != nil {
		return err
	}
	if !c.Follow {
		return nil
	}
	return c.vlogsTail(ctx, client, last, out)
}

// vlogsBackfill emits the last Tail entries and returns the newest timestamp it
// saw, which is where following picks up.
//
// The query endpoint answers newest first when a limit is given, so the entries
// are collected before being emitted rather than streamed.
func (c Config) vlogsBackfill(ctx context.Context, client *http.Client, out func(Line) bool) (last time.Time, err error) {
	if c.Tail <= 0 {
		return time.Time{}, nil
	}
	params := url.Values{
		"query": {c.vlogsQuery()},
		"limit": {strconv.Itoa(c.Tail)},
	}
	var lines [][]byte
	if err := c.vlogsRequest(ctx, client, vlogsQueryPath, params, func(entry []byte) bool {
		if t, ok := vlogsTime(entry); ok && t.After(last) {
			last = t
		}
		lines = append(lines, vlogsNormalize(entry))
		return true
	}); err != nil {
		return last, err
	}
	slices.Reverse(lines)
	for _, line := range lines {
		if !out(Line{Data: line}) {
			break
		}
	}
	return last, nil
}

// vlogsTail follows the query, dropping what the backfill already showed.
func (c Config) vlogsTail(ctx context.Context, client *http.Client, last time.Time, out func(Line) bool) error {
	params := url.Values{
		"query":        {c.vlogsQuery()},
		"start_offset": {vlogsStartOffset.String()},
	}
	return c.vlogsRequest(ctx, client, vlogsTailPath, params, func(entry []byte) bool {
		if t, ok := vlogsTime(entry); ok && !t.After(last) {
			return true
		}
		return out(Line{Data: vlogsNormalize(entry)})
	})
}

// vlogsRequest calls one endpoint and hands each JSON line to fn, which stops
// the read by returning false. Both endpoints answer with JSON lines, so the
// response is scanned rather than buffered: that is what makes tailing live.
func (c Config) vlogsRequest(
	ctx context.Context,
	client *http.Client,
	path string,
	params url.Values,
	fn func(entry []byte) bool,
) error {
	req, err := c.Endpoint.request(ctx, path, params)
	if err != nil {
		return err
	}
	c.Endpoint.setTenant(req, vlogsTenantHeaders...)

	resp, err := client.Do(req)
	if err != nil {
		return errors.Wrap(err, "query victorialogs")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
	}()
	if err := checkResponse(resp); err != nil {
		return errors.Wrapf(err, "victorialogs %s", path)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for sc.Scan() {
		if entry := sc.Bytes(); len(entry) > 0 && !fn(entry) {
			return nil
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return errors.Wrap(err, "read response")
	}
	return nil
}

// vlogsFields renames the envelope VictoriaLogs wraps every entry in onto the
// keys a log line is normally written with, so the message is rendered as the
// message rather than as a field called _msg. Everything else is left alone,
// including _stream: a query can drop what it does not want with "| drop".
var vlogsFields = map[string]string{"_time": "time", "_msg": "msg"}

// vlogsNormalize applies [vlogsFields] to one entry. A key the entry already
// carries wins, since that one is the application's own. Anything that does not
// decode is passed through untouched.
//
// The result never aliases entry, which is the scanner's buffer and is reused
// on the next line.
func vlogsNormalize(entry []byte) []byte {
	type field struct {
		key   string
		value jx.Raw
	}
	var (
		fields []field
		seen   = map[string]bool{}
	)
	d := jx.DecodeBytes(entry)
	if err := d.ObjBytes(func(d *jx.Decoder, key []byte) error {
		raw, err := d.RawAppend(nil)
		if err != nil {
			return err
		}
		fields = append(fields, field{key: string(key), value: raw})
		seen[string(key)] = true
		return nil
	}); err != nil {
		return slices.Clone(entry)
	}

	var renamed bool
	for i, f := range fields {
		to, ok := vlogsFields[f.key]
		if !ok || seen[to] {
			continue
		}
		fields[i].key, renamed = to, true
	}
	if !renamed {
		return slices.Clone(entry)
	}

	var e jx.Encoder
	e.ObjStart()
	for _, f := range fields {
		e.FieldStart(f.key)
		e.Raw(f.value)
	}
	e.ObjEnd()
	return e.Bytes()
}

// vlogsTime reads the _time of an entry without decoding the rest of it.
func vlogsTime(entry []byte) (time.Time, bool) {
	var out time.Time
	d := jx.DecodeBytes(entry)
	if err := d.ObjBytes(func(d *jx.Decoder, key []byte) error {
		if string(key) != "_time" {
			return d.Skip()
		}
		s, err := d.Str()
		if err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return err
		}
		out = t
		return nil
	}); err != nil {
		return time.Time{}, false
	}
	return out, !out.IsZero()
}
