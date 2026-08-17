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
func originCell(p *originPalette, o logs.Origins, e *logs.Entry) string {
	if !o.Several() {
		return ""
	}
	label, id := o.Of(e)
	pad := strings.Repeat(" ", max(o.Width()-lipgloss.Width(label), 0))
	if label == "" {
		return pad
	}
	return p.style(id).Render(label) + pad
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

// originColors are the gantt's twenty without the swatches a log list has
// already spent: the two reds it writes an error in and the grey it dims what
// does not matter with. A stream is not a severity, and a column that says one
// of three places in the red of ERROR is the view raising an alarm about a name.
var originColors = originSwatches()

func originSwatches() []lipgloss.AdaptiveColor {
	spent := map[string]bool{"#da1e28": true, "#a2191f": true, "#8d8d8d": true}
	out := make([]lipgloss.AdaptiveColor, 0, len(spanColors))
	for _, c := range spanColors {
		if !spent[c.Dark] {
			out = append(out, c)
		}
	}
	return out
}

// originPalette is which color each stream of a view was given.
//
// The name picks it, so a stream keeps its color while a filter narrows the
// list under it and reads the same on the next run: a color handed out in order
// of appearance moves, and a reader who learned that green is the pod that is
// failing would have to learn it again. But a hash alone is not enough — three
// streams into seventeen buckets collide about one time in five, and `api` and
// `worker` did — so the hash is a first choice and the next free color is the
// answer where it is taken. Nothing already handed out moves, and no two
// streams on screen read as one.
//
// See [servicePalette] for why a gantt hands its colors out in order instead:
// there the set is the one trace on screen and known before anything is drawn.
type originPalette struct {
	of   map[string]int
	used []bool
}

// newOriginPalette colors the places of a merge before any of them has said
// anything, so which color a place reads in is what the config declares rather
// than which of them spoke first.
func newOriginPalette(labels []string) *originPalette {
	p := &originPalette{of: make(map[string]int, len(labels)), used: make([]bool, len(originColors))}
	for _, l := range labels {
		p.take(l)
	}
	return p
}

// style colors a stream. A nil palette is a view that has none — the color is
// then the hash alone, which is right for the one stream it is being asked
// about.
func (p *originPalette) style(id string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(originColors[p.take(id)])
}

func (p *originPalette) take(id string) int {
	first := int(hashOf(id) % uint32(len(originColors)))
	if p == nil {
		return first
	}
	if i, ok := p.of[id]; ok {
		return i
	}
	// A merge names its places before any of them speaks, and the column names
	// one by what its lines carry: the chip says `api` where the column says
	// `api/api`. They are the same place and have to read as the same color, so
	// an id that begins with a place already colored takes that color.
	if head, _, cut := strings.Cut(id, "/"); cut {
		if i, known := p.of[head]; known {
			p.of[id] = i
			return i
		}
	}
	i := first
	for range originColors {
		if !p.used[i] {
			break
		}
		i = (i + 1) % len(originColors)
	}
	// More streams than colors ends where it started, sharing with whatever the
	// hash says it shares with. There is nothing better to do with the
	// twenty-first pod than to draw it like one of the twenty.
	p.of[id], p.used[i] = i, true
	return i
}

func hashOf(s string) uint32 {
	h := fnv.New32a()
	_, _ = io.WriteString(h, s)
	return h.Sum32()
}
