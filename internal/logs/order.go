package logs

import (
	"slices"
	"time"
)

// reorderWindow is how far back a late line may be carried.
//
// One source is not always one stream. "kubectl logs" on a deployment tails
// every pod of it at once and writes whichever spoke first, so a list that
// appended in arrival order showed a log that jumps backwards a minute and
// forwards again — the lines are in order per pod and in no order at all
// together. The same is true of anything else that fans in: a merge does the
// ordering itself, and this is for the sources that do not.
//
// It is a bounded insertion rather than a sort. A log view is a stream: lines
// arrive one at a time and forever, most of them after the one before them, and
// re-sorting what is held on every line would pay for the whole history to
// place one line. Walking back over the last few hundred costs nothing when the
// line belongs at the end — which is the common case, and is one comparison —
// and is enough for a fan-in, where what is out of order is out of order by
// however far apart the pods' clocks and the reader's are, not by an hour.
//
// The price is what falls outside the window: a line older than the last few
// hundred lands where it arrived. That is the right way round. A view that held
// every line back until it could prove nothing older was coming would not be
// following anything, and the alternative — deciding an old line is a lie and
// dropping it — loses a log line, which is the one thing this must not do.
const reorderWindow = 256

// insert puts e where its time says it belongs among the last [reorderWindow]
// entries, and re-shades whatever it landed in front of.
func (s *Store) insert(e *Entry) {
	at := s.place(e)
	if at == len(s.entries) {
		s.entries = append(s.entries, e)
	} else {
		s.entries = slices.Insert(s.entries, at, e)
		s.reordered = true
	}
	s.shade(at)
}

// place is where e belongs. A line that arrived in order — every line of a
// source that is one stream, and most lines of one that is not — is placed by
// the single comparison against the last.
//
// A line with no time of its own cannot be placed by one, and neither can it be
// stepped over: an entry telescope wrote itself, saying the stream restarted,
// marks the moment it was noticed, and a line that jumped over it would be
// claiming to have been read before the restart it arrived after.
func (s *Store) place(e *Entry) int {
	n := len(s.entries)
	if n == 0 || !e.HasTime {
		return n
	}
	if last := s.entries[n-1]; !last.HasTime || !e.At.Before(last.At) {
		return n
	}
	at := n
	for at > max(n-reorderWindow, 0) {
		prev := s.entries[at-1]
		if !prev.HasTime || !e.At.Before(prev.At) {
			break
		}
		at--
	}
	return at
}

// shade bands the entries from i on. Which second an entry belongs to is
// settled by where it ended up and not by when it turned up, so an insertion
// re-shades what it split: the band is what says two lines happened together,
// and the pair either side of a late line did not stop happening together
// because something landed between them.
func (s *Store) shade(i int) {
	if i == 0 {
		s.band, s.bandAt = false, time.Time{}
	} else {
		prev := s.entries[i-1]
		s.band, s.bandAt = prev.Band, prev.At.Truncate(time.Second)
	}
	for _, e := range s.entries[i:] {
		if sec := e.At.Truncate(time.Second); !sec.Equal(s.bandAt) {
			s.band, s.bandAt = !s.band, sec
		}
		e.Band = s.band
	}
}

// Settled is how many of the held entries can no longer move, which is all of
// them until a line has actually arrived late. A [View] folds in the settled
// ones once and looks at the rest every time it is asked, so a source that is
// one stream is filtered exactly as cheaply as it was before any of this.
func (s *Store) Settled() int {
	if !s.reordered {
		return len(s.entries)
	}
	return max(len(s.entries)-reorderWindow, 0)
}
