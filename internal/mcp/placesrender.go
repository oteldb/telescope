package mcp

import (
	"fmt"
	"strings"
)

// drawPlaces writes what the config declares as the text a model reads.
//
// It exists so the answer is not its own structured content serialized twice.
// A tool that leaves the text to the SDK gets the JSON of the facts as its text
// block, which is the same bytes said again under another key — and this is the
// tool every session calls first, so it is the one worth not paying double for.
//
// A place reads as a line: what it is, what it holds, and whether it opens. The
// detail that only matters when something is wrong — what it still needs, why
// it cannot be opened — goes underneath the line it belongs to rather than into
// a column that is empty for everything that works.
func drawPlaces(out placesOutput) string {
	b := &strings.Builder{}
	w := placeWidths(out)

	if len(out.Places) == 0 && len(out.Groups) == 0 {
		return "the config declares no places, so there is nothing to read. " +
			"telescope init writes one for what this machine runs\n"
	}

	if len(out.Places) > 0 {
		b.WriteString("places:\n")
		for _, p := range out.Places {
			fmt.Fprintf(b, "  %-*s  %-*s  reads %s", w.name, plain(p.Name), w.kind, p.Type,
				strings.Join(p.Reads, ","))
			if p.Target != "" {
				fmt.Fprintf(b, "  %s", plain(p.Target))
			}
			if p.Via != "" {
				fmt.Fprintf(b, "  via %s", plain(p.Via))
			}
			if p.URL != "" {
				fmt.Fprintf(b, "  %s", p.URL)
			}
			b.WriteString("\n")
			writeAside(b, p.Query, p.Range, p.Ready, p.Needs, p.Error)
			if t := p.Traces; t != nil {
				fmt.Fprintf(b, "      traces: %s", tracesSaid(*t))
				b.WriteString("\n")
			}
		}
	}

	if len(out.Groups) > 0 {
		b.WriteString("\ngroups:\n")
		for _, g := range out.Groups {
			fmt.Fprintf(b, "  %-*s  %s\n", w.name, plain(g.Name),
				strings.Join(g.Places, " + "))
			writeAside(b, g.Query, g.Range, g.Ready, g.Needs, g.Error)
		}
	}
	return b.String()
}

// tracesSaid is where a place's traces come from. A store it named is said by
// that name first: it is a place of its own, and the name is what the trace
// tools take.
func tracesSaid(t store) string {
	said := t.URL
	if t.Type != "" {
		said += " (" + t.Type + ")"
	}
	if t.Place != "" {
		return plain(t.Place) + " — " + said
	}
	return said
}

// writeAside writes what is only worth saying when it is there: the defaults a
// place carries, and whichever of ready, needs and error applies.
func writeAside(b *strings.Builder, query, timeRange string, ready bool, needs, failed string) {
	if query != "" {
		fmt.Fprintf(b, "      filtered by %s\n", plain(query))
	}
	if timeRange != "" {
		fmt.Fprintf(b, "      over %s\n", plain(timeRange))
	}
	switch {
	case failed != "":
		fmt.Fprintf(b, "      cannot be opened: %s\n", plain(failed))
	case !ready:
		// Not an error and not a place to skip: it opens once it is told what
		// to read, and the tools take that as an argument.
		if needs == "" {
			needs = "something it was not given"
		}
		fmt.Fprintf(b, "      needs %s\n", plain(needs))
	}
}

type placeWidth struct{ name, kind int }

func placeWidths(out placesOutput) placeWidth {
	var w placeWidth
	for _, p := range out.Places {
		w.name = max(w.name, len(p.Name))
		w.kind = max(w.kind, len(p.Type))
	}
	for _, g := range out.Groups {
		w.name = max(w.name, len(g.Name))
	}
	return w
}

// drawFields writes a field listing, which is the same wrapped run of names a
// trace store's listings read as.
func drawFields(title string, items []string, note string) string {
	b := &strings.Builder{}
	writeList(b, title, items)
	if note != "" {
		fmt.Fprintf(b, "\nnote: %s\n", note)
	}
	return strings.TrimPrefix(b.String(), "\n")
}
