package ui

import (
	"strconv"
	"strings"
)

// maxStackFrames bounds how far down a trace is read. Every frame that is not
// already on disk costs a listing, and the frame anybody wants is at the top:
// a hundred frames of runtime below it are what the panic fell through, not
// where it came from.
const maxStackFrames = 32

// stackFrames reads the places in files out of a stack trace, innermost first.
//
// Nothing here decides what a trace looks like — the runtimes disagree, and a
// line that turns out to be prose rather than a frame yields no line number and
// so no candidate. What comes back is offered to [locator.locate], which is the
// only thing that says whether a path is a file at all.
func stackFrames(s string) []site {
	if !strings.Contains(s, ":") {
		return nil
	}
	var (
		out  []site
		seen = map[site]bool{}
	)
	for line := range strings.SplitSeq(s, "\n") {
		for _, f := range frameSites(line) {
			if f.line == 0 || f.path == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
			if len(out) == maxStackFrames {
				return out
			}
		}
	}
	return out
}

// frameSites reads one line of a trace as every position it could be holding.
// A line is read every way that fits it rather than being classified first:
// which runtime wrote a trace is not knowable from one line of it, and a
// reading that was wrong resolves to no file.
func frameSites(line string) []site {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var out []site

	// CPython writes the path and the line as prose, in quotes.
	if s, ok := pythonFrame(line); ok {
		out = append(out, s)
	}
	// The JVM and V8 put the position in the frame's last parentheses:
	// "at com.example.Bar.baz(Bar.java:42)", "at fn (/src/app.js:1:2)".
	if i := strings.LastIndexByte(line, '('); i >= 0 {
		if j := strings.IndexByte(line[i:], ')'); j > 1 {
			out = append(out, splitSite(line[i+1:i+j]))
		}
	}
	// Go writes the position on a line of its own, indented under the function
	// it belongs to and followed by the program counter; V8 writes it after
	// "at" when the frame has no name to put in front of it.
	fields := strings.Fields(line)
	out = append(out, splitSite(fields[0]))
	if len(fields) > 1 && fields[0] == "at" {
		out = append(out, splitSite(fields[1]))
	}
	return out
}

// pythonFrame reads `File "/src/app.py", line 12, in handler`.
func pythonFrame(line string) (site, bool) {
	rest, ok := strings.CutPrefix(line, `File "`)
	if !ok {
		return site{}, false
	}
	path, rest, ok := strings.Cut(rest, `"`)
	if !ok || path == "" {
		return site{}, false
	}
	rest, ok = strings.CutPrefix(rest, ", line ")
	if !ok {
		return site{}, false
	}
	num, _, _ := strings.Cut(rest, ",")
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || n <= 0 {
		return site{}, false
	}
	return site{path: path, line: n}, true
}

// stackSite is the innermost frame of a trace that is a file here.
//
// The innermost that resolves rather than the innermost there is: a trace
// starts in the runtime or in a library, and the frame worth opening is the
// first one that belongs to the code being read.
func (l locator) stackSite(trace string) (site, bool) {
	for _, f := range stackFrames(trace) {
		if got, ok := l.locate(f); ok {
			return got, true
		}
	}
	return site{}, false
}

// stackKeys are the names a trace is written under. It is not one of the record's
// own fields: what a logger calls it is what it is called here.
var stackKeys = []string{
	"stacktrace", "stack_trace", "stack",
	"exception.stacktrace", "exception.stack_trace",
	"error.stack_trace", "error.stack",
}
