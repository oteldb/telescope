// Package mcp serves what the config declares to an agent, as tools it can
// call rather than a screen it cannot read.
//
// It is a second consumer of config and source, beside ui: nothing here draws,
// and nothing that draws asks here. A tool names a place the file already
// declared and never a command of its own, which is the guarantee the start
// screen gives a person — an agent that can name a command can run anything the
// user can, and telescope reaches other machines over ssh.
package mcp

import (
	"context"
	"io"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/config"
)

// Serve runs the server on stdio, which is how an agent starts one: it spawns
// telescope and speaks over the pipe rather than over a port.
//
// When log is not nil, every message either way is written to it. What a client
// does with an answer is not visible from here — a tool that returned its lines
// and a client that did not draw them look the same from the server, and from
// the person being told the lines are missing — so the traffic itself is the
// only thing that tells the two apart.
func Serve(ctx context.Context, cfg config.Config, version string, log io.Writer) error {
	var transport sdk.Transport = &sdk.StdioTransport{}
	if log != nil {
		transport = &sdk.LoggingTransport{Transport: transport, Writer: log}
	}
	return New(cfg, version).Run(ctx, transport)
}

// New builds the server over one config, read once: a place is resolved when
// the file is read, and a token that cost a keyring prompt is not asked for
// again per tool call.
func New(cfg config.Config, version string) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{
		Name:        "telescope",
		Title:       "telescope",
		Description: "Reads the logs and traces of the places telescope is configured for.",
		Version:     version,
	}, nil)
	addPlaces(s, cfg)
	addFields(s, cfg)
	addLogs(s, cfg)
	addSummary(s, cfg)
	addTraces(s, cfg)
	addTraceSearch(s, cfg)
	addTraceFields(s, cfg)
	addLink(s, cfg)
	return s
}

// textOnly registers a tool that answers with its text and no structured
// content at all.
//
// It is for the two tools whose structured half cannot stand on its own. Every
// other tool here carries its whole answer in both — places has the places,
// summary has the tallies, trace_search has the traces — so a client rendering
// either one is served. logs and trace are the exceptions: the lines and the
// tree are the answer, and the facts beside them are counts about it. A reader
// that took the structured half of those got how many lines matched and not one
// of them.
//
// The reason that is fatal rather than untidy is that a client picks a half
// rather than merging them. Given two, some pick the one without the payload;
// given one, there is nothing to pick wrong. So these two answer with the text,
// which is where every count already appears as well.
func textOnly[In, Out any](h sdk.ToolHandlerFor[In, Out]) sdk.ToolHandlerFor[In, any] {
	return func(ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
		res, _, err := h(ctx, req, in)
		return res, nil, err
	}
}

// addTool registers one tool. Every tool here reads, so the hint is not a
// parameter: the day one of them writes it will be the day it takes one.
func addTool[In, Out any](s *sdk.Server, name, description string, h sdk.ToolHandlerFor[In, Out]) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        name,
		Description: description,
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, h)
}
