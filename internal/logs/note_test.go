package logs

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// TestANoteReadsAsWhatHappened: a place that never opened and one that stopped
// halfway are different things to be told, and the kind is what tells them
// apart now that the sentence is not written where the note is made.
func TestANoteReadsAsWhatHappened(t *testing.T) {
	s := NewStore(10)

	open := s.Append(source.Line{
		Kind:   source.KindOpenFailed,
		Reason: "dial tcp: connection refused",
		Source: "eu",
	})
	require.Equal(t, "telescope: eu: could not open: dial tcp: connection refused", open.Text)

	exit := s.Append(source.Line{Kind: source.KindExited, Reason: "exit status 255", Source: "eu"})
	require.Equal(t, "telescope: eu: stopped: exit status 255", exit.Text)

	// One source, so there is no label to say which, and no reason worth
	// repeating: the sentence is still a sentence.
	read := s.Append(source.Line{Kind: source.KindReadFailed})
	require.Equal(t, "telescope: read failed", read.Text)
}

// TestANoteIsFilterable: the words a note reads as are the only thing a query
// can match it by, since it has no fields and no severity.
func TestANoteIsFilterable(t *testing.T) {
	s := NewStore(10)
	s.Append(line("serving"))
	s.Append(source.Line{Kind: source.KindExited, Reason: "exit status 255", Source: "eu"})

	v := NewView(Filter{Query: "255"}.Compile())
	got := v.Entries(s)
	require.Len(t, got, 1)
	require.True(t, got[0].Kind.IsNote())
	require.Contains(t, got[0].Text, "exit status 255")
}

// TestANoteIsNotDatedByTheSourceThatFailed: nothing wrote it at a time of its
// own, so it lands where it turned up and says as much.
func TestANoteIsNotDatedByTheSourceThatFailed(t *testing.T) {
	e := NewStore(10).Append(source.Line{Kind: source.KindReadFailed, Reason: "token too long"})
	require.False(t, e.HasTime)
	require.False(t, e.At.IsZero(), "but it still sorts among the lines it interrupts")
}
