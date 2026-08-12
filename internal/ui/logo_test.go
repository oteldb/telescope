package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// styled renders s the way the banner paints a run of one color.
func styled(c lipgloss.TerminalColor, s string) string {
	return lipgloss.NewStyle().Foreground(c).Render(s)
}

// TestBannerRowsAreOneBlock pins the rows to a single width. The start screen
// centers each line on its own, so a row that ends early is pushed right and
// its stems no longer meet the bars above them.
func TestBannerRowsAreOneBlock(t *testing.T) {
	for i, line := range strings.Split(renderLogo(maxContentWidth), "\n") {
		require.Equal(t, lipgloss.Width(logoWide), lipgloss.Width(line), "row %d", i+1)
	}
}

func TestBannerWearsTheMarkOnItsEs(t *testing.T) {
	out := renderLogo(maxContentWidth)

	var rows []string
	for line := range strings.SplitSeq(ansi.Strip(out), "\n") {
		rows = append(rows, strings.TrimRight(line, " "))
	}
	require.Equal(t, logoWide, strings.Join(rows, "\n"), "color and padding are all the art gains")

	// An E opens on the first color of the mark's gradient; the foot of the
	// T stands outside every E and stays ours.
	require.Contains(t, out, styled(markColor(0), ""))
	require.Contains(t, out, styled(colorAccent, ""))
}

func TestEachEIsTheWholeGradient(t *testing.T) {
	for _, span := range markSpans {
		require.Equal(t, markColor(0), logoColor(span[0]))
		require.Equal(t, markColor(1), logoColor(span[1]))
	}
}

func TestNarrowTerminalKeepsTheSmallBanner(t *testing.T) {
	require.Equal(t, styleLogo.Render(logoSmall), renderLogo(lipgloss.Width(logoWide)-1))
	require.NotEqual(t, styleLogo.Render(logoSmall), renderLogo(lipgloss.Width(logoWide)))
}
