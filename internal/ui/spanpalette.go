package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/oteldb/telescope/internal/trace"
)

// spanColors is IBM Carbon's twenty-swatch categorical palette, in the order
// jaeger-ui hands it out, with each theme's values as it defines them.
//
// It is a qualitative scale: neighboring indices are from different hue
// groups, so two services that end up next to each other stay apart. That
// property is the reason for taking the palette wholesale rather than
// extending telescope's own six, which were chosen to tell a handful of merged
// streams apart and go round again after six.
var spanColors = []lipgloss.AdaptiveColor{
	{Light: "#0072c3", Dark: "#1192e8"},
	{Light: "#eb6200", Dark: "#ff832b"},
	{Light: "#8a3ffc", Dark: "#a56eff"},
	{Light: "#b28600", Dark: "#f1c21b"},
	{Light: "#005d5d", Dark: "#009d9a"},
	{Light: "#fa4d56", Dark: "#da1e28"},
	{Light: "#198038", Dark: "#24a148"},
	{Light: "#9f1853", Dark: "#ee538b"},
	{Light: "#002d9c", Dark: "#00539c"},
	{Light: "#6f6f6f", Dark: "#8d8d8d"},
	{Light: "#00539c", Dark: "#0072c3"},
	{Light: "#8a3800", Dark: "#ba4e00"},
	{Light: "#6929c4", Dark: "#8a3ffc"},
	{Light: "#8e6a00", Dark: "#b28600"},
	{Light: "#002d2d", Dark: "#005d5d"},
	{Light: "#570408", Dark: "#a2191f"},
	{Light: "#0e6027", Dark: "#198038"},
	{Light: "#510224", Dark: "#9f1853"},
	{Light: "#001141", Dark: "#002d9c"},
	{Light: "#491d8b", Dark: "#6929c4"},
}

// servicePalette is which color each service in one trace was given.
//
// Colors are handed out in the order the trace first names a service, not
// derived from its name. That is jaeger-ui's rule and the reason for it is
// arithmetic: a hash into twenty buckets collides for six services more than
// half the time, and two services sharing a color in the one trace on screen
// defeats the only thing the color is for.
//
// Where this differs from theirs is that the counter is per trace rather than
// per session. Jaeger's generator is a browser-lifetime singleton, which keeps
// a service's color stable while walking between traces at the cost of the same
// trace drawing differently for two people; their ADR records that as the
// trade-off and reconciling it as unfinished. A gantt is opened on one trace at
// a time, so counting from zero here costs nothing and buys the other side of
// it: the same trace draws the same way every time anybody opens it.
type servicePalette map[string]int

func newServicePalette(t *trace.Tree) servicePalette {
	p := servicePalette{}
	t.Walk(func(n *trace.Node) bool {
		if _, seen := p[n.Service]; !seen {
			p[n.Service] = len(p) % len(spanColors)
		}
		return true
	})
	return p
}

// style is how a service's spans are drawn. A service the palette never saw —
// nothing in this trace, so nothing it has to be told apart from — takes the
// first color rather than no color.
func (p servicePalette) style(service string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(spanColors[p[service]])
}
