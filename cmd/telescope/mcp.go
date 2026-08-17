package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/mcp"
)

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the configured places to an agent over MCP",
		Long: "Speaks the Model Context Protocol on standard input and output, so an\n" +
			"agent can read what telescope reads: the places the config declares, what\n" +
			"their lines are labeled with, and what those labels have been seen saying.\n\n" +
			"Every tool names a place declared in the config and none of them takes a\n" +
			"command line, so an agent can reach what the file already said telescope may\n" +
			"run, and nothing else.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			// The pipe closing is how an agent ends a server it started, but a
			// server left running by hand is ended the way anything at a terminal
			// is, and the other commands here draw their own screen and take the
			// signal themselves.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return mcp.Serve(ctx, cfg, buildVersion())
		},
	}
}
