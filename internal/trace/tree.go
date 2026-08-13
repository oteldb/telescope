package trace

import (
	"cmp"
	"slices"
	"time"
)

// Node is a span in the tree its parent links put it in.
type Node struct {
	Span

	Depth    int
	Parent   *Node
	Children []*Node

	// Last reports the node is the final child of its parent, which is what
	// decides whether the guide drawn below it continues down the name column.
	Last bool

	// Detached is a span whose parent it named is not in the trace. It is drawn
	// as a root because there is nowhere else to put it, but it is not one: the
	// span above it was sampled away, or is still being written, or belongs to
	// a service that never reported. Saying so is the difference between a
	// trace with four roots and a trace missing three spans, and only the
	// second is worth telling somebody about.
	Detached bool

	// Skew is what [Tree.ClampSkew] moved this subtree by, zero if it was left
	// where it said it was.
	Skew time.Duration

	// failedBelow is whether anything under this node errored, which is what a
	// collapsed row has to say: folding a subtree away must not fold away the
	// one thing somebody opened the trace to find.
	failedBelow bool
}

// FailedBelow reports whether any descendant errored. It excludes the node
// itself, which reports its own status.
func (n *Node) FailedBelow() bool { return n.failedBelow }

// Tree is a trace arranged by its parent links, over the interval its spans
// cover.
type Tree struct {
	ID string

	Roots []*Node
	// Start and End bound every span in the tree, and are what a full-width
	// window is fitted to.
	Start time.Time
	End   time.Time

	// Detached is how many spans named a parent that never arrived. Any at all
	// means the trace is partial, which is worth saying before somebody reads
	// a latency off it.
	Detached int

	nodes []*Node
	byID  map[string]*Node
}

// Duration is how long the whole trace took.
func (t *Tree) Duration() time.Duration {
	if t == nil {
		return 0
	}
	return t.End.Sub(t.Start)
}

// Len is how many spans the tree holds.
func (t *Tree) Len() int {
	if t == nil {
		return 0
	}
	return len(t.nodes)
}

// Node finds a span by its id, the first of them if the trace reported that id
// more than once.
func (t *Tree) Node(id string) (*Node, bool) {
	if t == nil {
		return nil, false
	}
	n, ok := t.byID[id]
	return n, ok
}

// Services counts the spans each service contributed.
func (t *Tree) Services() map[string]int {
	if t == nil {
		return nil
	}
	out := make(map[string]int)
	for _, n := range t.nodes {
		out[n.Service]++
	}
	return out
}

// Build arranges spans into the tree their parent links describe.
//
// What arrives is not guaranteed to be a tree, and code that assumed it was
// would hang or silently drop spans on traces that are entirely ordinary in
// production: a parent sampled away, a span reported twice by two collectors, a
// runtime that wrote its own id as its parent, a clock that never got set.
// Every one of those resolves to something drawable here rather than to an
// error, because a trace that is nine-tenths readable is worth reading and
// there is nobody to report the other tenth to.
//
// Nothing is dropped. A span that cannot be placed becomes a root; that is the
// whole recovery strategy, and it is the right one — the alternative is a
// viewer that quietly shows fewer spans than the database holds.
func Build(id string, spans []Span) *Tree {
	t := &Tree{ID: id, byID: make(map[string]*Node, len(spans))}
	for _, s := range spans {
		if s.SpanID == "" {
			continue
		}
		n := &Node{Span: s}
		t.nodes = append(t.nodes, n)
		// A span id reported twice is kept, not merged: two collectors writing
		// the same span is indistinguishable here from two spans that really
		// were given one id, and only one of those is safe to throw away. The
		// first keeps the id for lookups; the ids themselves are left alone, so
		// what a reader copies off the screen is what the database holds.
		if _, dupe := t.byID[s.SpanID]; !dupe {
			t.byID[s.SpanID] = n
		}
	}
	if len(t.nodes) == 0 {
		return t
	}

	for _, n := range t.nodes {
		parent, ok := t.byID[n.ParentID]
		switch {
		case n.ParentID == "":
			// A real root.
		case !ok:
			n.Detached = true
			t.Detached++
		case parent == n:
			// Its own parent: not a cycle worth reasoning about, just wrong.
		default:
			n.Parent = parent
			parent.Children = append(parent.Children, n)
		}
	}
	for _, n := range t.nodes {
		if n.Parent == nil {
			t.Roots = append(t.Roots, n)
		}
	}
	t.breakCycles()

	slices.SortFunc(t.Roots, byStart)
	for i, r := range t.Roots {
		r.Last = i == len(t.Roots)-1
		sortChildren(r)
	}
	for _, r := range t.Roots {
		repair(r, time.Time{})
		failures(r)
	}
	t.bounds()
	return t
}

// breakCycles promotes spans that no root can reach. They point at each other
// in a ring, so there is no first one and any of them will do: taking the
// earliest in arrival order at least makes the result the same on every read.
// One promotion opens a whole ring, and a trace has at most a handful.
func (t *Tree) breakCycles() {
	for {
		seen := make(map[*Node]bool, len(t.nodes))
		for _, r := range t.Roots {
			mark(r, seen)
		}
		if len(seen) == len(t.nodes) {
			return
		}
		for _, n := range t.nodes {
			if seen[n] {
				continue
			}
			n.Parent.Children = slices.DeleteFunc(n.Parent.Children, func(c *Node) bool { return c == n })
			n.Parent = nil
			n.Detached = true
			t.Detached++
			t.Roots = append(t.Roots, n)
			break
		}
	}
}

func mark(n *Node, seen map[*Node]bool) {
	if seen[n] {
		return
	}
	seen[n] = true
	for _, c := range n.Children {
		mark(c, seen)
	}
}

// byStart orders siblings as they read: when they started, then the longer
// first so an operation is drawn above the ones it contains, then by id so a
// trace with two indistinguishable spans draws the same way twice.
func byStart(a, b *Node) int {
	if c := a.Start.Compare(b.Start); c != 0 {
		return c
	}
	if c := cmp.Compare(b.Duration, a.Duration); c != 0 {
		return c
	}
	return cmp.Compare(a.SpanID, b.SpanID)
}

func sortChildren(n *Node) {
	slices.SortFunc(n.Children, byStart)
	for i, c := range n.Children {
		c.Depth = n.Depth + 1
		c.Last = i == len(n.Children)-1
		sortChildren(c)
	}
}

// repair gives a span with no clock its caller's start.
//
// A zero timestamp is not a time near the trace, it is 1970, and one span with
// one is enough to stretch the window across fifty-six years and render every
// real span as a single cell. Inheriting the parent's start is a guess, but it
// is a guess inside the request, and it costs nothing to be wrong by the width
// of one span rather than by the width of the window.
func repair(n *Node, parentStart time.Time) {
	if n.Start.IsZero() {
		n.Start = parentStart
	}
	for _, c := range n.Children {
		repair(c, n.Start)
	}
}

func failures(n *Node) bool {
	for _, c := range n.Children {
		if failures(c) {
			n.failedBelow = true
		}
	}
	return n.failedBelow || n.Failed()
}

// bounds fits the tree to what its spans actually cover.
//
// The end is the latest anything ended and not when the root returned: a span
// that outlives the request that started it — a write flushed after the
// response went out — is the interesting one, and a window cut to the root
// would hide exactly that.
//
// Spans still at the zero time after [repair] are left out. They are the ones
// whose whole branch had no clock, and there is nothing to place them by.
func (t *Tree) bounds() {
	var found bool
	for _, n := range t.nodes {
		if n.Start.IsZero() {
			continue
		}
		if !found {
			t.Start, t.End = n.Start, n.End()
			found = true
			continue
		}
		if n.Start.Before(t.Start) {
			t.Start = n.Start
		}
		if e := n.End(); e.After(t.End) {
			t.End = e
		}
	}
}

// Walk visits every span in display order, parents before children, stopping
// early if f says to.
func (t *Tree) Walk(f func(*Node) bool) {
	if t == nil {
		return
	}
	for _, r := range t.Roots {
		if !walk(r, f) {
			return
		}
	}
}

func walk(n *Node, f func(*Node) bool) bool {
	if !f(n) {
		return false
	}
	for _, c := range n.Children {
		if !walk(c, f) {
			return false
		}
	}
	return true
}

// Rows is the tree flattened for display, stopping at anything collapsed names.
//
// Collapse is keyed by node and not by span id, since a trace that reported one
// id twice would otherwise fold both halves at once.
func (t *Tree) Rows(collapsed map[*Node]bool) []*Node {
	if t == nil {
		return nil
	}
	out := make([]*Node, 0, len(t.nodes))
	var rec func(n *Node)
	rec = func(n *Node) {
		out = append(out, n)
		if collapsed[n] {
			return
		}
		for _, c := range n.Children {
			rec(c)
		}
	}
	for _, r := range t.Roots {
		rec(r)
	}
	return out
}

// Hidden is how many spans collapsing this node puts away: the whole subtree
// and not just the children, since "2 more" on a node with thirty descendants
// reads as a lie the moment it is opened.
func (n *Node) Hidden() int {
	total := 0
	for _, c := range n.Children {
		total += 1 + c.Hidden()
	}
	return total
}
