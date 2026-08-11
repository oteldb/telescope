package logs

import (
	"bytes"
	"regexp"
	"strings"

	"go.uber.org/zap/zapcore"
)

// MinLevel is the minimum severity a [Filter] admits.
//
// It is not a [zapcore.Level] because that type's zero value is
// [zapcore.InfoLevel], which would make an empty filter drop debug records.
type MinLevel int8

// Minimum levels, in the order the log view cycles them.
const (
	LevelAll MinLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

// Level returns the zap level a record must reach to pass.
func (l MinLevel) Level() zapcore.Level {
	switch l {
	case LevelInfo:
		return zapcore.InfoLevel
	case LevelWarn:
		return zapcore.WarnLevel
	case LevelError:
		return zapcore.ErrorLevel
	default:
		return zapcore.DebugLevel
	}
}

// Next returns the following minimum level, wrapping around.
func (l MinLevel) Next() MinLevel {
	if l >= LevelError {
		return LevelAll
	}
	return l + 1
}

// Filter selects entries by grep term and minimum level.
type Filter struct {
	Query    string
	MinLevel MinLevel

	re      *regexp.Regexp
	literal []byte
}

// Compile prepares the query for matching. A query that is not a valid regexp
// degrades to a case-insensitive substring search.
func (f Filter) Compile() Filter {
	f.re, f.literal = nil, nil
	q := f.Query
	if q == "" {
		return f
	}
	re, err := regexp.Compile("(?i)" + q)
	if err != nil {
		f.literal = bytes.ToLower([]byte(q))
		return f
	}
	f.re = re
	return f
}

// Match reports whether e passes the filter.
//
// The labels a source reported are matched along with the line: they are how a
// line is found by the pod that wrote it, and the list has no room to show them.
func (f Filter) Match(e *Entry) bool {
	if e.Record.HasLevel && e.Record.Level < f.MinLevel.Level() {
		return false
	}
	switch {
	case f.re != nil:
		return f.re.Match(e.Raw) || f.re.MatchString(e.labelText)
	case f.literal != nil:
		return bytes.Contains(bytes.ToLower(e.Raw), f.literal) ||
			bytes.Contains(bytes.ToLower([]byte(e.labelText)), f.literal)
	default:
		return true
	}
}

// Equal reports whether two filters select the same entries.
func (f Filter) Equal(o Filter) bool {
	return f.Query == o.Query && f.MinLevel == o.MinLevel
}

// Describe renders the filter for the status bar.
func (f Filter) Describe() string {
	var parts []string
	if f.Query != "" {
		kind := "re"
		if f.re == nil {
			kind = "text"
		}
		parts = append(parts, kind+":"+f.Query)
	}
	if f.MinLevel > LevelAll {
		parts = append(parts, "level≥"+f.MinLevel.Level().String())
	}
	if len(parts) == 0 {
		return "no filter"
	}
	return strings.Join(parts, " · ")
}

// View is an incrementally maintained filtered projection of a [Store].
type View struct {
	filter  Filter
	entries []*Entry
	// scanned is how many store entries have been considered so far.
	scanned int
	// base is the store sequence of the first scanned entry, used to detect
	// eviction from the store's ring.
	base int
}

// NewView returns a view over the store using f.
func NewView(f Filter) *View {
	return &View{filter: f.Compile()}
}

// Filter returns the compiled filter.
func (v *View) Filter() Filter { return v.filter }

// SetFilter replaces the filter, invalidating the projection.
func (v *View) SetFilter(f Filter) {
	if v.filter.Equal(f) {
		return
	}
	v.filter = f.Compile()
	v.entries = nil
	v.scanned = 0
	v.base = 0
}

// Entries returns the entries of s matching the filter. Appends to the store
// are folded in incrementally; eviction triggers a rescan.
func (v *View) Entries(s *Store) []*Entry {
	all := s.Entries()
	if len(all) > 0 && all[0].Seq != v.base {
		v.entries, v.scanned, v.base = nil, 0, all[0].Seq
	}
	if v.scanned > len(all) {
		v.entries, v.scanned = nil, 0
	}
	for _, e := range all[v.scanned:] {
		if v.filter.Match(e) {
			v.entries = append(v.entries, e)
		}
	}
	v.scanned = len(all)
	return v.entries
}
