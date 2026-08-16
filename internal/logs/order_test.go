package logs

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// at is a line the source dated, as a collector asked for timestamps reports
// one.
func at(sec int, text string) source.Line {
	return source.Line{
		Data: []byte(text),
		At:   time.Date(2026, 8, 10, 1, 0, sec, 0, time.UTC),
	}
}

// TestALateLineLandsWhereItBelongs: "kubectl logs" on a deployment tails every
// pod at once and writes whichever spoke first, so one source arrives out of
// order and the list would otherwise jump backwards in time.
func TestALateLineLandsWhereItBelongs(t *testing.T) {
	s := NewStore(100)
	s.Append(at(1, "one"))
	s.Append(at(3, "three"))
	s.Append(at(2, "two"))

	require.Equal(t, []string{"one", "two", "three"}, bodies(s.Entries()))
	require.Equal(t, []int{0, 2, 1}, []int{
		s.Entries()[0].Seq, s.Entries()[1].Seq, s.Entries()[2].Seq,
	}, "when it arrived is still recorded")
}

// TestLinesInOrderAreNotReordered: the common case pays one comparison and the
// store stays settled, which is what keeps the filtered view incremental.
func TestLinesInOrderAreNotReordered(t *testing.T) {
	s := NewStore(100)
	for i := range 5 {
		s.Append(at(i, fmt.Sprintf("line %d", i)))
	}
	require.Equal(t, s.Len(), s.Settled())

	s.Append(at(2, "late"))
	require.Less(t, s.Settled(), s.Len(), "and once one is late, the tail can still move")
}

// TestTheSameInstantKeepsItsArrivalOrder: two pods writing in the same
// millisecond say nothing about which came first, so what the reader saw stands.
func TestTheSameInstantKeepsItsArrivalOrder(t *testing.T) {
	s := NewStore(100)
	s.Append(at(1, "first"))
	s.Append(at(1, "second"))
	require.Equal(t, []string{"first", "second"}, bodies(s.Entries()))
}

// TestALineOlderThanTheWindowGoesAsFarBackAsItCan: the walk is bounded, so a
// line older than everything held stops at the far end of the window rather
// than paying for the whole history to be searched for a place it is not.
func TestALineOlderThanTheWindowGoesAsFarBackAsItCan(t *testing.T) {
	s := NewStore(10_000)
	for i := range reorderWindow + 10 {
		s.Append(at(i+100, fmt.Sprintf("line %d", i)))
	}
	e := s.Append(at(0, "ancient"))
	require.Same(t, e, s.Entries()[s.Len()-1-reorderWindow])
}

// TestANoteIsNotSteppedOver: telescope saying the stream restarted marks the
// moment it noticed, and a line that jumped over it would claim to have been
// read before the restart it arrived after.
func TestANoteIsNotSteppedOver(t *testing.T) {
	s := NewStore(100)
	s.Append(at(5, "before"))
	s.Append(source.Line{Kind: source.KindRestarted, At: time.Now()})
	s.Append(at(4, "after"))

	require.Equal(t, "after", s.Entries()[2].Record.Body)
}

// TestBandingFollowsWhereALineLanded: the band is what says two lines happened
// in the same second, and the pair either side of a late line did not stop
// happening together because something landed between them.
func TestBandingFollowsWhereALineLanded(t *testing.T) {
	s := NewStore(100)
	s.Append(at(1, "one"))
	s.Append(at(2, "two"))
	s.Append(at(1, "also one"))

	e := s.Entries()
	require.Equal(t, []string{"one", "also one", "two"}, bodies(e))
	require.Equal(t, e[0].Band, e[1].Band, "the same second reads as one block")
	require.NotEqual(t, e[1].Band, e[2].Band, "and the next second as another")
}

// TestTheViewSeesALateLine: the filtered projection is built as lines arrive,
// so a line that lands behind the one it was scanned past has to be picked up
// all the same.
func TestTheViewSeesALateLine(t *testing.T) {
	s := NewStore(100)
	v := NewView(Filter{})

	s.Append(at(1, "alpha"))
	s.Append(at(3, "gamma"))
	require.Equal(t, []string{"alpha", "gamma"}, bodies(v.Entries(s)))

	s.Append(at(2, "beta"))
	require.Equal(t, []string{"alpha", "beta", "gamma"}, bodies(v.Entries(s)))
	require.Equal(t, []string{"alpha", "beta", "gamma"}, bodies(v.Entries(s)),
		"and asking twice does not count it twice")
}

// TestTheViewStillFiltersTheTail: what the store has not settled is matched
// again on every ask, and the filter is the same filter there.
func TestTheViewStillFiltersTheTail(t *testing.T) {
	s := NewStore(100)
	v := NewView(Filter{Query: "beta"})

	s.Append(at(1, "alpha"))
	s.Append(at(3, "gamma"))
	s.Append(at(2, "beta"))
	require.Equal(t, []string{"beta"}, bodies(v.Entries(s)))
}
