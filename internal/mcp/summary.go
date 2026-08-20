package mcp

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/logs"
)

// How much a summary reads and how much of it it writes down.
//
// A summary is the answer to "what happened", and the answer to that is a
// shape rather than a list: fifty lines of an incident are a sample of it, and
// the counts under them are what says which fifty to ask for next.
const (
	summaryReach   = 2000
	summaryBuckets = 12
	summaryTop     = 5
	summaryByTop   = 10
)

const summaryDescription = "Counts what a place holds rather than listing it: " +
	"how many lines by level, how the volume moves over the window, the messages " +
	"that repeat most, and optionally the values of one field. It is the cheap " +
	"first question about an incident — ask it before logs, then use what it " +
	"names as the filter."

type summaryInput struct {
	Place  string `json:"place" jsonschema:"The name of a place or group that reads logs, as places reports it"`
	Filter string `json:"filter,omitempty" jsonschema:"Count only the lines this filter selects, in telescope's filter language"`
	Range  string `json:"range,omitempty" jsonschema:"The window, relative or absolute: 1h, today, yesterday, 6h..1h, 10:00..12:00, 2026-01-02 10:00..2026-01-02 12:00, or two RFC 3339 instants. Empty reads the place's own window, and all removes every bound"`
	By     string `json:"by,omitempty" jsonschema:"Also count the values of this field, as fields names it"`
}

type summaryOutput struct {
	Place  string `json:"place"`
	Window window `json:"window"`
	// Counted is how many lines the counts are over, and Covered whether that
	// was the whole window: a count over a sample is still a shape, but it is
	// not a total.
	Counted  int      `json:"counted"`
	Covered  bool     `json:"covers_window"`
	Levels   []tally  `json:"levels,omitempty"`
	Buckets  []bucket `json:"buckets,omitempty"`
	Every    string   `json:"bucket,omitempty" jsonschema:"How long one bucket is"`
	Messages []tally  `json:"top_messages,omitempty"`
	ByField  string   `json:"by_field,omitempty"`
	By       []tally  `json:"by,omitempty"`
	Note     string   `json:"note,omitempty"`
}

type tally struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type bucket struct {
	At     string `json:"at"`
	Count  int    `json:"count"`
	Errors int    `json:"errors,omitempty"`
}

func addSummary(s *sdk.Server, cfg config.Config) {
	addTool(s, "summary", summaryDescription, summaryHandler(cfg))
}

func summaryHandler(cfg config.Config) sdk.ToolHandlerFor[summaryInput, summaryOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in summaryInput) (*sdk.CallToolResult, summaryOutput, error) {
		src, err := resolveOver(cfg, in.Place, in.Range)
		if err != nil {
			return nil, summaryOutput{}, err
		}
		filter := logs.Filter{Query: in.Filter}.Compile()
		if err := filter.Err(); err != nil {
			return nil, summaryOutput{}, errors.Wrap(err, "filter")
		}
		if !src.CanPage() {
			return nil, summaryOutput{}, errors.Errorf(
				"%q cannot be counted: it is a command telescope can only follow", in.Place)
		}

		read, err := readBack(ctx, src.WithFilter(filter.Expr()), filter, summaryReach)
		if err != nil {
			return nil, summaryOutput{}, err
		}
		out := summarize(read, in.By)
		out.Place, out.Window = in.Place, windowOf(src.Range)
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: drawSummary(out)}},
		}, out, nil
	}
}

func summarize(read page, by string) summaryOutput {
	out := summaryOutput{
		Counted: len(read.matched),
		Covered: read.covered,
		ByField: by,
	}
	if len(read.matched) == 0 {
		out.Note = "nothing in the window matched"
		return out
	}
	if !read.covered {
		out.Note = "counted over the newest " + strconv.Itoa(read.read) +
			" lines of the window, which is as far as one call reads: the shape is this " +
			"stretch of it and the counts are not totals"
	}

	levels := map[zapcore.Level]int{}
	messages := map[string]int{}
	values := map[string]int{}
	for _, e := range read.matched {
		if e.Record.HasLevel {
			levels[e.Record.Level]++
		}
		if body := e.Record.Body; body != "" {
			messages[body]++
		}
		if by != "" {
			if v, ok := fieldsOf(e)[by]; ok {
				values[v]++
			}
		}
	}

	for level, n := range levels {
		out.Levels = append(out.Levels, tally{Name: level.CapitalString(), Count: n})
	}
	slices.SortFunc(out.Levels, func(a, b tally) int { return b.Count - a.Count })
	out.Messages = top(messages, summaryTop)
	out.By = top(values, summaryByTop)
	out.Buckets, out.Every = spread(read.matched)
	return out
}

// spread is how the volume moves across the window, which is the one question a
// count of the whole cannot answer: a thousand errors spread evenly is a broken
// dependency and a thousand in one bucket is an incident.
func spread(entries []*logs.Entry) ([]bucket, string) {
	first, last := entries[0].At, entries[len(entries)-1].At
	if first.IsZero() || !last.After(first) {
		return nil, ""
	}
	every := step(last.Sub(first))
	// Aligned to the clock rather than to the first line: a bucket starting at
	// 12:05 is a time somebody can look for elsewhere, and one starting at
	// 12:04:31.220 is an artifact of when this was asked.
	start := first.Truncate(every)

	buckets := make([]bucket, 0, summaryBuckets+1)
	for at := start; !at.After(last); at = at.Add(every) {
		buckets = append(buckets, bucket{At: at.Format(bucketStamp)})
	}
	for _, e := range entries {
		i := min(int(e.At.Sub(start)/every), len(buckets)-1)
		if i < 0 {
			continue
		}
		buckets[i].Count++
		if e.Record.HasLevel && e.Record.Level >= zapcore.ErrorLevel {
			buckets[i].Errors++
		}
	}
	return buckets, every.String()
}

// bucketSteps are the widths a bucket may have. A window is divided into a
// readable interval rather than into equal twelfths of itself: "every 5m" is a
// sentence, and "every 9.916666666s" is arithmetic nobody asked for.
var bucketSteps = []time.Duration{
	time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second,
	time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// step is the narrowest of those that covers the span in about as many buckets
// as a reader will look at.
func step(span time.Duration) time.Duration {
	for _, s := range bucketSteps {
		if span/s <= summaryBuckets {
			return s
		}
	}
	return bucketSteps[len(bucketSteps)-1]
}

// top is the n most common of what was counted, most first. A tie is broken by
// the name so that asking twice answers the same way.
func top(counts map[string]int, n int) []tally {
	if len(counts) == 0 {
		return nil
	}
	out := make([]tally, 0, len(counts))
	for name, count := range counts {
		out = append(out, tally{Name: name, Count: count})
	}
	slices.SortFunc(out, func(a, b tally) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
