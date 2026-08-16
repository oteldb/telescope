package logs

import (
	"strings"

	"github.com/go-faster/jx"
)

// plFieldKey is the color pl paints a field's name in before writing "=" and
// the value. Recoloring the value means finding it in a rendering that is
// already colored, and the name in front of it is the only landmark there is.
const plFieldKey = "\x1b[36m"

// highlightRecord colors the values of the fields worth recognizing in a
// rendering pl already produced.
//
// It works by rewriting the rendering rather than by producing one, because the
// rendering is not ours: pl decides what a structured line looks like, and a
// second formatter here would be a second thing to keep in step with it. What
// this needs of pl is only that a field reads as "key=value", and a field it
// cannot find is one it leaves alone — so a pl that changes its mind about the
// layout costs the color and nothing else.
func highlightRecord(text string, fields []Field) string {
	for _, f := range fields {
		value := f.String()
		if !plWritesBare(f.Value, value) {
			continue
		}
		colored, ok := HighlightField(f.Key, value)
		if !ok {
			continue
		}
		head := plFieldKey + f.Key + ansiReset + "="
		i := strings.Index(text, head+value)
		if i < 0 {
			continue
		}
		text = text[:i] + head + colored + text[i+len(head)+len(value):]
	}
	return text
}

// plWritesBare reports whether pl writes the value as it stands. It quotes a
// string that contains a space, and a value this cannot predict is one that
// would not be found in the rendering anyway.
func plWritesBare(raw jx.Raw, value string) bool {
	if raw.Type() != jx.String {
		return string(raw) == value
	}
	return !strings.ContainsAny(value, " \t")
}
