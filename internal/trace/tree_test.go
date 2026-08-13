package trace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var epoch = time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

// at builds a span, both times in milliseconds from when the trace opened.
func at(id, parent, service, name string, start, dur int) Span {
	return Span{
		SpanID:   id,
		ParentID: parent,
		Service:  service,
		Name:     name,
		Start:    epoch.Add(time.Duration(start) * time.Millisecond),
		Duration: time.Duration(dur) * time.Millisecond,
	}
}

func ids(nodes []*Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.SpanID
	}
	return out
}

func TestASpanReadsUnderTheOneThatCalledIt(t *testing.T) {
	tr := Build("t", []Span{
		at("c", "b", "db", "select", 20, 30),
		at("a", "", "gateway", "GET /checkout", 0, 100),
		at("b", "a", "checkout", "charge", 10, 60),
	})

	require.Equal(t, []string{"a"}, ids(tr.Roots))
	require.Equal(t, []string{"b"}, ids(tr.Roots[0].Children))
	require.Equal(t, []string{"c"}, ids(tr.Roots[0].Children[0].Children))
	require.Equal(t, 2, tr.Rows(nil)[2].Depth)
	require.Equal(t, 3, tr.Len())
}

// The order spans arrive in is whatever the database felt like; the order they
// read in is when they happened.
func TestSiblingsReadInTheOrderTheyHappened(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("late", "root", "b", "second", 50, 10),
		at("early", "root", "a", "first", 10, 10),
	})
	require.Equal(t, []string{"early", "late"}, ids(tr.Roots[0].Children))
}

// Two siblings that started together read outermost first, so an operation is
// drawn above the ones it contains rather than under them.
func TestTheLongerOfTwoSiblingsReadsFirst(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("short", "root", "a", "short", 10, 5),
		at("long", "root", "a", "long", 10, 50),
	})
	require.Equal(t, []string{"long", "short"}, ids(tr.Roots[0].Children))
}

// The span above was sampled away, or has not been written yet. Neither is a
// reason to lose the ones below it.
func TestASpanWhoseParentNeverArrivedIsStillDrawn(t *testing.T) {
	tr := Build("t", []Span{
		at("orphan", "gone", "checkout", "charge", 10, 60),
		at("child", "orphan", "db", "select", 20, 30),
	})

	require.Equal(t, []string{"orphan"}, ids(tr.Roots))
	require.True(t, tr.Roots[0].Detached, "and says its parent is missing")
	require.Equal(t, 1, tr.Detached)
	require.Equal(t, 2, tr.Len(), "nothing was dropped")
}

func TestASpanThatIsItsOwnParentIsARoot(t *testing.T) {
	tr := Build("t", []Span{at("a", "a", "svc", "loop", 0, 10)})
	require.Equal(t, []string{"a"}, ids(tr.Roots))
	require.Equal(t, 1, tr.Len())
}

// A ring of parent links has no root to start a walk from, and code that
// followed it would not come back.
func TestACycleIsBrokenRatherThanFollowed(t *testing.T) {
	tr := Build("t", []Span{
		at("a", "c", "svc", "one", 0, 10),
		at("b", "a", "svc", "two", 1, 8),
		at("c", "b", "svc", "three", 2, 6),
	})
	require.Equal(t, 3, tr.Len(), "every span is still reachable")
	require.Len(t, tr.Rows(nil), 3)
	require.Len(t, tr.Roots, 1)
}

// Two collectors writing the same span is indistinguishable from two spans
// given one id, and only one of those is safe to throw away.
func TestASpanReportedTwiceIsKeptTwice(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("dupe", "root", "a", "first copy", 10, 10),
		at("dupe", "root", "a", "second copy", 10, 10),
	})
	require.Equal(t, 3, tr.Len())
	require.Len(t, tr.Rows(nil), 3)
}

func TestASpanWithNoIDIsNotATree(t *testing.T) {
	tr := Build("t", []Span{at("", "", "svc", "nameless", 0, 10)})
	require.Equal(t, 0, tr.Len())
	require.Empty(t, tr.Rows(nil))
	require.Equal(t, Window{Dur: minWindow}, Fit(tr))
}

// One span stamped 1970 would otherwise stretch the window across fifty-six
// years and draw every real span as a single cell.
func TestASpanWithNoClockTakesItsParents(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		{SpanID: "blind", ParentID: "root", Service: "a", Name: "unstamped", Duration: 5 * time.Millisecond},
	})

	blind, ok := tr.Node("blind")
	require.True(t, ok)
	require.Equal(t, tr.Start, blind.Start)
	require.Equal(t, 100*time.Millisecond, tr.Duration())
}

// A write flushed after the response went out is the interesting span, and a
// window cut to the root would hide exactly that.
func TestTheTraceEndsWhenTheLastSpanDoesNotWhenTheRootDoes(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("flush", "root", "db", "flush", 90, 60),
	})
	require.Equal(t, 150*time.Millisecond, tr.Duration())
}

func TestFoldingPutsAWholeSubtreeAway(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("mid", "root", "checkout", "charge", 10, 60),
		at("leaf", "mid", "db", "select", 20, 30),
		at("other", "root", "cache", "get", 80, 5),
	})
	mid, _ := tr.Node("mid")

	require.Equal(t, 1, mid.Hidden())
	require.Equal(t, []string{"root", "mid", "other"}, ids(tr.Rows(map[*Node]bool{mid: true})))

	root, _ := tr.Node("root")
	require.Equal(t, 3, root.Hidden(), "the whole subtree, not the children")
}

// Folding must not be a way to lose the one span somebody opened the trace for.
func TestAFoldOverAnErrorSaysSo(t *testing.T) {
	failed := at("leaf", "mid", "db", "select", 20, 30)
	failed.Status = StatusError
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("mid", "root", "checkout", "charge", 10, 60),
		failed,
	})

	root, _ := tr.Node("root")
	mid, _ := tr.Node("mid")
	leaf, _ := tr.Node("leaf")
	require.True(t, root.FailedBelow())
	require.True(t, mid.FailedBelow())
	require.False(t, leaf.FailedBelow(), "its own status is not something below it")
	require.True(t, leaf.Failed())
}

func TestServicesAreCounted(t *testing.T) {
	tr := Build("t", []Span{
		at("root", "", "gateway", "GET /", 0, 100),
		at("a", "root", "checkout", "charge", 10, 60),
		at("b", "root", "checkout", "refund", 20, 10),
	})
	require.Equal(t, map[string]int{"gateway": 1, "checkout": 2}, tr.Services())
}
