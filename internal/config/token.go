package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"
)

// Token says where a secret is read from. It never holds the secret itself, so
// the config file stays shareable and the secret keeps the permissions it
// already has.
//
// Exactly one source may be named.
type Token struct {
	// Env names an environment variable.
	Env string `yaml:"env,omitempty"`
	// File names a file holding the token, "~" accepted.
	File string `yaml:"file,omitempty"`
	// Exec runs a command and reads the token from its output, which is how a
	// keyring, a password manager or anything else with a CLI is reached.
	Exec Argv `yaml:"exec,omitempty"`
}

// tokenTimeout bounds a token command. It is generous because unlocking a
// keyring may mean answering a prompt.
const tokenTimeout = time.Minute

// expandHome resolves a leading ~, which is what a path in a config file is
// most likely to be written with.
func expandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrap(err, "home directory")
	}
	return filepath.Join(home, rest), nil
}

// IsZero reports whether no token is named, which is what an endpoint that
// needs no credentials looks like.
func (t Token) IsZero() bool { return t.Env == "" && t.File == "" && len(t.Exec) == 0 }

// Validate reports whether the token names one source of a secret.
func (t Token) Validate() error {
	var named []string
	if t.Env != "" {
		named = append(named, "env")
	}
	if t.File != "" {
		named = append(named, "file")
	}
	if len(t.Exec) > 0 {
		named = append(named, "exec")
	}
	if len(named) > 1 {
		return errors.Errorf("token names %s: pick one", strings.Join(named, " and "))
	}
	return nil
}

// Read fetches the secret.
func (t Token) Read(ctx context.Context) (string, error) {
	switch {
	case t.Env != "":
		token := strings.TrimSpace(os.Getenv(t.Env))
		if token == "" {
			return "", errors.Errorf("$%s is not set", t.Env)
		}
		return token, nil
	case t.File != "":
		path, err := expandHome(t.File)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", errors.Wrap(err, "read token")
		}
		return strings.TrimSpace(string(data)), nil
	case len(t.Exec) > 0:
		return t.Exec.run(ctx)
	default:
		return "", nil
	}
}

// Argv is a command, written either as a line for a shell or as its own
// arguments. The list form needs no quoting; the string form may use a pipe.
type Argv []string

// UnmarshalYAML accepts both forms.
func (a *Argv) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var line string
		if err := node.Decode(&line); err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" {
			return errors.New("exec is empty")
		}
		// A line goes through a shell, since that is what it was written for:
		// "pass show x | head -1" means nothing to exec(2).
		*a = Argv{"sh", "-c", line}
		return nil
	}
	var args []string
	if err := node.Decode(&args); err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("exec is empty")
	}
	*a = args
	return nil
}

// run executes the command and returns what it wrote to stdout.
//
// The command inherits the terminal: telescope reads its config before the
// alternate screen opens, so a keyring that asks for a passphrase can still
// ask. What it writes to stderr is kept for the error, since that is where a
// password manager explains itself.
func (a Argv) run(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, a[0], a[1:]...)
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", errors.Wrapf(err, "%s: %s", a[0], oneLine(string(exit.Stderr)))
		}
		if ctx.Err() != nil {
			return "", errors.Errorf("%s did not answer within %s", a[0], tokenTimeout)
		}
		return "", errors.Wrapf(err, "run %s", a[0])
	}
	// The first line only: a manager that prints the secret with notes after it
	// is the common case, and a trailing newline is universal.
	token, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if token == "" {
		return "", errors.Errorf("%s printed no token", a[0])
	}
	return token, nil
}

// oneLine folds a command's complaint onto one line, so it fits where it is
// shown.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
