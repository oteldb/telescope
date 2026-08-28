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
	"github.com/oteldb/telescope/internal/view"
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
	var query, timeRange string
	cmd := &cobra.Command{
		Use:   "telescope [place]",
		Short: "A terminal log viewer",
		Long: "Opens one stream — a systemd unit, a Kubernetes workload, a container,\n" +
			"any command, or a query against VictoriaLogs or Loki — and renders it as a\n" +
			"filterable list.\n\n" +
			"With no argument it opens the start screen, which picks a place. Naming one\n" +
			"the config declares opens its list directly, which is what a link written by\n" +
			"telescope mcp is.",
		Example: "  telescope\n" +
			"  telescope prod\n" +
			"  telescope prod --query 'level>=error pod=api-*' --range 6h..1h",
		Version:       buildVersion(),
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(_ *cobra.Command, args []string) error {
			model := ui.New()
			if len(args) > 0 {
				var err error
				if model, err = openPlace(args[0], query, timeRange); err != nil {
					return err
				}
			}
			_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
			return err
		},
	}
	cmd.Flags().StringVarP(&query, view.FlagQuery, "q", "",
		"the filter to open on, in the language the / prompt takes")
	cmd.Flags().StringVar(&timeRange, view.FlagRange, "",
		"the window to read: 1h, today, 6h..1h, or two RFC 3339 instants")
	// cliversion writes the word "version" itself, and cobra's default template
	// writes it too.
	cmd.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	cmd.AddCommand(traceCmd(), schemaCmd(), initCmd(), mcpCmd())
	return cmd
}

func schemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema of the config file",
		Long: "Writes the JSON Schema every key of the config file is declared by, which\n" +
			"is what an editor completes and checks one against. It is published at\n" +
			config.SchemaURL + ", so a file can point at it\n" +
			"with a $schema key rather than keeping a copy.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := config.Schema()
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func traceCmd() *cobra.Command {
	var from, api string
	cmd := &cobra.Command{
		Use:   "trace [flags] [trace-id | file | -]",
		Short: "Draw one trace as a gantt, or search for one",
		Long: "Reads a trace and draws it as a gantt: the spans in the order they were\n" +
			"called, how long each took, and when it ran against the request as a whole.\n\n" +
			"With --from, the argument is a trace id to fetch, and no argument opens a\n" +
			"search of that store instead. Without --from, the argument is a file holding\n" +
			"a response already, or - to read one on standard input. Tempo's OTLP and\n" +
			"Jaeger's query API are both understood, in either encoding.",
		Example: "  telescope trace --from https://tempo.example.com 4bf92f3577b34da6a3ce929d0e0e4736\n" +
			"  telescope trace --from prod 4bf92f3577b34da6a3ce929d0e0e4736\n" +
			"  telescope trace --from prod\n" +
			"  telescope trace ./saved.json\n" +
			"  curl -s \"$TEMPO/api/traces/$ID\" | telescope trace -",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A store and no id is somebody who does not know the id yet, which
			// is the ordinary case: an id is copied off a log line, and a search
			// is how one is found when there is no line to copy it from.
			if len(args) == 0 {
				if strings.TrimSpace(from) == "" {
					return errors.New(
						"name a trace to read, or a place to search with --from")
				}
				endpoint, err := traceEndpoint(from)
				if err != nil {
					return err
				}
				if endpoint, err = withAPI(endpoint, api); err != nil {
					return err
				}
				_, err = tea.NewProgram(ui.NewSearch(endpoint), tea.WithAltScreen()).Run()
				return err
			}

			target := args[0]
			tree, err := readTrace(cmd.Context(), from, api, target)
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
		"a trace store url, or the name of a place that reads traces or names one")
	cmd.Flags().StringVar(&api, "api", "",
		"what answers there: tempo or jaeger. A named place declares its own")
	return cmd
}

// openPlace resolves a name the config declares into the list it opens.
//
// Only a declared place, never a command: what is typed here is as likely to
// have been pasted out of a chat window as typed by whoever is running it, and
// a name that could be a command line would run it.
func openPlace(name, query, timeRange string) (ui.Model, error) {
	cfg, err := config.Load()
	if err != nil {
		return ui.Model{}, err
	}
	src, ready, err := placeStream(cfg, name)
	if err != nil {
		return ui.Model{}, err
	}
	if timeRange != "" {
		r, err := source.ParseRange(timeRange, time.Now())
		if err != nil {
			return ui.Model{}, err
		}
		src.Range = r
		for i := range src.Merge {
			src.Merge[i].Range = r
		}
	}
	if !ready {
		// The place needs something the config did not give it — a pod, a
		// container — and the start screen is the thing that asks for it. What
		// was named is not wrong, it is unfinished.
		return ui.Model{}, errors.Errorf(
			"%q needs a target before it can be read: open it from the start screen", name)
	}
	return ui.NewLogs(src, query), nil
}

// placeStream finds a place or a group by name, since either is a thing to open
// and whoever wrote the link should not have had to say which it was.
func placeStream(cfg config.Config, name string) (source.Config, bool, error) {
	for _, p := range cfg.Places {
		if p.Name != name {
			continue
		}
		if p.ReadsTraces() {
			return source.Config{}, false, errors.Errorf(
				"%q reads traces rather than lines: telescope trace --from %q", name, name)
		}
		return p.Stream()
	}
	for _, g := range cfg.Groups {
		if g.Name == name {
			return g.Stream()
		}
	}
	return source.Config{}, false, errors.Errorf(
		"no place named %q: run telescope with no argument to see what there is", name)
}

// readTrace reads a trace from wherever it was named: an endpoint, if one was,
// and a file or standard input otherwise.
func readTrace(ctx context.Context, from, api, target string) (*trace.Tree, error) {
	if strings.TrimSpace(from) != "" {
		return fetchTrace(ctx, from, api, target)
	}
	data, err := readFile(target)
	if err != nil {
		return nil, err
	}
	return trace.Decode(data)
}

func readFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func fetchTrace(ctx context.Context, from, api, id string) (*trace.Tree, error) {
	endpoint, err := traceEndpoint(from)
	if err != nil {
		return nil, err
	}
	if endpoint, err = withAPI(endpoint, api); err != nil {
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
	return trace.Decode(data)
}

// withAPI applies --api, which is how a url says what answers at it. A place
// named in the config has said already, and saying it twice is more likely to
// be a mistake than an override.
func withAPI(e source.Endpoint, api string) (source.Endpoint, error) {
	api = strings.TrimSpace(api)
	if api == "" {
		return e, nil
	}
	switch source.Collector(api) {
	case source.CollectorTempo, source.CollectorJaeger:
		e.Collector = source.Collector(api)
		return e, nil
	default:
		return e, errors.Errorf("unknown --api %q: want tempo or jaeger", api)
	}
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
