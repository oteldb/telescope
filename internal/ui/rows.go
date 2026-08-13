package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ansiReset ends a color sequence. A rendered log line is full of them, and
// each one takes any background laid under the line with it.
const ansiReset = "\x1b[0m"

// rgb is a color stop, mixed per column to make a gradient.
type rgb struct{ r, g, b int }

// Row backgrounds. A log line is prose of no fixed shape, and the eye needs
// something to hold on to.
var (
	// The cursor row fades violet to magenta, telescope's own colors, dark
	// enough on either theme that the line's own coloring stays readable.
	cursorFrom = adaptiveStop(rgb{225, 216, 255}, rgb{56, 42, 92})
	cursorTo   = adaptiveStop(rgb{255, 214, 238}, rgb{86, 34, 76})

	// A band is not decoration: it marks the lines that happened in the same
	// second, so a burst reads as a block and a gap reads as a seam. Faint
	// enough to be seen only as grouping.
	bandWash = lipgloss.AdaptiveColor{Light: "#f4f1fd", Dark: "#1b1a28"}

	// What telescope says about a source is not a log line, and a reader
	// scanning for the shape of the log should not have to read it to know
	// that. Dim, since it is a remark and not an alarm.
	noteWash = lipgloss.AdaptiveColor{Light: "#fbe9e9", Dark: "#3a1c20"}
)

// stop is one end of the gradient on each theme, mixed before the theme is
// chosen so the fade is the right one either way.
type stop struct{ light, dark rgb }

func adaptiveStop(light, dark rgb) stop { return stop{light: light, dark: dark} }

// bandRow lays the band wash under a row.
func bandRow(row string, width int) string {
	bg := bgSequence(bandWash)
	return paint(row, width, func(int) string { return bg })
}

// noteRow lays the note wash under a row telescope wrote itself.
func noteRow(row string, width int) string {
	bg := bgSequence(noteWash)
	return paint(row, width, func(int) string { return bg })
}

// cursorRow lays the gradient under the row the cursor is on.
func cursorRow(row string, width int) string {
	cells := make([]string, width)
	for i := range cells {
		cells[i] = bgSequence(lipgloss.AdaptiveColor{
			Light: hex(lerp(cursorFrom.light, cursorTo.light, i, width)),
			Dark:  hex(lerp(cursorFrom.dark, cursorTo.dark, i, width)),
		})
	}
	return paint(row, width, func(col int) string {
		if col >= len(cells) {
			return ""
		}
		return cells[col]
	})
}

// paint lays a background under a row, keeping the colors the row already
// carries: only the background is set, and it is set again after every reset
// the row contains, since a reset would otherwise end the background there.
func paint(row string, width int, bg func(col int) string) string {
	b := &strings.Builder{}
	b.Grow(len(row) + width)

	// A nil parser is enough: what a sequence means is its own business, and
	// all this needs to know is how wide it is.
	var (
		state byte
		col   int
		armed bool
		last  string
	)
	// arm sets the background for a column, unless it is already the one in
	// effect: a flat stripe is one sequence and a fade is one per column, from
	// the same code.
	arm := func(col int) {
		seq := bg(col)
		if armed && seq == last {
			return
		}
		b.WriteString(seq)
		last, armed = seq, true
	}

	for i := 0; i < len(row); {
		seq, w, n, next := ansi.DecodeSequence(row[i:], state, nil)
		state, i = next, i+n
		if w == 0 {
			b.WriteString(seq)
			// The row just reset its own attributes, and took the background
			// with them.
			armed = armed && !isReset(seq)
			continue
		}
		arm(col)
		b.WriteString(seq)
		col += w
	}
	// The background is what makes the row a row, so it runs to the frame.
	for ; col < width; col++ {
		arm(col)
		b.WriteByte(' ')
	}
	b.WriteString(ansiReset)
	return b.String()
}

// isReset reports whether a sequence clears the attributes set before it.
func isReset(seq string) bool { return seq == ansiReset || seq == "\x1b[m" }

// bgSequence is the escape that sets a background, in whatever the terminal can
// actually show.
func bgSequence(c lipgloss.AdaptiveColor) string {
	value := c.Light
	if lipgloss.HasDarkBackground() {
		value = c.Dark
	}
	color := lipgloss.ColorProfile().Color(value)
	if color == nil {
		return ""
	}
	return "\x1b[" + color.Sequence(true) + "m"
}

// lerp mixes two stops at step i of n.
func lerp(from, to rgb, i, n int) rgb {
	if n <= 1 {
		return from
	}
	mix := func(a, b int) int { return a + (b-a)*i/(n-1) }
	return rgb{mix(from.r, to.r), mix(from.g, to.g), mix(from.b, to.b)}
}

// hex writes a color the way lipgloss reads one, so a terminal without
// truecolor still gets the nearest thing it has.
func hex(c rgb) string {
	const digits = "0123456789abcdef"
	out := []byte{'#', 0, 0, 0, 0, 0, 0}
	for i, v := range [3]int{c.r, c.g, c.b} {
		v = min(max(v, 0), 255)
		out[1+2*i] = digits[v>>4]
		out[2+2*i] = digits[v&0xf]
	}
	return string(out)
}
