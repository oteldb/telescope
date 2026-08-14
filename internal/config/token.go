package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/figureout"
	fyaml "github.com/go-faster/figureout/source/yaml"
)

// Token says where a secret is read from. It never holds the secret itself, so
// the config file stays shareable and the secret keeps the permissions it
// already has.
//
// Exactly one source may be named.
type Token struct {
	// Env names an environment variable.
	Env string
	// File names a file holding the token, "~" accepted.
	File string
	// Exec runs a command and reads the token from its output, which is how a
	// keyring, a password manager or anything else with a CLI is reached.
	Exec Argv
}

// tokenDescriptor describes a [Token] as it is written in the file. That
// exactly one source may be named is [Token.Validate]'s: it is a rule about the
// three keys together, and the schema describes them one at a time.
var tokenDescriptor = figureout.MustDerive(func(t *Token, s *figureout.Schema[Token]) {
	figureout.Value(s, &t.Env, "env").
		Doc("An environment variable holding the token.")
	figureout.Value(s, &t.File, "file").
		Doc(`A file holding the token, "~" accepted.`)
	figureout.Value(s, &t.Exec, "exec", argvDecoder{}.option()).
		Doc("A command whose first line of output is the token: a keyring, " +
			"a password manager, anything with a CLI.")
})

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

// argvDecoder reads both forms.
//
// It is a decoder rather than a [figureout.ScalarOr] because the alternative
// spelling is a list, not an object, and a list has no descriptor to widen
// into. The shapes it declares are what the generated schema says the key
// accepts, so the two cannot drift.
type argvDecoder struct{}

func (argvDecoder) option() figureout.FieldOption {
	str := figureout.Shape{Kind: figureout.ShapeString}
	return figureout.WithDecoder(fyaml.Source, argvDecoder{},
		str,
		figureout.Shape{Kind: figureout.ShapeArray, Elem: &str},
	)
}

// DecodeValue implements [figureout.Decoder].
func (argvDecoder) DecodeValue(raw any) (any, error) {
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, errors.New("exec is empty")
		}
		// A line goes through a shell, since that is what it was written for:
		// "pass show x | head -1" means nothing to exec(2).
		return Argv{"sh", "-c", v}, nil
	case []any:
		args := make(Argv, 0, len(v))
		for _, item := range v {
			arg, ok := item.(string)
			if !ok {
				return nil, errors.Errorf("exec argument %v is not a string", item)
			}
			args = append(args, arg)
		}
		if len(args) == 0 {
			return nil, errors.New("exec is empty")
		}
		return args, nil
	default:
		return nil, errors.Errorf("exec is a command line or a list of arguments, not %T", raw)
	}
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
