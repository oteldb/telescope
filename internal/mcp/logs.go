package mcp

import (
	"context"
	"strconv"
	"time"

	"github.com/go-faster/errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// How much of a place one call reads.
//
// The limit is what comes back and the reach is what is looked at to find it:
// a place that cannot be asked the filter answers with everything it has and
// the filter is applied here, so a narrow query over a busy stream is a lot of
// lines read for a few returned. Both are reported rather than assumed — a
// reader that cannot scroll has no other way to tell a quiet window from a
// window nobody finished reading.
const (
	defaultLogsLimit = 50
	maxLogsLimit     = 500
	logsReachFactor  = 20
	maxLogsReach     = 2000
)

const logsDescription = "Reads the lines of a place, newest last. " +
	"The filter is telescope's own: bare words match anywhere in a line, " +
	"key=value and key~regexp match one field, level>=warn compares severity, " +
	"and terms combine with and, or and not. " +
	"It is pushed down to a log database where it compiles and applied here " +
	"where it does not, so the answer is the same either way and only the amount " +
	"read differs. Labels every returned line shares are hoisted out of them and " +
	"reported once, and lines repeating the one before them are folded with a count."

type logsInput struct {
	Place  string `json:"place" jsonschema:"The name of a place or group that reads logs, as places reports it"`
	Target string `json:"target,omitempty" jsonschema:"What to read there, for a place that does not name one itself: a pod or workload for kubectl, a unit for journalctl, a container for docker. places says which places need one"`
	Filter string `json:"filter,omitempty" jsonschema:"The filter to read through, in telescope's filter language"`
	Range  string `json:"range,omitempty" jsonschema:"The window, relative or absolute: 1h, today, yesterday, 6h..1h, 10:00..12:00, 2026-01-02 10:00..2026-01-02 12:00, or two RFC 3339 instants. Empty reads the place's own window, and all removes every bound"`
	Limit  int    `json:"limit,omitempty" jsonschema:"How many lines to return, newest last. Defaults to 50 and is capped at 500"`
}

// logsOutput is what the answer is, rather than the answer: the lines
// themselves are the text of the result, since a model reads a rendered line
// for a third of what the same line costs as an object, and a second copy of
// them here would be paid for by whoever reads both.
type logsOutput struct {
	Place  string `json:"place"`
	Window window `json:"window"`
	// Read is how many lines the place answered with, Matched how many of them
	// passed the filter and Returned how many are in the text. They differ when
	// the place cannot be asked the filter itself.
	Read     int `json:"read"`
	Matched  int `json:"matched"`
	Returned int `json:"returned"`
	// Covered says the window was read to its far end. When it is false the
	// counts are about what was read and not about what is there.
	Covered bool `json:"covers_window"`
	// Pushed is the query each stream was actually sent, which is what read
	// and matched differing means: a place that answered the filter itself
	// returns only matches, and one that could not is filtered here after the
	// fact.
	Pushed []pushed          `json:"pushed,omitempty"`
	Common map[string]string `json:"common,omitempty" jsonschema:"Labels every returned line shares, written once instead of on each"`
	Varies []string          `json:"varies,omitempty" jsonschema:"The fields that tell the returned lines apart"`
	Note   string            `json:"note,omitempty" jsonschema:"What the answer leaves out, and what to ask instead"`
}

// pushed is one stream and the query the database was asked, empty for a
// source that answers no query of its own.
type pushed struct {
	Place string `json:"place"`
	Query string `json:"query,omitempty"`
}

type window struct {
	Since string `json:"since,omitempty"`
	Until string `json:"until,omitempty"`
}

func addLogs(s *sdk.Server, cfg config.Config) {
	addTool(s, "logs", logsDescription, logsHandler(cfg))
}

func logsHandler(cfg config.Config) sdk.ToolHandlerFor[logsInput, logsOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in logsInput) (*sdk.CallToolResult, logsOutput, error) {
		src, err := resolveOver(cfg, in.Place, in.Target, in.Range)
		if err != nil {
			return nil, logsOutput{}, err
		}
		filter := logs.Filter{Query: in.Filter}.Compile()
		if err := filter.Err(); err != nil {
			return nil, logsOutput{}, errors.Wrap(err, "filter")
		}
		if !src.CanPage() {
			// A free-form command is somebody else's line with nowhere to put a
			// bound in it: what it has written is gone and what it writes next is
			// all there is, which is a stream to watch and not a question to ask.
			return nil, logsOutput{}, errors.Errorf(
				"%q cannot be read on request: it is a command telescope can only follow", in.Place)
		}

		limit, capped, err := capLimit(in.Limit, defaultLogsLimit, maxLogsLimit)
		if err != nil {
			return nil, logsOutput{}, err
		}
		asked := src.WithFilter(filter.Expr())
		read, err := readBack(ctx, asked, filter, limit)
		if err != nil {
			return nil, logsOutput{}, err
		}

		out := logsOutput{
			Place:    in.Place,
			Window:   windowOf(src.Range),
			Read:     read.read,
			Matched:  len(read.matched),
			Returned: min(len(read.matched), limit),
			Covered:  read.covered,
			Pushed:   pushedTo(asked),
		}
		shown := read.matched
		if len(shown) > limit {
			shown = shown[len(shown)-limit:]
		}
		rows := fold(shown)
		out.Common, out.Varies = split(shown)
		out.Note = join(capped, logsNote(out, limit))
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: draw(out, rows)}},
		}, out, nil
	}
}

// page is what one read of a place came back with.
type page struct {
	matched []*logs.Entry
	read    int
	covered bool
}

// readBack walks back from the end of the window until it has enough matches,
// which is what the list does when a reader scrolls into what has not been
// read yet.
//
// One page would do where the place answers the filter itself. Where it does
// not, one page is however many lines the place happened to write last, of
// which a narrow filter keeps none — and answering "nothing matched" after
// reading a minute of a busy stream would be a lie told confidently.
func readBack(ctx context.Context, src source.Config, filter logs.Filter, limit int) (page, error) {
	reach := min(max(limit*logsReachFactor, limit), maxLogsReach)
	var (
		store = logs.NewStore(reach)
		view  = logs.NewView(filter)
		out   = page{}
		// The window's end, or now: a place with no end reads up to the moment
		// it is asked.
		before = src.Range.Until
	)
	if before.IsZero() {
		before = time.Now()
	}
	for out.read < reach {
		lines, err := src.Page(ctx, before, min(limit, reach-out.read), source.WithTimeFunc(logs.LineTime))
		if err != nil {
			return page{}, err
		}
		if len(lines) == 0 {
			out.covered = true
			break
		}
		out.read += len(lines)
		store.Prepend(lines)
		out.matched = view.Entries(store)
		if len(out.matched) >= limit {
			break
		}
		// The next page ends where this one began. A line nothing dated cannot
		// say where that is, and asking again from the same instant would read
		// the same lines forever.
		at := logs.LineTime(lines[0])
		if !at.Before(before) {
			break
		}
		before = at
	}
	return out, nil
}

func windowOf(r source.Range) window {
	var w window
	if !r.Since.IsZero() {
		w.Since = r.Since.Format(time.RFC3339)
	}
	if !r.Until.IsZero() {
		w.Until = r.Until.Format(time.RFC3339)
	}
	return w
}

// logsNote says what the answer leaves out. An answer cut by the limit, one cut
// by how far a single call reads and one that is everything there was all look
// the same from the outside, and only the last of them means stop asking.
func logsNote(out logsOutput, limit int) string {
	switch {
	case out.Matched > out.Returned:
		return "the newest " + strconv.Itoa(out.Returned) + " of " +
			strconv.Itoa(out.Matched) + " matches are here; narrow the filter or ask for more"
	case out.Covered:
		if out.Returned == 0 {
			return "nothing in the window matched"
		}
		return ""
	case out.Returned >= limit:
		return "this is the whole of the limit, and the window holds more before them: " +
			"ask again over an earlier window, as in 6h..1h"
	default:
		return "the read stopped " + strconv.Itoa(out.Read) +
			" lines back, which is as far as one call looks; anything older than the first " +
			"line here was not read. Ask again over an earlier window to go on"
	}
}

// pushedTo is what each stream was asked, named as the streams are named.
//
// A filter telescope could not compile into the database's own language is not
// a narrower answer, it is the same answer read here instead — which is the
// whole of the difference between the lines read and the lines matched, and
// the one thing a caller cannot work out from the counts alone.
func pushedTo(src source.Config) []pushed {
	var (
		queries = src.Pushed()
		names   = src.Labels()
		out     = make([]pushed, 0, len(queries))
	)
	for i, q := range queries {
		name := src.Name
		if i < len(names) {
			name = names[i]
		}
		out = append(out, pushed{Place: name, Query: q})
	}
	return out
}
