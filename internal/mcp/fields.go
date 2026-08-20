package mcp

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-faster/errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

const fieldsDescription = "Names the fields a place's lines are labeled with, " +
	"which is what a filter can be written against. Only a log database answers: " +
	"a collector is a process writing to a pipe and knows nothing about its own " +
	"output until some of it has been read."

const valuesDescription = "Lists the values one field has been seen with at a " +
	"place, which is how a filter is narrowed before anything is read. Only a log " +
	"database answers, and it answers for the window asked about."

var errNoField = errors.New("name a field: fields lists the ones a place indexes")

type fieldsInput struct {
	Place string `json:"place" jsonschema:"The name of a place or group, as places reports it"`
	Range string `json:"range,omitempty" jsonschema:"The window, relative or absolute: 1h, today, yesterday, 6h..1h, 10:00..12:00, 2026-01-02 10:00..2026-01-02 12:00, or two RFC 3339 instants. Empty reads the place's own window, and all removes every bound"`
}

type fieldsOutput struct {
	Fields []string `json:"fields"`
	Note   string   `json:"note,omitempty" jsonschema:"What was not asked, or what the answer leaves out"`
}

type valuesInput struct {
	Place string `json:"place" jsonschema:"The name of a place or group, as places reports it"`
	Field string `json:"field" jsonschema:"The field to list, as fields names it"`
	Range string `json:"range,omitempty" jsonschema:"The window, relative or absolute: 1h, today, yesterday, 6h..1h, 10:00..12:00, 2026-01-02 10:00..2026-01-02 12:00, or two RFC 3339 instants. Empty reads the place's own window, and all removes every bound"`
}

type valuesOutput struct {
	Values []string `json:"values"`
	Note   string   `json:"note,omitempty" jsonschema:"What was not asked, or what the answer leaves out"`
}

func addFields(s *sdk.Server, cfg config.Config) {
	addTool(s, "fields", fieldsDescription, fieldsHandler(cfg))
	addTool(s, "field_values", valuesDescription, valuesHandler(cfg))
}

func fieldsHandler(cfg config.Config) sdk.ToolHandlerFor[fieldsInput, fieldsOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in fieldsInput) (*sdk.CallToolResult, fieldsOutput, error) {
		src, err := resolveOver(cfg, in.Place, in.Range)
		if err != nil {
			return nil, fieldsOutput{}, err
		}
		asked, silent := answering(src)
		out := fieldsOutput{Fields: []string{}, Note: indexNote(asked, silent)}
		if len(asked) == 0 {
			return nil, out, nil
		}
		if out.Fields, err = src.FieldNames(ctx); err != nil {
			return nil, fieldsOutput{}, err
		}
		return nil, out, nil
	}
}

func valuesHandler(cfg config.Config) sdk.ToolHandlerFor[valuesInput, valuesOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in valuesInput) (*sdk.CallToolResult, valuesOutput, error) {
		if strings.TrimSpace(in.Field) == "" {
			return nil, valuesOutput{}, errNoField
		}
		src, err := resolveOver(cfg, in.Place, in.Range)
		if err != nil {
			return nil, valuesOutput{}, err
		}
		asked, silent := answering(src)
		out := valuesOutput{Values: []string{}, Note: indexNote(asked, silent)}
		if len(asked) == 0 {
			return nil, out, nil
		}
		if out.Values, err = src.FieldValues(ctx, in.Field); err != nil {
			return nil, valuesOutput{}, err
		}
		if len(out.Values) >= source.FieldValuesLimit {
			out.Note = join(out.Note, "the store was asked for "+
				strconv.Itoa(source.FieldValuesLimit)+" values and returned that many, so there are more")
		}
		return nil, out, nil
	}
}

func resolveOver(cfg config.Config, place, spec string) (source.Config, error) {
	src, err := stream(cfg, place)
	if err != nil {
		return source.Config{}, err
	}
	return withRange(src, spec)
}

// answering splits a place into the streams a listing can be asked of and the
// ones it cannot, which for a group is both at once: a merge of a database and
// a container answers for half of itself, and the half it does not answer for
// is the half whose fields are missing from the reply.
func answering(src source.Config) (asked, silent []string) {
	streams := []source.Config{src}
	if src.Collector == source.CollectorMerge {
		streams = src.Children()
	}
	for _, sub := range streams {
		if sub.Collector.IsRemoteAPI() {
			asked = append(asked, listingName(sub))
		} else {
			silent = append(silent, listingName(sub))
		}
	}
	return asked, silent
}

func listingName(src source.Config) string {
	if src.Name != "" {
		return src.Name
	}
	return string(src.Collector)
}

// indexNote says what was left out, since an empty list and a list that nobody
// was able to answer for read the same.
func indexNote(asked, silent []string) string {
	if len(silent) == 0 {
		return ""
	}
	writes := strings.Join(silent, ", ") +
		" writes lines rather than indexing them, so what one is labeled with is only known once it has been read"
	if len(asked) == 0 {
		return writes
	}
	return "only " + strings.Join(asked, ", ") + " was asked: " + writes
}

func join(note, more string) string {
	if note == "" {
		return more
	}
	return note + "; " + more
}
