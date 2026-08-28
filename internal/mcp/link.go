package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/go-faster/errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
	"github.com/oteldb/telescope/internal/view"
)

const linkDescription = "Writes the command that opens a view of what you have " +
	"been reading, for a person to run. Use it to point somebody at what you " +
	"found rather than describing it: the same place, the same filter and the " +
	"same window, on their screen and scrollable. It returns the command and " +
	"runs nothing."

type linkInput struct {
	Place string `json:"place,omitempty" jsonschema:"The place, group or trace store to open, as places reports it"`
	Query string `json:"query,omitempty" jsonschema:"The filter to open on, in the same language the logs tool takes. Say level>=error here rather than asking for a level toggle"`
	Range string `json:"range,omitempty" jsonschema:"The window to open on: 1h, today, 6h..1h, 10:00..12:00, or two RFC 3339 instants"`
	Trace string `json:"trace,omitempty" jsonschema:"A trace id to draw instead of a list. With a store and no id, opens that store's trace search"`
}

type linkOutput struct {
	Link  string `json:"link" jsonschema:"The command that opens the view"`
	Opens string `json:"opens" jsonschema:"What it opens: logs, trace or search"`
	Note  string `json:"note,omitempty"`
}

func addLink(s *sdk.Server, cfg config.Config) {
	addTool(s, "link", linkDescription, linkHandler(cfg))
}

func linkHandler(cfg config.Config) sdk.ToolHandlerFor[linkInput, linkOutput] {
	return func(_ context.Context, _ *sdk.CallToolRequest, in linkInput) (*sdk.CallToolResult, linkOutput, error) {
		v, err := linkView(cfg, in)
		if err != nil {
			return nil, linkOutput{}, err
		}
		out := linkOutput{Link: v.Link(), Opens: opensSaid(v.Kind), Note: linkNote(in, v)}

		text := out.Link
		if out.Note != "" {
			text += "\n\nnote: " + out.Note
		}
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: text}},
		}, out, nil
	}
}

// linkView works out which screen was meant and checks that it can be opened.
//
// A link is checked here rather than left to fail when it is run, because the
// person running it did not write it: a command that opens nothing is worse
// than a refusal, since they have no way to tell a name that was wrong from a
// window that was quiet.
func linkView(cfg config.Config, in linkInput) (view.View, error) {
	place := strings.TrimSpace(in.Place)
	trace := strings.TrimSpace(in.Trace)

	if trace != "" || readsOnlyTraces(cfg, place) {
		at, err := traceStore(cfg, place)
		if err != nil {
			return view.View{}, err
		}
		if place == "" {
			// The store was found by there being only one; a link has to name
			// it, since it is run somewhere this tool is not.
			if place = storeNamed(cfg, at); place == "" {
				return view.View{}, errors.New(
					"name the store to link to: places says which read traces")
			}
		}
		kind := view.KindSearch
		if trace != "" {
			kind = view.KindTrace
		}
		return view.View{Kind: kind, Place: place, Trace: trace}, nil
	}

	if place == "" {
		return view.View{}, errors.Errorf("name a place to open: %s", declared(cfg))
	}
	// Resolved for the same reason: what cannot be opened here cannot be opened
	// there either, and the refusal is worth more now than later.
	if _, err := stream(cfg, place); err != nil {
		return view.View{}, err
	}
	if q := strings.TrimSpace(in.Query); q != "" {
		if err := (logs.Filter{Query: q}).Compile().Err(); err != nil {
			return view.View{}, errors.Wrap(err, "query")
		}
	}
	if r := strings.TrimSpace(in.Range); r != "" {
		if _, err := source.ParseRange(r, time.Now()); err != nil {
			return view.View{}, errors.Wrap(err, "range")
		}
	}
	return view.View{
		Kind:  view.KindLogs,
		Place: place,
		Query: strings.TrimSpace(in.Query),
		Range: strings.TrimSpace(in.Range),
	}, nil
}

// readsOnlyTraces reports whether the name is a store, so that naming one and
// no trace id means its search rather than a list it does not have.
func readsOnlyTraces(cfg config.Config, name string) bool {
	if name == "" {
		return false
	}
	for _, p := range cfg.Places {
		if p.Name == name {
			return p.ReadsTraces()
		}
	}
	return false
}

// storeNamed is the declared name of the store at an endpoint, since a link has
// to say a name and not a URL: a URL in a link would carry a token past the
// config that was keeping it.
func storeNamed(cfg config.Config, at source.Endpoint) string {
	for _, p := range cfg.Places {
		if !p.ReadsTraces() {
			continue
		}
		if e, ok, err := p.TraceEndpoint(); ok && err == nil && e.URL == at.URL {
			return p.Name
		}
	}
	return ""
}

func opensSaid(k view.Kind) string {
	switch k {
	case view.KindTrace:
		return "trace"
	case view.KindSearch:
		return "search"
	default:
		return "logs"
	}
}

// linkNote says what the link will not carry, since a view opened without it
// would otherwise look like the link having been written wrong.
func linkNote(in linkInput, v view.View) string {
	var note string
	if v.Kind != view.KindLogs {
		if strings.TrimSpace(in.Query) != "" || strings.TrimSpace(in.Range) != "" {
			note = join(note, "a trace is one request and carries its own interval, "+
				"so the query and the window were left off")
		}
		return note
	}
	if v.Range == "" {
		note = join(note, "no window was given, so it opens on the place's own and follows")
	}
	return note
}
