package trace

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
)

// Result is one trace as a search answered with it.
//
// It is not a [Tree] and does not become one: a search answers with a summary
// of each trace it found, and the spans are a second request made when somebody
// picks one. The two backends summarize differently, which is why the counts
// below are separate fields rather than one.
type Result struct {
	TraceID string
	// Service and Name are the root span's: who was called, and what for.
	Service string
	Name    string

	Start    time.Time
	Duration time.Duration

	// Spans is how many spans the trace holds and Errors how many of them
	// failed. Only a backend that answered with the trace itself can know
	// either, so for Tempo they are zero.
	Spans, Errors int
	// Matched is how many spans the query selected, which is what Tempo counts
	// and what Jaeger has no notion of. It is zero there for the same reason.
	//
	// The two are not interchangeable and must not be drawn as one number: "38
	// spans" and "3 matched" are different claims about the same trace, and a
	// column that meant whichever the backend happened to report would be a
	// number nobody can read.
	Matched int
}

// Summary describes a trace that arrived whole, which is what Jaeger's search
// answers with: it returns the traces themselves, so what a Tempo search would
// only summarize is already known here and counted rather than guessed.
func Summary(t *Tree) Result {
	if t == nil {
		return Result{}
	}
	out := Result{
		TraceID:  t.ID,
		Start:    t.Start,
		Duration: t.Duration(),
		Spans:    t.Len(),
	}
	for _, n := range t.nodes {
		if n.Failed() {
			out.Errors++
		}
	}
	// The first root is the request as far as this trace can see it. A trace
	// whose real root was sampled away has several, sorted by start, and the
	// earliest is the closest thing to the call that began the rest.
	if len(t.Roots) > 0 {
		out.Service, out.Name = t.Roots[0].Service, t.Roots[0].Name
	}
	if out.TraceID == "" && len(t.nodes) > 0 {
		out.TraceID = t.nodes[0].TraceID
	}
	return out
}

// DecodeSearch reads what Tempo's /api/search answered with.
//
// Finding nothing is not a failure: a search that matched no trace is an answer
// and an empty list is how it is said. Only a document that is not the response
// at all is an error.
func DecodeSearch(data []byte) ([]Result, error) {
	var doc struct {
		Traces []tempoResult `json:"traces"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, errors.Wrap(err, "decode")
	}
	out := make([]Result, 0, len(doc.Traces))
	for _, t := range doc.Traces {
		out = append(out, t.result())
	}
	return out, nil
}

type tempoResult struct {
	TraceID           string     `json:"traceID"`
	RootServiceName   string     `json:"rootServiceName"`
	RootTraceName     string     `json:"rootTraceName"`
	StartTimeUnixNano jsonNumber `json:"startTimeUnixNano"`
	DurationMs        jsonNumber `json:"durationMs"`
	SpanSet           *spanSet   `json:"spanSet"`
	SpanSets          []spanSet  `json:"spanSets"`
}

// spanSet is the spans of one trace the query selected. Only how many of them
// there were is read: the spans themselves are a fragment of a trace telescope
// is about to fetch whole, and drawing a row off a fragment would mean the list
// disagreeing with the gantt opened from it.
type spanSet struct {
	Matched jsonNumber `json:"matched"`
	Spans   []struct{} `json:"spans"`
}

func (s spanSet) count() int {
	if s.Matched > 0 {
		return int(s.Matched)
	}
	return len(s.Spans)
}

func (t tempoResult) result() Result {
	out := Result{
		TraceID: t.TraceID,
		Service: t.RootServiceName,
		Name:    t.RootTraceName,
	}
	if t.StartTimeUnixNano > 0 {
		out.Start = time.Unix(0, int64(t.StartTimeUnixNano)).UTC()
	}
	// Milliseconds, and a thousand thousand of them do not always fit where one
	// did: the multiplication wraps into a trace that ran backwards.
	if d := int64(t.DurationMs); d > 0 && d <= math.MaxInt64/int64(time.Millisecond) {
		out.Duration = time.Duration(d) * time.Millisecond
	}
	// A trace is reported once per span set that matched it, and the sets are
	// parts of the same answer about the same trace.
	if t.SpanSet != nil {
		out.Matched = t.SpanSet.count()
	}
	if len(t.SpanSets) > 0 {
		out.Matched = 0
		for _, s := range t.SpanSets {
			out.Matched += s.count()
		}
	}
	return out
}

// jsonNumber is an integer written either way. protojson spells a 64-bit field
// as a string and a 32-bit one as a number, so `startTimeUnixNano` arrives
// quoted from Tempo and bare from something that built the document by hand,
// and a decoder that insisted on one would refuse half the servers that speak
// this API.
type jsonNumber int64

func (n *jsonNumber) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// A float is not what the schema says, but a server that wrote one still
		// means the number: 1.7e18 truncated is a timestamp, and refusing the
		// whole response over it would lose every other trace in it.
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil || math.IsNaN(f) || math.IsInf(f, 0) ||
			f < math.MinInt64 || f > math.MaxInt64 {
			return errors.Wrapf(err, "read %q as a number", s)
		}
		v = int64(f)
	}
	*n = jsonNumber(v)
	return nil
}

// SortResults orders newest first, which is the order both backends answer in
// and the one a reader searching for what just happened is asking for. Ties
// keep the order the database gave them.
func SortResults(rs []Result) {
	slices.SortStableFunc(rs, func(a, b Result) int {
		return b.Start.Compare(a.Start)
	})
}
