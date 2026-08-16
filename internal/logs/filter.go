package logs

import (
	"strings"

	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/query"
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

// Filter selects entries by query and minimum level. The level is the one the
// view cycles with a key, and it narrows whatever the query already said.
type Filter struct {
	Query    string
	MinLevel MinLevel

	expr query.Expr
	err  error
}

// Compile parses the query. A query that does not parse is kept, with the
// reason: what to do about it is the caller's, and a view that silently
// filtered by something else would be worse than one that says so.
func (f Filter) Compile() Filter {
	f.expr, f.err = query.Parse(f.Query)
	return f
}

// Err is why the query did not parse, if it did not.
func (f Filter) Err() error { return f.err }

// Expr is the parsed query, for a source that can be asked part of it. A nil
// expr selects everything.
func (f Filter) Expr() query.Expr { return f.expr }

// Match reports whether e passes the filter. A query that did not parse selects
// nothing, so a filter is never quietly wider than what was typed.
func (f Filter) Match(e *Entry) bool {
	if e.Record.HasLevel && e.Record.Level < f.MinLevel.Level() {
		return false
	}
	if f.err != nil {
		return false
	}
	return query.Match(f.expr, e)
}

// Equal reports whether two filters select the same entries.
func (f Filter) Equal(o Filter) bool {
	return f.Query == o.Query && f.MinLevel == o.MinLevel
}

// Describe renders the filter for the status bar, as the query it would be
// typed as rather than as what was typed: a filter reads back canonically.
func (f Filter) Describe() string {
	var parts []string
	switch {
	case f.err != nil:
		parts = append(parts, "bad query: "+f.err.Error())
	case f.expr != nil:
		parts = append(parts, f.expr.String())
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
	filter Filter
	// entries are the matches among the entries the store has settled. The tail
	// it has not is matched again on every ask, since a line arriving late lands
	// among those and an incremental scan counts what it walked past.
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

// Entries returns the entries of s matching the filter, in the order the store
// holds them. Appends are folded in incrementally; eviction triggers a rescan,
// and so does the first line that arrives out of order, after which the store's
// unsettled tail is matched afresh each time.
func (v *View) Entries(s *Store) []*Entry {
	all := s.Entries()
	settled := s.Settled()
	if len(all) > 0 && all[0].Seq != v.base {
		v.entries, v.scanned, v.base = nil, 0, all[0].Seq
	}
	if v.scanned > settled {
		v.entries, v.scanned = nil, 0
	}
	for _, e := range all[v.scanned:settled] {
		if v.filter.Match(e) {
			v.entries = append(v.entries, e)
		}
	}
	v.scanned = settled
	if settled == len(all) {
		return v.entries
	}
	// Capped, so matching the tail appends into a slice of its own and leaves
	// what is settled alone.
	out := v.entries[:len(v.entries):len(v.entries)]
	for _, e := range all[settled:] {
		if v.filter.Match(e) {
			out = append(out, e)
		}
	}
	return out
}
