package setup

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/go-faster/errors"
)

// asker reads the answers a line at a time.
//
// It is a prompt on a pipe rather than a bubbletea form, though telescope has
// the pieces for one, because init is the command that runs before anything is
// set up: over ssh in a provisioning run, in CI, with its output redirected
// into the file it is writing. A full-screen model needs a terminal it can take
// over and gives that up for each of those. What is being asked is a list of
// yes-or-no questions, and a line answers one as well as a cursor does.
type asker struct {
	in  io.Reader
	out io.Writer
	// yes takes every offer without reading anything, which is the
	// non-interactive path.
	yes bool

	lines *bufio.Reader
}

func (a *asker) read() (string, error) {
	if a.lines == nil {
		a.lines = bufio.NewReader(a.in)
	}
	line, err := a.lines.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		// A pipe that has run out is somebody who meant to answer and cannot,
		// which is worth naming as the flag that would have answered for them.
		return "", errors.New("no more answers to read: pass --yes to take every offer")
	}
	return strings.ToLower(strings.TrimSpace(line)), nil
}

// confirm asks a yes-or-no question. An empty answer takes def.
func (a *asker) confirm(question string, def bool) (bool, error) {
	if a.yes {
		return true, nil
	}
	choices := "[y/N]"
	if def {
		choices = "[Y/n]"
	}
	for {
		fprintf(a.out, "%s %s ", question, choices)
		answer, err := a.read()
		if err != nil {
			return false, err
		}
		switch answer {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// pinNamespace asks which of a cluster's namespaces the place should pin.
// Answering nothing pins none, which is the place that opens the prompt with
// the cluster already filled in.
func (a *asker) pinNamespace(offer *Offer) error {
	if a.yes || len(offer.Namespaces) == 0 {
		return nil
	}
	for {
		fprintf(a.out, "  namespace for %s, blank for all (%s): ",
			offer.Place.Name, strings.Join(offer.Namespaces, ", "))
		answer, err := a.read()
		if err != nil {
			return err
		}
		if answer == "" {
			return nil
		}
		if slices.Contains(offer.Namespaces, answer) {
			offer.Place.Namespace = answer
			return nil
		}
		fprintf(a.out, "  %s is not one of them\n", answer)
	}
}

func fprintf(w io.Writer, format string, args ...any) {
	// Nothing can be done about a prompt that cannot be printed, and the answer
	// still has to be read.
	_, _ = fmt.Fprintf(w, format, args...)
}
