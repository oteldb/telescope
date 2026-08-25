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

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/config"
)

// Serve runs the server on stdio, which is how an agent starts one: it spawns
// telescope and speaks over the pipe rather than over a port.
func Serve(ctx context.Context, cfg config.Config, version string) error {
	return New(cfg, version).Run(ctx, &sdk.StdioTransport{})
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
	return s
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
