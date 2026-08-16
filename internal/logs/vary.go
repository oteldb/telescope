package logs

// maxVaryKeys bounds what the varying is remembered for, the way the completion
// index is bounded: a stream is not finite and neither is what it labels itself
// with.
const maxVaryKeys = 4096

// varyIndex remembers whether a key has ever disagreed with itself.
//
// It is what decides a field is worth a column. A key reading the same on every
// line is the stream's own name for itself — the service, the image, the
// content type it always answers with — and repeating it down four hundred rows
// spends the width that the five fields telling the lines apart needed. Which
// key that is cannot be listed in advance: it is `namespace` when one namespace
// is being watched and not when several are, and it is whatever this particular
// service happens to hold constant.
//
// Whether it has disagreed is asked of everything the store holds rather than
// of one stream, because a merge is there to be read across. The label that
// never changes on one of its streams is exactly the label saying which stream
// a line came from, and a merge that hid those would be a merge of nothing in
// particular.
//
// Whether anything has agreed with it yet is asked per source, which is not the
// same question: a stream that has just joined a merge has said one thing once,
// and what another stream settled says nothing about it.
//
// Nothing is ever forgotten, not even when the line that taught it is evicted.
// A key that has varied once goes on being shown, so a column that appeared
// never disappears again — the alternative is a row whose fields shift under
// the reader as the cap bites.
type varyIndex struct {
	held map[string]held
	seen map[sourceKey]int
}

// sourceKey is one key as one source has been writing it. See [varyIndex]: the
// source is part of it so that a stream is judged on what its own lines have
// said, and it is the seam a per-source identity would be threaded through.
type sourceKey struct{ source, key string }

// held is the value a key has been holding, and whether anything disagreed.
type held struct {
	value  string
	varied bool
}

// index records what one entry says.
func (ix *varyIndex) index(e *Entry) {
	for _, f := range e.Record.Fields {
		ix.observe(e.Source, f.Key, f.String())
	}
	for _, l := range e.Labels {
		ix.observe(e.Source, l.Key, l.Value)
	}
}

func (ix *varyIndex) observe(source, key, value string) {
	if key == "" {
		return
	}
	st, known := ix.held[key]
	if !known {
		if len(ix.held) >= maxVaryKeys {
			return
		}
		if ix.held == nil {
			ix.held = make(map[string]held)
			ix.seen = make(map[sourceKey]int)
		}
		ix.held[key] = held{value: value}
	} else if !st.varied && st.value != value {
		st.varied = true
		ix.held[key] = st
	}
	ix.seen[sourceKey{source: source, key: key}]++
}

// varies reports whether key has said anything different, and so whether it is
// worth a column of source's rows.
//
// A key only one of a source's lines has carried is not yet constant: nothing
// has disagreed with it, but nothing has agreed either, and a stream that has
// just opened should read like the stream it is about to be rather than like a
// bare sentence with everything hidden behind it.
func (ix *varyIndex) varies(source, key string) bool {
	if ix.held[key].varied {
		return true
	}
	return ix.seen[sourceKey{source: source, key: key}] < 2
}
