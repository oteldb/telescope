package source

import (
	"slices"
	"strings"
)

// absentSaid is how a collector says the place does not have what it was
// pointed at. Each tool words it its own way, so this is a list of what they
// say and not a rule.
//
// It is deliberately narrow. "command not found" is the collector itself
// missing and "no route to host" is the host being unreachable; both are worth
// seeing, and only a tool saying the resource is not there belongs here.
var absentSaid = []string{
	"from server (notfound)",      // kubectl, named a resource the cluster does not have
	"no matching resources found", // kubectl, a selector nothing answers to
	"no such container",           // docker
	"no such object",              // docker
}

// absent reports whether what a collector wrote before exiting says the place
// has nothing to give rather than that it could not be read.
//
// A group may name a workload that three clusters out of four run, and the
// fourth is not an error: it is a place with nothing to contribute, and a line
// saying so in the middle of the timeline is noise the reader has to learn to
// skip.
func absent(lines []Line) bool {
	return slices.ContainsFunc(lines, absentLine)
}

// absentSearched bounds what one line is searched for the phrase.
//
// Every tool here writes it at the head of the line, and this runs on each line
// of every stream that is working: lowercasing the whole of somebody's JSON to
// find a phrase that is never past its first few words would be a copy of the
// log per line of it.
const absentSearched = 256

// absentLine is [absent] for one line.
func absentLine(l Line) bool {
	data := l.Data
	if len(data) > absentSearched {
		data = data[:absentSearched]
	}
	said := strings.ToLower(string(data))
	for _, s := range absentSaid {
		if strings.Contains(said, s) {
			return true
		}
	}
	return false
}
