package trace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Two machines' clocks disagree by more than most spans last, and a child drawn
// starting before the span that called it reads as a causal impossibility.
func TestAChildIsNotDrawnBeforeTheSpanThatCalledIt(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 100, 100),
		at("child", "root", "checkout", "charge", 80, 40),
	})
	require.Equal(t, 1, tr.ClampSkew())

	child, _ := tr.Node("child")
	require.Equal(t, tr.Start, child.Start)
	require.Equal(t, 20*time.Millisecond, child.Skew, "and says how far it was moved")
}

func TestAChildInsideItsParentIsLeftWhereItSaidItWas(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("child", "root", "checkout", "charge", 10, 40),
	})
	require.Equal(t, 0, tr.ClampSkew())

	child, _ := tr.Node("child")
	require.Zero(t, child.Skew)
	require.Equal(t, epoch.Add(10*time.Millisecond), child.Start)
}

// Whatever the clock was wrong by, it was wrong by it for the whole subtree.
func TestTheWholeSubtreeMovesTogether(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 100, 100),
		at("child", "root", "checkout", "charge", 80, 40),
		at("grand", "child", "db", "select", 85, 10),
	})
	tr.ClampSkew()

	child, _ := tr.Node("child")
	grand, _ := tr.Node("grand")
	require.Equal(t, 20*time.Millisecond, grand.Skew)
	require.Equal(t, 5*time.Millisecond, grand.Start.Sub(child.Start), "and keeps its shape")
}

// A child that ran past its parent is pulled back to fit, but never so far
// that it starts before the call that made it.
func TestAChildIsPulledBackNoFurtherThanItsParentsStart(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("child", "root", "db", "select", 90, 50),
	})
	tr.ClampSkew()

	child, _ := tr.Node("child")
	require.Equal(t, epoch.Add(50*time.Millisecond), child.Start)
	require.Equal(t, tr.End, child.End())
}

// A child longer than its parent cannot fit inside it, and the overhang is then
// information rather than a clock: the parent returned before the work it
// started had finished.
func TestAChildTooLongToFitKeepsItsOverhang(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 50),
		at("child", "root", "db", "flush", 30, 200),
	})
	tr.ClampSkew()

	child, _ := tr.Node("child")
	require.Equal(t, tr.Start, child.Start)
	require.Equal(t, 200*time.Millisecond, tr.Duration(), "and the window still covers it")
}

// The trace was measured from a span that has since moved.
func TestTheTraceIsRemeasuredAfterAnythingMoves(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 100, 100),
		at("child", "root", "checkout", "charge", 80, 40),
	})
	require.Equal(t, 120*time.Millisecond, tr.Duration())

	tr.ClampSkew()
	require.Equal(t, epoch.Add(100*time.Millisecond), tr.Start)
	require.Equal(t, 100*time.Millisecond, tr.Duration())
}
