package ui

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// logoArt is TELESCOPE cut from the same slanted slabs the go-faster mark is
// drawn with. The slabs are powerline triangles, which only a patched font
// has, and no reply settles whether one is in use: CPR after printing a glyph
// measures the cell it was given, not whether it was drawn, and the font a
// terminal names is not the font fontconfig may have fallen back to. So the
// banner is picked on width alone, and logoSmall is what renders anywhere.
//
//go:embed logo.txt
var logoArt string

// logoWide is the art as a banner: a file ends in a newline, a banner does not.
var logoWide = strings.TrimRight(logoArt, "\n")

// logoSmall is the banner a terminal too narrow for the wordmark gets.
const logoSmall = "" +
	"╺┳╸┏━╸╻  ┏━╸┏━┓┏━╸┏━┓┏━┓┏━╸\n" +
	" ┃ ┣╸ ┃  ┣╸ ┗━┓┃  ┃ ┃┣━┛┣╸ \n" +
	" ╹ ┗━╸┗━╸┗━╸┗━┛┗━╸┗━┛╹  ┗━╸"

// markSpans are the columns of logoWide each E covers. An E is the go-faster
// mark itself rather than a letter we drew, so it keeps the mark's colours
// instead of taking the palette's accent.
var markSpans = [3][2]int{{8, 17}, {27, 36}, {70, 79}}

// The mark is a gradient across its own width, left to right.
var markLeft, markRight = [3]int{0x01, 0xad, 0xd6}, [3]int{0x00, 0xa2, 0x9d}

// markColor is the mark's colour a fraction of the way across it.
func markColor(at float64) lipgloss.Color {
	var c [3]int
	for i := range c {
		c[i] = markLeft[i] + int(float64(markRight[i]-markLeft[i])*at)
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]))
}

// logoColor is what column col of logoWide is painted with.
func logoColor(col int) lipgloss.TerminalColor {
	for _, span := range markSpans {
		if col >= span[0] && col <= span[1] {
			return markColor(float64(col-span[0]) / float64(span[1]-span[0]))
		}
	}
	return colorAccent
}

// renderLogo paints the banner for a block width columns wide. The wide one is
// not bold: the slabs are already solid, and bold brightens a colour in enough
// terminals to pull the E's off the mark's gradient.
func renderLogo(width int) string {
	if width < lipgloss.Width(logoWide) {
		return styleLogo.Render(logoSmall)
	}
	var (
		b     strings.Builder
		run   []rune
		color lipgloss.TerminalColor
		block = lipgloss.Width(logoWide)
	)
	flush := func() {
		if len(run) == 0 {
			return
		}
		b.WriteString(lipgloss.NewStyle().Foreground(color).Render(string(run)))
		run = run[:0]
	}
	for i, line := range strings.Split(logoWide, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		for col, r := range []rune(line) {
			if r == ' ' {
				flush()
				b.WriteRune(r)
				continue
			}
			if c := logoColor(col); c != color {
				flush()
				color = c
			}
			run = append(run, r)
		}
		flush()
		// The screen centres every line on its own width, so a row that ends
		// early slides out from under the row above it and the slabs stop
		// meeting. The rows without bars are the short ones.
		b.WriteString(strings.Repeat(" ", block-lipgloss.Width(line)))
	}
	return b.String()
}
