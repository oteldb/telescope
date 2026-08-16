package ui

import (
	"hash/fnv"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/oteldb/telescope/internal/logs"
)

// originCell is the source column of one entry, padded so the lines beside it
// line up whichever stream they came from. It is empty for a view reading one
// stream: a column that says the same thing on every row is width taken from
// the log for nothing.
func originCell(o logs.Origins, e *logs.Entry) string {
	if !o.Several() {
		return ""
	}
	label, id := o.Of(e)
	pad := strings.Repeat(" ", max(o.Width()-lipgloss.Width(label), 0))
	if label == "" {
		return pad
	}
	return originStyle(id).Render(label) + pad
}

// originRow drops from a row what the column beside it already says.
func originRow(o logs.Origins, fields []logs.RowField) []logs.RowField {
	if !o.Several() {
		return fields
	}
	out := fields[:0:0]
	for _, f := range fields {
		if !o.Names(f.Key) {
			out = append(out, f)
		}
	}
	return out
}

// originStyle colors a stream by what it is rather than by when it first spoke.
//
// A color handed out in order of appearance moves: the same pod is the second
// stream this time and the fourth after a filter narrowed the list, and a
// reader who learned that green is the pod that is failing has to learn it
// again. Hashing the identity costs the odd collision — see [servicePalette]
// for why a gantt does the opposite — and buys a column that means the same
// thing for as long as the view is open.
//
// The palette is the gantt's twenty rather than the six a merge is tagged with:
// a deployment is more pods than six.
func originStyle(id string) lipgloss.Style {
	h := fnv.New32a()
	_, _ = io.WriteString(h, id)
	return lipgloss.NewStyle().Foreground(spanColors[h.Sum32()%uint32(len(spanColors))])
}
