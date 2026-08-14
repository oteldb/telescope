package source

// Kind says what a line is: bytes a source wrote, or telescope's own words
// about the source.
//
// It is a kind rather than a flag because the difference between the notes
// matters to whoever reads one. A place that never opened, a stream that broke
// halfway and a collector that exited are three things to be told, and a view
// that only knew "telescope wrote this" would have to read the sentence back to
// tell them apart.
type Kind uint8

const (
	// KindLog is a line the source wrote, which is every line unless something
	// says otherwise.
	KindLog Kind = iota
	// KindOpenFailed is a source that never opened.
	KindOpenFailed
	// KindReadFailed is a source that opened and could not be read to the end,
	// most often a line past maxLineSize.
	KindReadFailed
	// KindExited is a source that stopped with an error.
	KindExited
	// KindRestarted is a container the source is reading from coming back: not
	// a failure of the stream, and the one note here that is about the thing
	// being read rather than about the reading of it.
	KindRestarted
)

// IsNote reports whether the line is telescope talking rather than the source.
func (k Kind) IsNote() bool { return k != KindLog }
