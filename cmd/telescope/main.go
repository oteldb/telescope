// Command telescope is a terminal log viewer.
package main

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
	"github.com/oteldb/telescope/internal/trace"
	"github.com/oteldb/telescope/internal/ui"
)

// fetchTimeout bounds a trace fetch. A trace is one request with an end, unlike
// the streams telescope otherwise opens, so it gets a deadline rather than
// living for as long as the view.
const fetchTimeout = 30 * time.Second

func main() {
	if err := root().Execute(); err != nil {
		os.Exit(1)
	}
}

func root() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telescope",
		Short: "A terminal log viewer",
		Long: "Opens one stream — a systemd unit, a Kubernetes workload, a container,\n" +
			"any command, or a query against VictoriaLogs or Loki — and renders it as a\n" +
			"filterable list.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(*cobra.Command, []string) error {
			_, err := tea.NewProgram(ui.New(), tea.WithAltScreen()).Run()
			return err
		},
	}
	cmd.AddCommand(traceCmd())
	return cmd
}

func traceCmd() *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "trace [flags] <trace-id | file | ->",
		Short: "Draw one trace as a gantt",
		Long: "Reads a trace and draws it as a gantt: the spans in the order they were\n" +
			"called, how long each took, and when it ran against the request as a whole.\n\n" +
			"With --from, the argument is a trace id to fetch. Without it, the argument\n" +
			"is a file holding a response already, or - to read one on standard input.\n" +
			"Tempo's OTLP and Jaeger's query API are both understood, in either encoding.",
		Example: "  telescope trace --from https://tempo.example.com 4bf92f3577b34da6a3ce929d0e0e4736\n" +
			"  telescope trace --from prod 4bf92f3577b34da6a3ce929d0e0e4736\n" +
			"  telescope trace ./saved.json\n" +
			"  curl -s \"$TEMPO/api/traces/$ID\" | telescope trace -",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			tree, err := readTrace(cmd.Context(), from, target)
			if err != nil {
				return err
			}
			opts := []tea.ProgramOption{tea.WithAltScreen()}
			if target == "-" {
				// The trace came in on standard input, so the keys have to come
				// from somewhere else.
				opts = append(opts, tea.WithInputTTY())
			}
			_, err = tea.NewProgram(ui.NewTrace(tree), opts...).Run()
			return err
		},
	}
	cmd.Flags().StringVar(&from, "from", "",
		"a Tempo url, or the name of a place in the config that declares one")
	return cmd
}

// readTrace reads a trace from wherever it was named: an endpoint, if one was,
// and a file or standard input otherwise.
func readTrace(ctx context.Context, from, target string) (*trace.Tree, error) {
	if strings.TrimSpace(from) != "" {
		return fetchTrace(ctx, from, target)
	}
	data, err := readFile(target)
	if err != nil {
		return nil, err
	}
	return decodeTrace(data)
}

func readFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func fetchTrace(ctx context.Context, from, id string) (*trace.Tree, error) {
	endpoint, err := traceEndpoint(from)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	data, err := endpoint.Trace(ctx, id)
	if err != nil {
		return nil, err
	}
	return decodeTrace(data)
}

// traceEndpoint resolves what --from named. A URL is itself; anything else is
// the name of a place, so that a Tempo behind a token is declared once and
// referred to by the name of the system it belongs to.
func traceEndpoint(from string) (source.Endpoint, error) {
	from = strings.TrimSpace(from)
	if strings.HasPrefix(from, "http://") || strings.HasPrefix(from, "https://") {
		return source.Endpoint{URL: strings.TrimRight(from, "/")}, nil
	}

	cfg, err := config.Load()
	if err != nil {
		return source.Endpoint{}, err
	}
	var named []string
	for _, p := range cfg.Places {
		if p.Name != from {
			if _, ok, _ := p.TraceEndpoint(); ok {
				named = append(named, p.Name)
			}
			continue
		}
		endpoint, ok, err := p.TraceEndpoint()
		if err != nil {
			return source.Endpoint{}, err
		}
		if !ok {
			return source.Endpoint{}, errors.Errorf("place %q reads no traces: give it a traces: url", from)
		}
		return endpoint, nil
	}
	if len(named) > 0 {
		return source.Endpoint{}, errors.Errorf(
			"no place named %q: the ones that read traces are %s", from, strings.Join(named, ", "))
	}
	return source.Endpoint{}, errors.Errorf("no place named %q, and it is not a url either", from)
}

// decodeTrace reads whichever of the two formats arrived.
//
// Neither is asked for by name: a file has no content type, and somebody who
// has a trace should not have to know which database wrote it.
//
// What decides is whether spans came out, not whether the decode returned an
// error, and that is the whole subtlety here. A Jaeger response is JSON with a
// "data" key, so the OTLP decoder reads it as a well-formed payload describing
// no spans at all and reports no error — it is only unknown fields, and unknown
// fields are how OTLP stays forwards-compatible. Dispatching on the error would
// mean a Jaeger file opened as an empty trace.
func decodeTrace(data []byte) (*trace.Tree, error) {
	otlp, otlpErr := trace.DecodeOTLP(data)
	if tree, ok := firstWithSpans(otlp); ok {
		return tree, nil
	}
	jaeger, jaegerErr := trace.DecodeJaeger(data)
	if tree, ok := firstWithSpans(jaeger); ok {
		return tree, nil
	}
	// Both failed to find anything. The OTLP complaint is the one to report,
	// since it is the format an endpoint answers with.
	if otlpErr != nil {
		return nil, otlpErr
	}
	if jaegerErr != nil {
		return nil, jaegerErr
	}
	return nil, errors.New("no spans in that trace")
}

func firstWithSpans(found []*trace.Tree) (*trace.Tree, bool) {
	for _, t := range found {
		if t.Len() > 0 {
			return t, true
		}
	}
	return nil, false
}
