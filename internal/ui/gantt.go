package ui

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/trace"
)

// Column widths. The name column takes a share of the terminal rather than a
// fixed count, since the thing being read is the shape of the bars and a name
// column that ate half an eighty-column terminal would leave nothing to read
// them in.
const (
	nameShareNum = 2
	nameShareDen = 5
	nameMin      = 20
	nameMax      = 52
	// durWidth holds the longest thing [humanDur] writes, right-aligned so the
	// durations read as a column of numbers.
	durWidth = 8
	// barMin is the point below which there is no bar area worth drawing and
	// the view is names alone.
	barMin = 8
)

// gantt is a trace drawn as bars over a shared time window: which trace, where
// the window is, what is folded away, and where the cursor sits.
//
// It is not a tea.Model. Everything here is a function of the tree, the window
// and the cursor, and none of it needs a message loop to be looked at or
// tested; what a key does is the wiring's business.
type gantt struct {
	tree    *trace.Tree
	palette servicePalette
	win     trace.Window
	// bounds is the whole trace, which is what panning is kept near and what
	// the axis counts its offsets from.
	bounds trace.Window

	// filter is the query the spans were narrowed by, and spec is it as it was
	// typed: the bar under the chart shows what is in force, and a query printed
	// back from its own tree would show it rewritten rather than as somebody
	// wrote it. matched is what it selected, which is not every row drawn — see
	// [gantt.selected].
	filter  query.Expr
	spec    string
	matched map[*trace.Node]bool

	rows      []*trace.Node
	collapsed map[*trace.Node]bool
	// hidden is the services filtered out. A span of one is still drawn when
	// something under it is not, since removing it would reparent its children
	// onto a span that never called them.
	hidden map[string]bool
	cursor int
	top    int
}

func newGantt(t *trace.Tree) *gantt {
	g := &gantt{
		tree:      t,
		palette:   newServicePalette(t),
		win:       trace.Fit(t),
		bounds:    trace.Fit(t),
		collapsed: map[*trace.Node]bool{},
		hidden:    map[string]bool{},
	}
	g.reflow()
	return g
}

// reflow rebuilds the visible rows after something folded or unfolded, keeping
// the cursor on the span it was on rather than at the index it was at: folding
// a subtree above the cursor would otherwise slide the selection somewhere the
// reader was not looking.
func (g *gantt) reflow() {
	var was *trace.Node
	if g.cursor >= 0 && g.cursor < len(g.rows) {
		was = g.rows[g.cursor]
	}
	g.rows = g.selected(g.tree.Rows(g.collapsed, g.hidden))
	if was == nil {
		return
	}
	for i, n := range g.rows {
		if n == was {
			g.cursor = i
			return
		}
	}
	// The cursor was inside what just folded, so it lands on the fold.
	for at := was.Parent; at != nil; at = at.Parent {
		for i, n := range g.rows {
			if n == at {
				g.cursor = i
				return
			}
		}
	}
	g.cursor = 0
}

// at is the span under the cursor.
func (g *gantt) at() *trace.Node {
	if g.cursor < 0 || g.cursor >= len(g.rows) {
		return nil
	}
	return g.rows[g.cursor]
}

func (g *gantt) move(d int) {
	if len(g.rows) == 0 {
		return
	}
	g.cursor = min(max(g.cursor+d, 0), len(g.rows)-1)
}

// toggle folds or unfolds the subtree under the cursor.
func (g *gantt) toggle() {
	n := g.at()
	if n == nil || len(n.Children) == 0 {
		return
	}
	if g.collapsed[n] {
		delete(g.collapsed, n)
	} else {
		g.collapsed[n] = true
	}
	g.reflow()
}

// collapseAll folds every span below a root, leaving the request and what it
// called directly.
//
// The roots stay open on purpose. Folding them too is one row and nothing to
// read off it, whereas the top level is the shape somebody opens a
// hundred-span trace to see — and folding down to it one keystroke at a time is
// not reading.
func (g *gantt) collapseAll() {
	g.tree.Walk(func(n *trace.Node) bool {
		if len(n.Children) > 0 && n.Parent != nil {
			g.collapsed[n] = true
		}
		return true
	})
	g.reflow()
}

// expandAll unfolds everything.
func (g *gantt) expandAll() {
	clear(g.collapsed)
	g.reflow()
}

// setFilter narrows the chart to the spans a query selects. A nil expression is
// no filter, which is what an emptied prompt parses to.
func (g *gantt) setFilter(e query.Expr, spec string) {
	g.filter, g.spec = e, spec
	if e == nil {
		g.spec = ""
	}
	g.reflow()
}

// selected drops the rows the filter did not select, keeping the spans above
// one that it did.
//
// Those are kept for the reason the service filter keeps them: removing a span
// would reparent what hangs off it onto something that never called it, and the
// tree is the whole of what says who called whom. What is kept that way is
// structure rather than an answer, which is for the drawing to say.
//
// A filter that selects nothing is obeyed rather than dropped, unlike a service
// filter that hides everything: somebody typed a question here, and answering
// "nothing matches" is the answer. Showing the trace back would be claiming the
// spans on screen are the ones asked for.
func (g *gantt) selected(rows []*trace.Node) []*trace.Node {
	if g.filter == nil {
		g.matched = nil
		return rows
	}
	g.matched = map[*trace.Node]bool{}
	// Matched over the whole tree and not over the rows, so a fold that hides a
	// match is still drawn: what is collapsed is put away, not filtered out.
	above := map[*trace.Node]bool{}
	g.tree.Walk(func(n *trace.Node) bool {
		if !query.Match(g.filter, spanRecord{n}) {
			return true
		}
		g.matched[n] = true
		for at := n; at != nil && !above[at]; at = at.Parent {
			above[at] = true
		}
		return true
	})
	return slices.DeleteFunc(slices.Clone(rows), func(n *trace.Node) bool { return !above[n] })
}

// scaffold reports that a row is drawn only to hold up what is under it: either
// its service was filtered out, or the query selected something below and not
// this.
func (g *gantt) scaffold(n *trace.Node) bool {
	if g.filter != nil && !g.matched[n] {
		return true
	}
	return n.Scaffold(g.hidden)
}

// toggleService filters a service out of the view, or brings it back.
func (g *gantt) toggleService(service string) {
	if g.hidden[service] {
		delete(g.hidden, service)
	} else {
		g.hidden[service] = true
	}
	g.reflow()
}

func (g *gantt) showAllServices() {
	clear(g.hidden)
	g.reflow()
}

// zoom narrows the window around the span under the cursor, so that zooming in
// on something keeps it under the cursor instead of sliding it off an edge.
func (g *gantt) zoom(factor float64) {
	anchor := g.win.Start.Add(g.win.Dur / 2)
	if n := g.at(); n != nil {
		anchor = n.Start
	}
	g.win = g.win.Zoom(factor, anchor).Clamp(g.bounds)
}

// pan slides the window by a fraction of its own width, so a keystroke moves
// the same visible distance however far in somebody has zoomed.
func (g *gantt) pan(frac float64) {
	g.win = g.win.Pan(time.Duration(frac * float64(g.win.Dur))).Clamp(g.bounds)
}

// fit returns the window to the whole trace.
func (g *gantt) fit() { g.win = g.bounds }

// focus narrows the window to the span under the cursor.
func (g *gantt) focus() {
	if n := g.at(); n != nil {
		g.win = trace.Focus(n).Clamp(g.bounds)
	}
}

// layout divides the width into the name, duration and bar columns.
func layout(width int) (name, bar int) {
	name = min(max(width*nameShareNum/nameShareDen, nameMin), nameMax)
	bar = width - name - durWidth - 2
	if bar < barMin {
		bar = 0
		name = max(width-durWidth-1, 0)
	}
	return name, bar
}

// View draws the gantt at a size, header rows included.
func (g *gantt) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if g.tree.Len() == 0 {
		return styleDim.Render("no spans")
	}
	nameWidth, barWidth := layout(width)

	out := []string{
		ansi.Truncate(g.summary(), width, "…"),
		g.axis(nameWidth, barWidth),
	}
	body := max(height-len(out), 0)
	g.clamp(body)

	// A filter is obeyed even where it selects nothing, so the chart has to be
	// able to say that rather than leave the reader looking at an empty frame.
	if len(g.rows) == 0 {
		return strings.Join(append(out, styleDim.Render("no spans match this filter")), "\n")
	}

	for i := g.top; i < len(g.rows) && i < g.top+body; i++ {
		row := g.row(g.rows[i], nameWidth, barWidth)
		if i == g.cursor {
			row = cursorRow(row, width)
		}
		out = append(out, row)
	}
	return strings.Join(out, "\n")
}

// clamp keeps the cursor inside the list and the window around the cursor.
func (g *gantt) clamp(height int) {
	g.cursor = min(max(g.cursor, 0), max(len(g.rows)-1, 0))
	g.top = min(max(g.top, 0), g.cursor)
	if height > 0 && g.cursor >= g.top+height {
		g.top = g.cursor - height + 1
	}
}

// summary is what has to be known before a duration read off the bars means
// anything: how much of the trace this is, and whether it is all of it.
func (g *gantt) summary() string {
	parts := []string{
		styleTitle.Render("trace ") + styleTrace.Render(g.tree.ID),
		styleDim.Render(fmt.Sprintf("%d spans", g.tree.Len())),
		styleDim.Render(fmt.Sprintf("%d services", len(g.tree.Services()))),
		styleDim.Render(humanDur(g.tree.Duration())),
	}
	if g.filter != nil {
		parts = append(parts, styleSelected.Render(fmt.Sprintf("%d of %d spans match",
			len(g.matched), g.tree.Len())))
	}
	if len(g.hidden) > 0 {
		parts = append(parts, styleSelected.Render(fmt.Sprintf("%d of %d services",
			len(g.tree.Services())-len(g.hidden), len(g.tree.Services()))))
	}
	if d := g.tree.Detached; d > 0 {
		// Worded as what it is. A span whose parent never arrived may mean the
		// trace is still being written, and "incomplete" alone reads as
		// corruption.
		parts = append(parts, styleErr.Render(fmt.Sprintf("%s missing a parent", plural(d, "span"))))
	}
	return strings.Join(parts, styleDim.Render(" · "))
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// axisGap is the least room left between two labels on the time axis.
const axisGap = 12

// axis writes the time offsets the bars are read against. Offsets run from the
// start of the trace and not of the window: an axis in wall-clock time would be
// the same date thirteen characters wide at every tick, and one counted from
// wherever somebody happened to pan to would renumber itself as they moved.
func (g *gantt) axis(nameWidth, barWidth int) string {
	if barWidth <= 0 {
		return ""
	}
	cells := []rune(strings.Repeat(" ", barWidth))
	next := 0
	for _, t := range g.win.Ticks(g.bounds.Start, barWidth, axisGap) {
		label := []rune(humanDur(t.Offset))
		at := t.Cell
		// The last label would otherwise run off the frame; anchor it to the
		// right edge instead, which is where a reader looks for the end anyway.
		if at+len(label) > barWidth {
			at = barWidth - len(label)
		}
		if at < next {
			continue
		}
		copy(cells[at:], label)
		next = at + len(label) + 1
	}
	return strings.Repeat(" ", nameWidth+durWidth+2) + styleDim.Render(string(cells))
}

// row draws one span: where it sits in the tree, how long it took, and when.
//
// A span kept only to hold up what is under it draws as the structure it is:
// no bar, and its duration dimmed. Drawing it as an ordinary row would put a
// service back on screen that somebody had just asked to take off it.
func (g *gantt) row(n *trace.Node, nameWidth, barWidth int) string {
	b := &strings.Builder{}
	b.WriteString(padRight(ansi.Truncate(g.name(n), nameWidth, styleDim.Render("…")), nameWidth))
	b.WriteByte(' ')

	scaffold := g.scaffold(n)
	if scaffold {
		b.WriteString(padLeft(styleDim.Render(humanDur(n.Duration)), durWidth))
	} else {
		b.WriteString(padLeft(durStyle(n).Render(humanDur(n.Duration)), durWidth))
	}
	if barWidth > 0 {
		b.WriteByte(' ')
		if scaffold {
			b.WriteString(strings.Repeat(" ", barWidth))
		} else {
			b.WriteString(g.bar(n, barWidth))
		}
	}
	return b.String()
}

// Fold markers. A leaf spends the column on a space rather than on a glyph, so
// the names of the spans that do nothing else line up with the ones that do.
const (
	markOpen   = "▾ "
	markClosed = "▸ "
	markLeaf   = "  "
)

func (g *gantt) name(n *trace.Node) string {
	b := &strings.Builder{}
	b.WriteString(styleDim.Render(treeGuide(n)))
	switch {
	case len(n.Children) == 0:
		b.WriteString(markLeaf)
	case g.collapsed[n]:
		b.WriteString(styleDim.Render(markClosed))
	default:
		b.WriteString(styleDim.Render(markOpen))
	}
	if n.Detached {
		// The span above this one never arrived, so the guide leading into it
		// would be drawing a parent that is not there.
		b.WriteString(styleDim.Render("⋯ "))
	}
	if g.scaffold(n) {
		b.WriteString(styleDim.Render(logs.Sanitize(n.Service) + " " + logs.Sanitize(n.Name)))
	} else {
		b.WriteString(g.palette.style(n.Service).Render(logs.Sanitize(n.Service)))
		b.WriteByte(' ')
		b.WriteString(logs.Sanitize(n.Name))
	}
	if n.Failed() {
		// Said and not only colored. The palette is a qualitative scale and one
		// of its twenty swatches is red, so a service that drew that one would
		// otherwise read as a request that failed — and a reader who cannot
		// tell the two reds apart is not helped by there being two.
		b.WriteString(styleErr.Render(" ✗"))
	}
	if g.collapsed[n] {
		hidden := fmt.Sprintf(" +%d", n.Hidden())
		// A fold that hid an error has to say so, or folding becomes a way to
		// lose the one span somebody opened the trace to find.
		if n.FailedBelow() {
			b.WriteString(styleErr.Render(hidden + " ✗"))
		} else {
			b.WriteString(styleDim.Render(hidden))
		}
	}
	return b.String()
}

// treeGuide draws the lines connecting a span to the one that called it.
//
// One cell per level, not two. A real trace is deep — an authentication filter
// alone can be eight frames — and at two cells a level the guide had taken half
// the name column before the names it is indenting had been read. What is lost
// is the horizontal stroke into the connector, which was decoration; what is
// kept is which parent each row hangs off.
//
// The connector sits at the parent's indent, so the verticals to its left stand
// for the ancestors below the root: each one is a line down to that ancestor's
// next sibling, and it is drawn where that sibling is still to come. The root's
// own column is never a vertical — there is nothing to its left to connect to.
func treeGuide(n *trace.Node) string {
	if n.Depth == 0 {
		return ""
	}
	above := make([]bool, 0, n.Depth)
	for at := n.Parent; at != nil; at = at.Parent {
		above = append(above, at.Last)
	}
	b := &strings.Builder{}
	for i := len(above) - 2; i >= 0; i-- {
		if above[i] {
			b.WriteByte(' ')
		} else {
			b.WriteString("│")
		}
	}
	if n.Last {
		b.WriteString("└")
	} else {
		b.WriteString("├")
	}
	return b.String()
}

// Bar glyphs.
//
// The two edges of a bar do not get the same resolution, and that is a property
// of the block characters rather than a shortcut. The eighths fill a cell from
// its left, so a bar *ending* mid-cell can be drawn to an eighth of a cell with
// no idea what the terminal's background is. A bar *starting* mid-cell needs
// the right of a cell filled, which the block characters only offer as a half —
// the alternative is drawing an eighth in reverse video, which means naming the
// background color, and a transparent terminal does not have one.
//
// So: a right edge lands within an eighth of a cell, a left edge within a half.
// At any zoom worth reading, that is well under the width of the bar.
const (
	barFull   = '█'
	barLeft   = '▌'
	barRight  = '▐'
	barClipLo = '‹'
	barClipHi = '›'
)

var barEighths = [...]rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// barFloor is the least of a cell a span is drawn as. A span too short to cover
// a pixel is still a span that happened, and a row that drew it as nothing
// would say it did not.
const barFloor = 1.0 / 8

func (g *gantt) bar(n *trace.Node, cells int) string {
	x0, x1 := g.win.Span(n.Span, cells)
	return g.barStyle(n).Render(string(barCells(cells, x0, x1)))
}

// barCells draws an interval given in cell coordinates, which may lie partly or
// wholly outside the width.
func barCells(cells int, x0, x1 float64) []rune {
	const eps = 1e-9
	out := make([]rune, cells)
	for i := range out {
		out[i] = ' '
	}
	if cells == 0 {
		return out
	}
	width := float64(cells)
	// Wholly off one side: all that is left to say is which side.
	if x1 <= 0 {
		out[0] = barClipLo
		return out
	}
	if x0 >= width {
		out[cells-1] = barClipHi
		return out
	}

	lo, hi := math.Max(x0, 0), math.Min(x1, width)
	if hi-lo < barFloor {
		if hi = lo + barFloor; hi > width {
			hi, lo = width, width-barFloor
		}
	}
	for i := int(lo); i < cells && float64(i) < hi; i++ {
		a, b := math.Max(lo, float64(i)), math.Min(hi, float64(i+1))
		switch cover := b - a; {
		case cover >= 1-eps:
			out[i] = barFull
		case a <= float64(i)+eps:
			// Flush with the cell's left edge, so the eighths fill it exactly.
			out[i] = barEighths[min(max(int(math.Round(cover*8)), 1), len(barEighths))-1]
		case (a+b)/2 < float64(i)+0.5:
			out[i] = barLeft
		default:
			out[i] = barRight
		}
	}
	// Only marked once the bar has a cell to spare: a one-cell bar replaced by
	// an arrow would be a span that vanished to say it was clipped.
	if drawn := int(hi) - int(lo); drawn >= 1 {
		if x0 < 0 {
			out[0] = barClipLo
		}
		if x1 > width {
			out[cells-1] = barClipHi
		}
	}
	return out
}

// barStyle colors a span by the service that ran it, so a request crossing four
// of them reads as four blocks of color, and red when the span itself failed —
// which outranks whose it was, since a failure is what somebody opened the
// trace for.
func (g *gantt) barStyle(n *trace.Node) lipgloss.Style {
	if n.Failed() {
		return lipgloss.NewStyle().Foreground(colorErr)
	}
	return g.palette.style(n.Service)
}

func durStyle(n *trace.Node) lipgloss.Style {
	if n.Failed() {
		return styleErr
	}
	return styleDim
}

// humanDur writes a span's length the way a trace is read: three significant
// digits in the largest unit that leaves any, which is the convention Jaeger
// and Grafana already write and so the one their readers already parse.
func humanDur(d time.Duration) string {
	if d < 0 {
		return "-" + humanDur(-d)
	}
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return sig(float64(d)/float64(time.Microsecond), "µs")
	case d < time.Second:
		return sig(float64(d)/float64(time.Millisecond), "ms")
	case d < time.Minute:
		return sig(d.Seconds(), "s")
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// sig writes a value to three significant digits, dropping the decimals a whole
// number does not need: "1.5ms" and "150ms", never "1.50ms" or "150.0ms".
func sig(v float64, unit string) string {
	var s string
	switch {
	case v < 10:
		s = fmt.Sprintf("%.2f", v)
	case v < 100:
		s = fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.0f%s", v, unit)
	}
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".") + unit
}

// padLeft and padRight pad to a width the terminal will agree with: a rendered
// cell is full of escapes that take no columns, and a multi-byte glyph takes
// fewer columns than bytes, so neither is a job for fmt's widths.
func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(w-ansi.StringWidth(s), 0)) + s
}

func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}
