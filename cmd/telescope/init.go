package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/setup"
)

func initCmd() *cobra.Command {
	var (
		opts   setup.Options
		out    string
		token  string
		force  bool
		stdout bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a first config file",
		Long: "Looks at what this machine already runs — its containers, its units, the\n" +
			"clusters its kubeconfigs name, the hosts its ssh config does — and offers\n" +
			"each one as a place. With --grafana it reads a Grafana's datasources too,\n" +
			"and writes a place for every Loki, VictoriaLogs and Tempo among them.\n\n" +
			"Answering is a line at a time; --yes takes every offer without asking, and\n" +
			"--print writes the file to standard output instead of to disk.",
		Example: "  telescope init\n" +
			"  telescope init --yes --print\n" +
			"  telescope init --grafana https://grafana.example.com --grafana-token env:GRAFANA_TOKEN\n" +
			"  telescope init --grafana-provisioning /etc/grafana/provisioning/datasources",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(token) != "" {
				var err error
				if opts.Grafana.Token, err = setup.ParseToken(token); err != nil {
					return err
				}
			}
			// The questions go to stderr and the file to stdout, so that
			// redirecting one does not swallow the other.
			opts.In, opts.Out = cmd.InOrStdin(), cmd.ErrOrStderr()

			path := out
			if path == "" {
				path = config.Path()
			}
			if !stdout {
				if err := vacant(path, force); err != nil {
					return err
				}
			}

			data, err := setup.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if stdout {
				_, err := cmd.OutOrStdout().Write(data)
				return err
			}
			if err := write(path, data); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "wrote "+path)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.Probe, "probe", true,
		"look at this machine: its containers, units, clusters and ssh hosts")
	f.BoolVarP(&opts.Yes, "yes", "y", false, "take every offer without asking")
	f.StringVar(&opts.Grafana.URL, "grafana", "",
		"a grafana whose datasources become places")
	f.StringVar(&token, "grafana-token", "",
		"where that grafana's token is read from: env:NAME, file:PATH or exec:COMMAND")
	f.StringVar(&opts.Grafana.Provisioning, "grafana-provisioning", "",
		"a grafana provisioning file, or a directory of them, read instead of the API")
	f.StringVar(&out, "config", "", "write here instead of the usual path")
	f.BoolVar(&force, "force", false, "replace a config that is already there")
	f.BoolVar(&stdout, "print", false, "write to standard output instead of to a file")
	return cmd
}

// vacant reports whether the file may be written. A config is somebody's own
// work, and init has no idea what is in it, so replacing one is said out loud
// rather than assumed.
func vacant(path string, force bool) error {
	if path == "" {
		return errors.New("no config path: name one with --config")
	}
	if force {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return errors.Errorf(
			"%s is already there: --force replaces it, --print shows what would go in it", path)
	}
	return nil
}

func write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.Wrap(err, "create config dir")
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return errors.Wrap(err, "write config")
	}
	return nil
}
