package mcp

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
	"github.com/oteldb/telescope/internal/trace"
)

const searchDescription = "Finds traces at a store: by the service that ran " +
	"them, the operation they were called, how long they took and what their " +
	"spans were labeled with. Answers a summary of each — id, root, when, how " +
	"long — which is what names one for the trace tool to read whole."

// The bounds on how many traces one search answers with. A summary is a line
// each, so more of them is cheap, but a reader that asked for a hundred traces
// is looking for a pattern and should be counting rather than listing.
const (
	defaultSearchLimit = 20
	maxSearchLimit     = 100
)

type searchInput struct {
	Place       string `json:"place,omitempty" jsonschema:"Which store to search, as places reports it: a store, or a place whose logs carry ids into one. May be left out when the config declares a single store"`
	Service     string `json:"service,omitempty" jsonschema:"The service that ran the request. Required by a jaeger store, which indexes per service; trace_fields lists them"`
	Operation   string `json:"operation,omitempty" jsonschema:"What the root was called, as trace_fields lists it for a service"`
	Tags        string `json:"tags,omitempty" jsonschema:"What a span was labeled with, written as key=value pairs separated by spaces: http.status_code=500 error=true. A trace matches when one of its spans carries all of them"`
	MinDuration string `json:"min_duration,omitempty" jsonschema:"Only traces at least this long, as a Go duration: 500ms, 2s"`
	MaxDuration string `json:"max_duration,omitempty" jsonschema:"Only traces at most this long, as a Go duration"`
	Range       string `json:"range,omitempty" jsonschema:"The window, relative or absolute: 1h, today, yesterday, 6h..1h, 10:00..12:00, 2026-01-02 10:00..2026-01-02 12:00, or two RFC 3339 instants. Empty searches the last hour"`
	Limit       int    `json:"limit,omitempty" jsonschema:"How many traces to return, at most 100. Defaults to 20"`
}

// found is one trace as a search reports it.
//
// The three counts are separate fields and never one, because the two backends
// count different things: Jaeger answers with the traces themselves and so
// knows how many spans each holds and how many failed, while Tempo answers with
// a summary and knows only how many spans its query selected. A column that
// meant whichever the backend happened to report would be a number nobody can
// read — see [trace.Result].
type found struct {
	TraceID  string        `json:"trace_id"`
	Service  string        `json:"service,omitempty"`
	Name     string        `json:"name,omitempty"`
	Start    string        `json:"start" jsonschema:"When the trace began, RFC 3339"`
	Millis   int64         `json:"duration_ms"`
	Spans    int           `json:"spans,omitempty" jsonschema:"How many spans the trace holds, where the store answered with the trace itself"`
	Errors   int           `json:"errors,omitempty" jsonschema:"How many of them failed, where the store answered with the trace itself"`
	Matched  int           `json:"matched,omitempty" jsonschema:"How many spans the query selected, where the store counts that instead"`
	Duration time.Duration `json:"-"`
	At       time.Time     `json:"-"`
}

type searchOutput struct {
	Store    string  `json:"store" jsonschema:"The store that answered"`
	Asked    string  `json:"asked" jsonschema:"The query as the store was asked it"`
	Window   string  `json:"window" jsonschema:"The interval searched"`
	Returned int     `json:"returned"`
	Traces   []found `json:"traces"`
	Note     string  `json:"note,omitempty" jsonschema:"What was cut, and what the answer leaves out"`
}

func addTraceSearch(s *sdk.Server, cfg config.Config) {
	addTool(s, "trace_search", searchDescription, searchHandler(cfg))
}

func searchHandler(cfg config.Config) sdk.ToolHandlerFor[searchInput, searchOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in searchInput) (*sdk.CallToolResult, searchOutput, error) {
		at, err := traceStore(cfg, in.Place)
		if err != nil {
			return nil, searchOutput{}, err
		}
		q, capped, err := searchQuery(in)
		if err != nil {
			return nil, searchOutput{}, err
		}

		ctx, cancel := context.WithTimeout(ctx, traceFetchTimeout)
		defer cancel()

		data, err := at.SearchTraces(ctx, q)
		if err != nil {
			return nil, searchOutput{}, err
		}
		results, err := searchResults(at, data)
		if err != nil {
			return nil, searchOutput{}, err
		}
		trace.SortResults(results)

		out := describeSearch(at, q, results)
		out.Note = join(capped, out.Note)
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: drawSearch(out)}},
		}, out, nil
	}
}

// searchResults reads what a store answered, by the API it is. The two send
// different documents — Tempo a summary of each trace, Jaeger the traces
// themselves — and both are valid JSON to the other's decoder, so one of them
// would quietly come out empty if the choice were made by trying.
func searchResults(at source.Endpoint, data []byte) ([]trace.Result, error) {
	if at.Collector != source.CollectorJaeger {
		return trace.DecodeSearch(data)
	}
	found, err := trace.DecodeJaegerSearch(data)
	if err != nil {
		return nil, err
	}
	out := make([]trace.Result, 0, len(found))
	for _, t := range found {
		out = append(out, trace.Summary(t))
	}
	return out, nil
}

func searchQuery(in searchInput) (source.TraceQuery, string, error) {
	q := source.TraceQuery{
		Service:   strings.TrimSpace(in.Service),
		Operation: strings.TrimSpace(in.Operation),
	}
	limit, capped, err := capLimit(in.Limit, defaultSearchLimit, maxSearchLimit)
	if err != nil {
		return source.TraceQuery{}, "", err
	}
	q.Limit = limit
	if q.Tags, err = source.ParseTags(in.Tags); err != nil {
		return source.TraceQuery{}, "", err
	}
	if q.MinDuration, err = bound(in.MinDuration, "min_duration"); err != nil {
		return source.TraceQuery{}, "", err
	}
	if q.MaxDuration, err = bound(in.MaxDuration, "max_duration"); err != nil {
		return source.TraceQuery{}, "", err
	}
	if spec := strings.TrimSpace(in.Range); spec != "" {
		if q.Range, err = source.ParseRange(spec, time.Now()); err != nil {
			return source.TraceQuery{}, "", err
		}
	}
	return q, capped, nil
}

func bound(s, name string) (time.Duration, error) {
	if s = strings.TrimSpace(s); s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, errors.Wrapf(err, "%s", name)
	}
	if d < 0 {
		return 0, errors.Errorf("%s is negative", name)
	}
	return d, nil
}

func describeSearch(at source.Endpoint, q source.TraceQuery, results []trace.Result) searchOutput {
	window := q.Window(time.Now())
	out := searchOutput{
		Store:    at.URL,
		Asked:    q.Asked(at.Collector),
		Window:   windowSpec(window),
		Returned: len(results),
		Traces:   make([]found, 0, len(results)),
	}
	for _, r := range results {
		out.Traces = append(out.Traces, found{
			TraceID:  r.TraceID,
			Service:  r.Service,
			Name:     r.Name,
			Start:    r.Start.Format(time.RFC3339Nano),
			Millis:   r.Duration.Round(time.Millisecond).Milliseconds(),
			Spans:    r.Spans,
			Errors:   r.Errors,
			Matched:  r.Matched,
			At:       r.Start,
			Duration: r.Duration,
		})
	}
	out.Note = searchNote(out, q)
	return out
}

// windowSpec is the interval searched, written out rather than named. What the
// caller typed is a relative thing and the answer is about a fixed one, so the
// two instants are what makes the answer comparable to the next one.
func windowSpec(r source.Range) string {
	return r.Since.Format(time.RFC3339) + ".." + r.Until.Format(time.RFC3339)
}

func searchNote(out searchOutput, q source.TraceQuery) string {
	var note string
	if out.Returned == 0 {
		return "nothing matched in the window. A trace store keeps less than a log " +
			"database does, and a sampled one never had most of them"
	}
	if out.Returned >= q.Limit {
		note = join(note, "this is the whole of the limit ("+strconv.Itoa(q.Limit)+
			"), so the window holds more: narrow it, or ask for more")
	}
	return note
}
