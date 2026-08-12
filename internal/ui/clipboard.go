package ui

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
	"github.com/go-faster/errors"
)

// copyValue puts a value on the clipboard.
//
// A variable so tests can watch what a copy was given without writing to the
// terminal running them.
var copyValue = func(s string) error { return copyTo(clipboardTool(), s) }

// clipboardTool is the local clipboard program for this session, or nil if the
// session has no clipboard of its own.
//
// Telescope is normally the local end of what it reads: a place reached `via:`
// runs the collector over ssh, not the viewer, so the process holding the value
// is already on the machine holding the clipboard and a pipe is the whole job.
// What decides the tool is the display the session actually has, not what is
// installed — wl-copy on a box with no Wayland is a program that exists and
// cannot work.
func clipboardTool() []string {
	switch {
	case runtime.GOOS == "darwin":
		return []string{"pbcopy"}
	case runtime.GOOS == "windows":
		return []string{"clip"}
	case os.Getenv("WAYLAND_DISPLAY") != "":
		return []string{"wl-copy"}
	case os.Getenv("DISPLAY") != "":
		return []string{"xclip", "-selection", "clipboard"}
	}
	return nil
}

// copyTo hands the value to tool, falling back to OSC 52 when there is no tool
// to hand it to. That is the other way telescope is run — ssh into the box and
// read the logs there — where the clipboard is on the near side of the
// connection and the terminal is the only thing that can reach it. It is the
// fallback rather than the rule because it is the weaker of the two: the
// terminal has to support the sequence, and tmux has to be told to allow it.
func copyTo(tool []string, value string) error {
	if len(tool) == 0 {
		// To stdout, where the renderer already writes and so the one stream
		// certain to be the terminal. Landing mid-frame does no harm: the
		// sequence sets no attribute and moves no cursor.
		if _, err := osc52.New(value).WriteTo(os.Stdout); err != nil {
			return errors.Wrap(err, "write OSC 52")
		}
		return nil
	}

	cmd := exec.Command(tool[0], tool[1:]...)
	cmd.Stdin = strings.NewReader(value)
	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "run %s", tool[0])
	}
	return nil
}
