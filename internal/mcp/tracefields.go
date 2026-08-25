package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

const traceFieldsDescription = "Says what a trace store can be searched by: the " +
	"services that report to it, the operations one of them was called, and the " +
	"tag keys its spans carry. A jaeger store indexes per service and refuses a " +
	"search that does not name one, so this is what to ask first."

const traceValuesDescription = "Lists the values one tag key has been seen with " +
	"at a trace store, which is how a search is narrowed before anything is read."

type traceFieldsInput struct {
	Place   string `json:"place,omitempty" jsonschema:"Which store, as places reports it. May be left out when the config declares a single store"`
	Service string `json:"service,omitempty" jsonschema:"Also list the operations this service was called, which is otherwise left out: there is one list per service and no useful union of them"`
}

type traceFieldsOutput struct {
	Store      string   `json:"store"`
	Services   []string `json:"services"`
	Operations []string `json:"operations,omitempty" jsonschema:"The operations of the named service, if one was named"`
	Tags       []string `json:"tags"`
	Note       string   `json:"note,omitempty"`
}

type traceValuesInput struct {
	Place string `json:"place,omitempty" jsonschema:"Which store, as places reports it. May be left out when the config declares a single store"`
	Tag   string `json:"tag" jsonschema:"The tag key to list, as trace_fields names it"`
}

type traceValuesOutput struct {
	Store  string   `json:"store"`
	Tag    string   `json:"tag"`
	Values []string `json:"values"`
	Note   string   `json:"note,omitempty"`
}

func addTraceFields(s *sdk.Server, cfg config.Config) {
	addTool(s, "trace_fields", traceFieldsDescription, traceFieldsHandler(cfg))
	addTool(s, "trace_tag_values", traceValuesDescription, traceValuesHandler(cfg))
}

func traceFieldsHandler(cfg config.Config) sdk.ToolHandlerFor[traceFieldsInput, traceFieldsOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in traceFieldsInput) (*sdk.CallToolResult, traceFieldsOutput, error) {
		at, err := traceStore(cfg, in.Place)
		if err != nil {
			return nil, traceFieldsOutput{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, traceFetchTimeout)
		defer cancel()

		out := traceFieldsOutput{Store: at.URL, Services: []string{}, Tags: []string{}}

		// A store that answers one listing and not the other is still worth what
		// it answered: an agent holding the services can search a Jaeger, which
		// is the listing that stands between it and any answer at all.
		var asked, missed []string
		ask := func(what string, list func() ([]string, error)) []string {
			asked = append(asked, what)
			got, err := list()
			if err != nil {
				missed = append(missed, what+" ("+err.Error()+")")
				return nil
			}
			return got
		}

		if got := ask("services", func() ([]string, error) { return at.TraceServices(ctx) }); got != nil {
			out.Services = got
		}
		if service := strings.TrimSpace(in.Service); service != "" {
			out.Operations = ask("operations", func() ([]string, error) {
				return at.TraceOperations(ctx, service)
			})
		}
		// Jaeger's API has no tag-name endpoint at all, so there is nothing to
		// ask and nothing that failed. Saying "none" would be the wrong answer
		// to a question nobody put: the spans carry tags and a search can narrow
		// by them, they just cannot be enumerated from here.
		if at.Collector == source.CollectorJaeger {
			out.Note = join(out.Note, jaegerNoTags)
		} else if got := ask("tag keys", func() ([]string, error) { return at.TraceTagKeys(ctx) }); got != nil {
			out.Tags = got
		}

		if len(missed) > 0 {
			out.Note = join(out.Note, "the store did not answer for "+strings.Join(missed, ", "))
		}
		if len(missed) == len(asked) {
			return nil, traceFieldsOutput{}, errors.Errorf(
				"%s answered nothing it was asked: %s", at.URL, strings.Join(missed, "; "))
		}
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: drawTraceFields(out, in.Service)}},
		}, out, nil
	}
}

func traceValuesHandler(cfg config.Config) sdk.ToolHandlerFor[traceValuesInput, traceValuesOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in traceValuesInput) (*sdk.CallToolResult, traceValuesOutput, error) {
		tag := strings.TrimSpace(in.Tag)
		if tag == "" {
			return nil, traceValuesOutput{}, errors.New(
				"name a tag: trace_fields lists the ones a store indexes")
		}
		at, err := traceStore(cfg, in.Place)
		if err != nil {
			return nil, traceValuesOutput{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, traceFetchTimeout)
		defer cancel()

		out := traceValuesOutput{Store: at.URL, Tag: tag, Values: []string{}}
		if at.Collector == source.CollectorJaeger {
			// As in trace_fields: nothing was asked, so nothing came back, and
			// an empty list on its own would answer a question nobody put.
			out.Note = jaegerNoTags
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: drawTraceValues(out)}},
			}, out, nil
		}
		values, err := at.TraceTagValues(ctx, tag)
		if err != nil {
			return nil, traceValuesOutput{}, err
		}
		if values != nil {
			out.Values = values
		}
		if len(out.Values) == 0 {
			out.Note = "the store indexes no values under " + tag +
				", which is either a key nothing carries or one it does not index"
		}
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: drawTraceValues(out)}},
		}, out, nil
	}
}

// jaegerNoTags is what a Jaeger store has to say about its tags, which is not
// that it has none.
const jaegerNoTags = "this store speaks the jaeger API, which has no listing of " +
	"tag names or their values: its spans carry tags and trace_search can narrow " +
	"by them, they just cannot be enumerated from here"

func drawTraceFields(out traceFieldsOutput, service string) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "%s\n", out.Store)
	writeList(b, "services", out.Services)
	if strings.TrimSpace(service) != "" {
		writeList(b, "operations of "+plain(service), out.Operations)
	}
	if out.Note != jaegerNoTags && !strings.HasPrefix(out.Note, jaegerNoTags) {
		writeList(b, "tags", out.Tags)
	}
	if out.Note != "" {
		fmt.Fprintf(b, "\nnote: %s\n", out.Note)
	}
	return b.String()
}

func drawTraceValues(out traceValuesOutput) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "%s\n", out.Store)
	writeList(b, plain(out.Tag), out.Values)
	if out.Note != "" {
		fmt.Fprintf(b, "\nnote: %s\n", out.Note)
	}
	return b.String()
}

// writeList writes one listing as a wrapped run of names rather than a column.
// A hundred service names down the left is a hundred lines of mostly margin,
// and none of them is being pointed at by anything else.
func writeList(b *strings.Builder, title string, items []string) {
	fmt.Fprintf(b, "\n%s (%d)", title, len(items))
	if len(items) == 0 {
		b.WriteString(": none\n")
		return
	}
	b.WriteString(":\n")
	line := "  "
	for _, it := range items {
		it = plain(it)
		if len(line)+len(it) > listWrap && line != "  " {
			b.WriteString(line + "\n")
			line = "  "
		}
		line += it + "  "
	}
	b.WriteString(strings.TrimRight(line, " ") + "\n")
}

// listWrap is how wide a wrapped listing runs. Nothing here is aligned against
// anything, so the width is only about not writing one endless line.
const listWrap = 78
