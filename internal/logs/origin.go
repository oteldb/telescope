package logs

import (
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// origin is a thing two lines can come from and disagree about, ranked from the
// broadest to the narrowest.
//
// Which of them tells a view's streams apart is not fixed: reading three
// containers of one pod it is the container, reading a deployment it is the
// pod, reading a namespace it is the service. So the ranking is not a
// preference for what to look at — every one of them is looked at — but for
// what to keep when more than one varies, and the narrower a name is the more
// it says about the line beside it.
type origin int

const (
	originSource origin = iota
	originNamespace
	originNode
	originHost
	originService
	originPod
	originContainer
)

// How much of a view the column may take. Both are small on purpose: it stands
// beside every line of the log, and every column it takes is one the log does
// not get.
const (
	maxOriginKeys  = 2
	maxOriginWidth = 14
)

// originOf says what a key names, and whether it names it wherever it appears.
//
// A key the source reported beside the line is trusted whatever it is called:
// that is the shipper saying what the stream is. A key out of the line itself
// is trusted only where it can mean nothing else — an access log calls the
// caller "host" and the thing it proxied to "app", and neither of those wrote
// the line.
func originOf(key string) (kind origin, resource, ok bool) {
	switch semanticOf(key) {
	case semNamespace:
		return originNamespace, true, true
	case semNode:
		return originNode, true, true
	case semPod:
		return originPod, true, true
	case semContainer:
		return originContainer, true, true
	}
	switch normalizeKey(key) {
	case "service_name", "service", "otel_service_name":
		return originService, true, true
	case "app", "application", "component", "unit", "systemd_unit", "_systemd_unit", "syslog_identifier":
		return originService, false, true
	case "host", "hostname", "host_name", "instance", "service_instance_id", "nodename":
		return originHost, false, true
	case "source":
		// Which place of a merge a line came from, which is telescope's own
		// word for it rather than anything the line carried.
		return originSource, false, true
	}
	return 0, false, false
}

// Origins is what tells the streams of a view apart: the keys whose values
// actually differ between the lines it holds, and how much of those values is
// worth showing.
//
// It is derived from the field index rather than kept as a second one — what
// the lines were labeled with is already counted there, and a stream that turns
// out to be several says so through the same values the filter prompt offers.
type Origins struct {
	keys  []originKey
	width int
}

type originKey struct {
	key string
	// trim is how much of the front of every value is the same, and therefore
	// distinguishes nothing: the pods of one deployment are the deployment's
	// name and then the part that differs.
	trim int
	// width is the widest value under this key, once trimmed.
	width int
}

// Origins works out what tells this store's streams apart.
func (s *Store) Origins() Origins {
	pick := make(map[origin]string)
	for _, key := range s.index.order {
		kind, resource, ok := originOf(key)
		if !ok || (!resource && !s.index.labeled[key]) {
			continue
		}
		// One value is one stream: a key every line agrees about is what the
		// view is, not what the lines differ by.
		values := s.index.values[key]
		if len(values) < 2 {
			continue
		}
		// The same thing arrives under several spellings — k8s.pod.name from a
		// collector, pod from a shipper — and the one that saw more of them is
		// the one carried by more of the lines.
		if cur, seen := pick[kind]; !seen || len(values) > len(s.index.values[cur]) {
			pick[kind] = key
		}
	}
	if len(pick) == 0 {
		return Origins{}
	}

	kinds := slices.Sorted(maps.Keys(pick))
	if len(kinds) > maxOriginKeys {
		kinds = kinds[len(kinds)-maxOriginKeys:]
	}

	var o Origins
	for _, kind := range kinds {
		key := pick[kind]
		values := s.index.values[key]
		trim := sharedPrefix(values)
		width := 0
		for _, v := range values {
			width = max(width, utf8.RuneCountInString(v[trim:]))
		}
		o.keys = append(o.keys, originKey{key: key, trim: trim, width: width})
		o.width += width
	}
	o.width = min(o.width+len(o.keys)-1, maxOriginWidth)
	return o
}

// Several reports whether the view is reading more than one stream, which is
// the only case where a column saying which is worth its width.
func (o Origins) Several() bool { return len(o.keys) > 0 }

// Width is how wide the column has to be for the streams seen so far.
func (o Origins) Width() int { return o.width }

// Of names the stream an entry came from: what to write, and the identity to
// color it by.
//
// The two are not the same string. What is written is shortened by whatever
// every stream has in common, and that shrinks as another stream turns up, so a
// color hung off it would change under the reader; the identity is the values
// as they came.
func (o Origins) Of(e *Entry) (label, id string) {
	if len(o.keys) == 0 {
		return "", ""
	}
	var short, full []string
	for _, k := range o.keys {
		v, ok := e.Field(k.key)
		if !ok || v == "" {
			continue
		}
		full = append(full, v)
		short = append(short, cut(k.value(v), k.width))
	}
	if len(full) == 0 {
		return "", ""
	}
	return cut(strings.Join(short, "/"), o.width), strings.Join(full, "/")
}

// Names says the column already draws this key, so a row that repeats it is
// writing the same thing twice — and writing it longer, since the column drops
// the part every stream shares and the row cannot.
func (o Origins) Names(key string) bool {
	norm := normalizeKey(key)
	for _, k := range o.keys {
		if normalizeKey(k.key) == norm {
			return true
		}
	}
	return false
}

// Same reports whether two entries came from the same stream. It is what a
// clamp asks before folding a line into the one above it: the same words from
// two pods are two of them saying it, not one saying it twice.
//
// The values are compared where they are rather than through [Origins.Of],
// which joins them into a string: this is asked of every line held on every
// redraw, and the answer is a boolean either way.
func (o Origins) Same(a, b *Entry) bool {
	for _, k := range o.keys {
		av, _ := a.Field(k.key)
		bv, _ := b.Field(k.key)
		if av != bv {
			return false
		}
	}
	return true
}

// value is v with the part every stream shares taken off.
func (k originKey) value(v string) string {
	if k.trim == 0 || len(v) <= k.trim {
		return v
	}
	return v[k.trim:]
}

// cut shortens a name to fit, from the left: the end of a pod name is the part
// that says which pod.
func cut(s string, width int) string {
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-width+1:])
}

// sharedPrefix is how much of the front of every value is the same, cut back to
// where a name breaks: the pods of one deployment share its name and the
// replica set's hash, and what is left is the five characters that differ.
//
// It stops short of taking a whole value: two names where one is the prefix of
// the other are still two names, and one of them would be left blank.
func sharedPrefix(values []string) int {
	if len(values) < 2 {
		return 0
	}
	n := len(values[0])
	for _, v := range values[1:] {
		n = min(n, commonPrefix(values[0], v))
		if n == 0 {
			return 0
		}
	}
	cut := strings.LastIndexAny(values[0][:n], "-._:/")
	if cut < 0 {
		return 0
	}
	cut++
	for _, v := range values {
		if len(v) <= cut {
			return 0
		}
	}
	return cut
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
