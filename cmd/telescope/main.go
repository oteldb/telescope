// Command telescope is a terminal log viewer.
package main

import (
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oteldb/telescope/internal/trace"
	"github.com/oteldb/telescope/internal/ui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "telescope:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	model := ui.New()

	if len(args) >= 2 && args[0] == "trace" {
		t, err := readTrace(args[1])
		if err != nil {
			return err
		}
		model = ui.NewTrace(t)
		if args[1] == "-" {
			// The trace came in on stdin, so the keys have to come from
			// somewhere else.
			opts = append(opts, tea.WithInputTTY())
		}
	}

	_, err := tea.NewProgram(model, opts...).Run()
	return err
}

// readTrace reads a Jaeger query API response and returns the first trace in
// it. A response can carry several — that is what a search answers with — but
// a viewer opened on a file was pointed at one.
func readTrace(path string) (*trace.Tree, error) {
	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	found, err := trace.DecodeJaeger(data)
	if err != nil {
		return nil, err
	}
	return found[0], nil
}
