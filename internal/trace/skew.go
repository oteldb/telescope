package trace

import "time"

// ClampSkew moves subtrees so that no span begins before the span that called
// it, and reports how many it had to move.
//
// Two machines' clocks disagree by more than most spans last, so a child drawn
// from raw timestamps routinely starts to the left of its parent and sometimes
// ends entirely outside it. Drawn honestly that reads as a causal impossibility
// and a reader spends their time on the clocks instead of on the request.
//
// This is not Jaeger's adjustment. That one works out a per-service offset from
// matched client and server spans and corrects every span of that service by
// it, which is a better answer and needs the span kinds and references to make
// it. This shifts one subtree by the least that makes it fit, which is enough
// to stop the picture lying and no claim about what the clocks really were —
// hence [Node.Skew], so a row that was moved can say it was.
func (t *Tree) ClampSkew() int {
	if t == nil {
		return 0
	}
	moved := 0
	for _, r := range t.Roots {
		moved += clamp(r)
	}
	t.bounds()
	return moved
}

func clamp(n *Node) int {
	moved := 0
	for _, c := range n.Children {
		if d := fit(n.Span, c.Span); d != 0 {
			shift(c, d)
			moved++
		}
		moved += clamp(c)
	}
	return moved
}

// fit is how far a child has to move to sit inside its parent. A child longer
// than its parent cannot, and gets its start aligned: the overhang past the end
// is then real information — the parent returned before the work it started
// finished — rather than a clock.
func fit(parent, child Span) time.Duration {
	if child.Start.Before(parent.Start) {
		return parent.Start.Sub(child.Start)
	}
	if over := child.End().Sub(parent.End()); over > 0 {
		if back := child.Start.Sub(parent.Start); over > back {
			return -back
		}
		return -over
	}
	return 0
}

func shift(n *Node, d time.Duration) {
	n.Start = n.Start.Add(d)
	n.Skew += d
	for _, c := range n.Children {
		shift(c, d)
	}
}
