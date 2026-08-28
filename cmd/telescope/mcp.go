package main

import (
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/go-faster/errors"

	"github.com/spf13/cobra"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/mcp"
)

func mcpCmd() *cobra.Command {
	var logFile string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the configured places to an agent over MCP",
		Long: "Speaks the Model Context Protocol on standard input and output, so an\n" +
			"agent can read what telescope reads: the places the config declares, what\n" +
			"their lines are labeled with, and what those labels have been seen saying.\n\n" +
			"Every tool names a place declared in the config and none of them takes a\n" +
			"command line, so an agent can reach what the file already said telescope may\n" +
			"run, and nothing else.\n\n" +
			"--log-file writes every message either way to a file, which is how to\n" +
			"tell a tool that answered nothing from a client that did not draw the\n" +
			"answer: the two look the same from either end.",
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
			log, closeLog, err := openLog(logFile)
			if err != nil {
				return err
			}
			defer closeLog()

			// The pipe closing is how an agent ends a server it started, but a
			// server left running by hand is ended the way anything at a terminal
			// is, and the other commands here draw their own screen and take the
			// signal themselves.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return mcp.Serve(ctx, cfg, buildVersion(), log)
		},
	}
	cmd.Flags().StringVar(&logFile, "log-file", "",
		"write every MCP message to this file, for finding out what a client was actually sent")
	return cmd
}

// openLog opens the file the traffic is written to.
//
// Never standard output: that is the MCP session itself on this transport, and
// a log written into it would corrupt the very conversation it was opened to
// explain. Standard error is free, and is where a client usually keeps a
// server's own output.
// It returns a nil writer when there is nothing to write, which has to be a
// nil interface and not a nil file: a typed nil reads as present everywhere it
// is checked.
func openLog(path string) (io.Writer, func(), error) {
	nothing := func() {}
	switch strings.TrimSpace(path) {
	case "":
		return nil, nothing, nil
	case "-", "/dev/stdout", "stdout":
		return nil, nothing, errors.Errorf(
			"--log-file=%s writes into the MCP session itself, which is what stdio means here: "+
				"name a file, or stderr", path)
	case "stderr", "/dev/stderr":
		// Not closed: it is not ours, and the rest of the process still needs it.
		return os.Stderr, nothing, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nothing, errors.Wrap(err, "open log")
	}
	return f, func() { _ = f.Close() }, nil
}
