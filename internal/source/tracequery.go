package source

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
)

// TraceQuery is what a trace store is asked for: a service, an operation of it,
// tags the spans carry, and how long the whole thing took.
//
// It is Jaeger's form and not Tempo's, for two reasons. It is the one that can
// be drawn as fields — a service, an operation, some tags — and a form is what
// somebody hunting a request wants to fill in rather than a language to learn.
// And it is the smaller of the two, so it compiles into TraceQL without
// inventing anything: every field here is one term there. The reverse would not
// hold, which is why this is not a TraceQL prompt with a Jaeger translation.
//
// What it cannot say is deliberate. TraceQL can ask for structural things —
// a span with a child that failed — and no field here means that. Somebody who
// needs it is asking a question this screen does not answer.
type TraceQuery struct {
	// Service is who ran the request, and Operation what it was called. Only
	// the first is ever required, and only by Jaeger: its API refuses a search
	// that does not name one, since its index is per service.
	Service   string
	Operation string
	// Tags narrow by what the spans were labeled with. A trace matches when one
	// of its spans carries all of them — not when the trace as a whole does,
	// which is the same rule in both APIs.
	Tags []TraceTag
	// MinDuration and MaxDuration bound how long the trace took. Zero is
	// unbounded at that end.
	MinDuration, MaxDuration time.Duration
	// Range is the window searched. Both backends want an interval and neither
	// agrees what the default is, so [Endpoint.SearchTraces] resolves an empty
	// one rather than leaving it to whichever server answered.
	Range Range
	// Limit is how many traces to return.
	Limit int
}

// TraceTag is one key and value a span was labeled with. It is a pair and not a
// map so the order somebody typed them in survives to the query, which is what
// makes a compiled one readable back.
type TraceTag struct {
	Key   string
	Value string
}

// searchWindow is how far back a search with no range reaches, and
// searchWindowSpec is how that is written where it is shown. Jaeger requires a
// window and Tempo defaults to one of its own, so telescope picks it instead:
// the same form typed at two backends has to search the same interval or the
// results cannot be compared.
//
// They are one value said twice, once for the request and once for the screen,
// and a test holds them to it.
const (
	searchWindow     = time.Hour
	searchWindowSpec = "1h"
)

// searchLimit is how many traces a search asks for when nothing says. It is
// jaeger-ui's default, and about a screenful.
const searchLimit = 20

// IsZero reports whether the query narrows nothing, which for Tempo is a search
// for everything in the window.
func (q TraceQuery) IsZero() bool {
	return strings.TrimSpace(q.Service) == "" && strings.TrimSpace(q.Operation) == "" &&
		len(q.Tags) == 0 && q.MinDuration == 0 && q.MaxDuration == 0
}

// Validate reports what the given API will not accept, before a round trip
// spends time finding out.
func (q TraceQuery) Validate(api Collector) error {
	if q.MinDuration > 0 && q.MaxDuration > 0 && q.MinDuration > q.MaxDuration {
		return errors.Errorf("min duration %s is longer than max %s", q.MinDuration, q.MaxDuration)
	}
	if api == CollectorJaeger && strings.TrimSpace(q.Service) == "" {
		// Jaeger indexes per service and refuses to search without one. Said
		// here rather than passed on, since its own complaint arrives as a 400
		// with the parameter name in it and nothing about what to do.
		return errors.New("this trace store searches by service: name one")
	}
	return nil
}

// TraceQL compiles the query into the language Tempo reads.
//
// Everything is a span condition joined by &&, which is TraceQL's rule that a
// trace matches when one span satisfies the lot. Unscoped attributes — `.key`
// rather than `span.key` or `resource.key` — are used for tags because a tag
// somebody types is as likely to have been recorded on the resource as on the
// span, and the reader is not the one who should have to know which.
//
// A query that narrows nothing compiles to `{}`, which matches every trace in
// the window. That is a real answer here and not an empty one: unlike LogQL,
// TraceQL can say "everything", so a search with the form left blank is the
// last hour of traffic rather than a refusal.
func (q TraceQuery) TraceQL() string {
	var terms []string
	if s := strings.TrimSpace(q.Service); s != "" {
		terms = append(terms, "resource.service.name="+traceQLString(s))
	}
	if op := strings.TrimSpace(q.Operation); op != "" {
		terms = append(terms, "name="+traceQLString(op))
	}
	for _, t := range q.Tags {
		key := strings.TrimSpace(t.Key)
		if key == "" {
			continue
		}
		terms = append(terms, traceQLKey(key)+"="+traceQLValue(t.Value))
	}
	// Durations are the trace's, so they are written outside the span
	// conditions: `{...} | duration > 100ms` would be a filter over what the
	// braces selected, and duration inside them is the span's own.
	var suffix string
	if q.MinDuration > 0 {
		suffix += " && traceDuration>" + traceQLDuration(q.MinDuration)
	}
	if q.MaxDuration > 0 {
		suffix += " && traceDuration<" + traceQLDuration(q.MaxDuration)
	}
	return "{" + strings.TrimPrefix(strings.Join(terms, " && ")+suffix, " && ") + "}"
}

// traceQLKey writes an attribute name. A name that is already scoped is left
// alone, so somebody who knows TraceQL can type `span.http.status_code` and
// mean it; anything else is unscoped and searched in both scopes.
func traceQLKey(key string) string {
	for _, scope := range []string{"span.", "resource.", "event.", "link.", "instrumentation."} {
		if strings.HasPrefix(key, scope) {
			return key
		}
	}
	if strings.HasPrefix(key, ".") {
		return key
	}
	// Intrinsics are the fields of the span itself rather than its attributes,
	// and are written bare. A tag named after one would otherwise compile into
	// a comparison against an attribute nobody set.
	switch key {
	case "name", "status", "statusMessage", "kind", "duration", "traceDuration", "rootName", "rootServiceName":
		return key
	}
	return "." + key
}

// traceQLValue writes a value as the type it was typed as. TraceQL compares
// typed values, so a status code quoted as a string does not match an attribute
// recorded as an integer — and `error=true` means the boolean, which is how
// every tracer that still writes that tag records it.
func traceQLValue(v string) string {
	v = strings.TrimSpace(v)
	switch v {
	case "true", "false":
		return v
	}
	if _, err := strconv.ParseInt(v, 10, 64); err == nil {
		return v
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return v
	}
	return traceQLString(v)
}

// traceQLString quotes a value. It is somebody else's text going into a query
// language, so the quoting is the whole of what keeps it one value: strconv
// escapes the quote and the backslash, which are what could end the string
// early.
func traceQLString(s string) string { return strconv.Quote(s) }

// traceQLDuration writes a duration the way TraceQL spells one. Go's own
// spelling is close enough that it is only sub-second precision that differs,
// and a search bounded to the nanosecond is not a search anybody typed.
func traceQLDuration(d time.Duration) string {
	if d%time.Millisecond == 0 && d >= time.Millisecond {
		return fmt.Sprintf("%dms", d/time.Millisecond)
	}
	return d.String()
}

// Asked is the query as the given API was actually asked it, for reporting
// back to whoever asked.
//
// It is not always TraceQL. Jaeger takes named parameters and never compiles a
// query at all, so showing it a TraceQL string would be telling a reader the
// store was sent something it has never seen — and the whole point of saying
// what was asked is that a reader can tell what the store did from what was
// filtered afterwards.
//
// The window and the limit are left out of both: they are reported beside this
// rather than inside it, and a caller that showed them twice would be paying
// for the same fact twice.
func (q TraceQuery) Asked(api Collector) string {
	if api != CollectorJaeger {
		return q.TraceQL()
	}
	var said []string
	for _, kv := range [][2]string{
		{"service", strings.TrimSpace(q.Service)},
		{"operation", strings.TrimSpace(q.Operation)},
		{"tags", q.tagObject()},
	} {
		if kv[1] != "" {
			said = append(said, kv[0]+"="+kv[1])
		}
	}
	if q.MinDuration > 0 {
		said = append(said, "minDuration="+q.MinDuration.String())
	}
	if q.MaxDuration > 0 {
		said = append(said, "maxDuration="+q.MaxDuration.String())
	}
	if len(said) == 0 {
		return "everything in the window"
	}
	return strings.Join(said, " ")
}

// jaegerParams compiles the query into the parameters Jaeger's /api/traces
// reads.
//
// Its times are microseconds since the epoch, as everything in that API is, and
// both ends are always sent: Jaeger will not search without a window, and a
// server left to pick one picks a different one from Tempo.
func (q TraceQuery) jaegerParams(now time.Time) url.Values {
	params := url.Values{}
	if s := strings.TrimSpace(q.Service); s != "" {
		params.Set("service", s)
	}
	if op := strings.TrimSpace(q.Operation); op != "" {
		params.Set("operation", op)
	}
	if tags := q.tagObject(); tags != "" {
		params.Set("tags", tags)
	}
	if q.MinDuration > 0 {
		params.Set("minDuration", q.MinDuration.String())
	}
	if q.MaxDuration > 0 {
		params.Set("maxDuration", q.MaxDuration.String())
	}
	since, until := q.window(now)
	params.Set("start", strconv.FormatInt(since.UnixMicro(), 10))
	params.Set("end", strconv.FormatInt(until.UnixMicro(), 10))
	params.Set("limit", strconv.Itoa(q.limit()))
	return params
}

// tagObject writes the tags as the JSON object Jaeger wants them in. Every
// value is a string there, whatever the tag was recorded as, which is the one
// place the two backends disagree about a form field's meaning.
func (q TraceQuery) tagObject() string {
	tags := make(map[string]string, len(q.Tags))
	for _, t := range q.Tags {
		if key := strings.TrimSpace(t.Key); key != "" {
			tags[key] = t.Value
		}
	}
	if len(tags) == 0 {
		return ""
	}
	// A map of strings cannot fail to marshal, and there is nothing useful to
	// do about it here if it somehow did.
	data, err := json.Marshal(tags)
	if err != nil {
		return ""
	}
	return string(data)
}

// tempoParams compiles the query into what Tempo's /api/search reads. Its times
// are seconds, not microseconds.
func (q TraceQuery) tempoParams(now time.Time) url.Values {
	since, until := q.window(now)
	return url.Values{
		"q":     {q.TraceQL()},
		"start": {strconv.FormatInt(since.Unix(), 10)},
		"end":   {strconv.FormatInt(until.Unix(), 10)},
		"limit": {strconv.Itoa(q.limit())},
	}
}

// Window is the interval the query covers, resolved.
//
// It exists so that what a search asks for and what the screen says it asked
// for cannot drift apart: a form with the range left blank searches the last
// hour, and saying "all" there would be claiming a history nobody looked
// through.
func (q TraceQuery) Window(now time.Time) Range {
	since, until := q.window(now)
	spec := q.Range.Spec
	if spec == "" && q.Range.IsZero() {
		spec = searchWindowSpec
	}
	return Range{Spec: spec, Since: since, Until: until}
}

// window resolves the interval searched, filling in whichever end was left
// open. A range with no end is up to now, which is what "the last hour" means
// to somebody who typed it a minute ago.
func (q TraceQuery) window(now time.Time) (since, until time.Time) {
	since, until = q.Range.Since, q.Range.Until
	if until.IsZero() {
		until = now
	}
	if since.IsZero() {
		since = until.Add(-searchWindow)
	}
	return since, until
}

func (q TraceQuery) limit() int {
	if q.Limit > 0 {
		return q.Limit
	}
	return searchLimit
}

// ParseTags reads the tag field as it is typed: `http.status_code=500
// error=true`, with a quoted value when one has a space in it.
//
// It is logfmt and not JSON because it is being typed into a one-line field by
// somebody who is looking for a request, not written into a file. A word with
// no `=` is not a tag and saying so is more use than searching for something
// nobody asked for.
func ParseTags(s string) ([]TraceTag, error) {
	fields, err := splitTags(s)
	if err != nil {
		return nil, err
	}
	var out []TraceTag
	for _, f := range fields {
		key, value, ok := strings.Cut(f, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, errors.Errorf("tag %q is not key=value", f)
		}
		out = append(out, TraceTag{Key: strings.TrimSpace(key), Value: value})
	}
	return out, nil
}

// splitTags breaks the field on spaces, keeping a quoted run together.
func splitTags(s string) ([]string, error) {
	var (
		out   []string
		cur   strings.Builder
		quote rune
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, errors.New("unclosed quote")
	}
	flush()
	return out, nil
}

// TagsSpec writes tags back the way they were typed, so what a search was asked
// can be shown and edited again.
func TagsSpec(tags []TraceTag) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		value := t.Value
		if strings.ContainsAny(value, " \t\"'") {
			value = strconv.Quote(value)
		}
		parts = append(parts, t.Key+"="+value)
	}
	return strings.Join(parts, " ")
}
