// Package view is what one screen of telescope is looking at, in a form that
// can be written down and handed to somebody else.
//
// It exists so that an agent and a person can point at the same thing. An agent
// that has read a window of logs has the place, the filter and the interval in
// hand; what it does not have is a way to say "look at this" that ends up on
// the screen rather than in a paragraph describing it.
//
// The written form is the command line that opens it. That is deliberately not
// a scheme of its own: a command line runs where it is pasted, needs nothing
// registered with the desktop to work, survives being quoted in a chat window,
// and can be read by whoever is about to run it — which matters, since it names
// a place that may reach a production database over ssh.
//
// It sits below everything that uses it and depends on none of them: `cmd`
// binds flags to it, `ui` opens one, `mcp` writes them out.
package view

import (
	"strings"
)

// The flags a link is written with, named once so that whoever writes one and
// whoever binds them cannot drift apart.
const (
	FlagQuery = "query"
	FlagRange = "range"
	FlagFrom  = "from"
)

// Program is the command a link invokes. A link is pasted into a shell, so it
// says the name the binary is installed under rather than the path this process
// happens to have been started from.
const Program = "telescope"

// Kind is which screen a view is of.
type Kind uint8

const (
	// KindLogs is a place read as a list of lines.
	KindLogs Kind = iota
	// KindTrace is one trace drawn as a gantt.
	KindTrace
	// KindSearch is a store's trace search, with nothing picked yet.
	KindSearch
)

// View is one screen, named by what it would take to open it again.
//
// The level a list is filtered to is not here, and not by omission: the filter
// language says `level>=error` itself, so a link that carried the toggle
// separately would be saying the same thing two ways and would have to decide
// which won.
type View struct {
	Kind Kind
	// Place is the name of a declared place or group, as the config writes it.
	// It is never a command: a link naming one would run whatever it said on
	// the machine of whoever pasted it.
	Place string
	// Query is the filter, in the language the `/` prompt takes.
	Query string
	// Range is the window, in the form [source.ParseRange] reads.
	Range string
	// Trace is the id of the trace to draw, for [KindTrace].
	Trace string
}

// Link writes the view as the command line that opens it.
func (v View) Link() string {
	argv := []string{Program}
	switch v.Kind {
	case KindTrace, KindSearch:
		argv = append(argv, "trace")
		if v.Place != "" {
			argv = append(argv, "--"+FlagFrom, v.Place)
		}
		if v.Kind == KindTrace {
			argv = append(argv, v.Trace)
		}
	default:
		argv = append(argv, v.Place)
		if v.Query != "" {
			argv = append(argv, "--"+FlagQuery, v.Query)
		}
		if v.Range != "" {
			argv = append(argv, "--"+FlagRange, v.Range)
		}
	}

	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, shellQuote(a))
	}
	return strings.Join(quoted, " ")
}

// shellQuote makes one argument survive being pasted into a shell.
//
// A place is named by a person and a filter is typed by one, so both routinely
// hold spaces, and a filter holds quotes and glob characters besides:
// `"connection refused" pod=api-*` is an ordinary thing to have been reading.
// Single quotes are what stops all of it, and the only thing they cannot hold
// is a single quote, which is spliced.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$`*?[]{}()<>|&;#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
