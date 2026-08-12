package ui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/source"
)

// suggestRows is how much of the screen the prompt may take from the list. A
// suggestion is worth less than a log line, and the lines under it are the
// reason the filter is being typed at all.
const suggestRows = 6

// completion is what the prompt is being asked to finish.
//
// It is read off the raw text rather than off a parsed query, because a query
// being typed is usually not one yet: "pod=" parses as nothing and is exactly
// the moment a suggestion is worth having.
type completion struct {
	// Key is the field whose values are wanted. Empty means a field name is.
	Key string
	// Prefix is what has been typed of the thing being completed.
	Prefix string
	// At is where in the query the completed text starts, which is what an
	// accepted suggestion replaces from.
	At int
	// OK is false where nothing should be offered: inside a quoted phrase or a
	// regexp, where what is being written is not a name or a value.
	OK bool
}

// completeAt reads the completion the cursor is sitting in.
func completeAt(s string, cursor int) completion {
	cursor = min(max(cursor, 0), len(s))
	start := cursor
	for start > 0 && !breaksTerm(s[start-1]) {
		start--
	}
	// A negation leads a term rather than belonging to it, so -pod= completes
	// the same as pod= does.
	for start < cursor && s[start] == '-' {
		start++
	}
	term := s[start:cursor]

	i := strings.IndexAny(term, opChars)
	if i < 0 {
		// A word that is only an operator's worth of punctuation is not a name.
		return completion{Prefix: term, At: start, OK: true}
	}
	key, rest := term[:i], term[i:]
	n := opLen(rest)
	if key == "" || n == 0 {
		return completion{}
	}
	value := rest[n:]
	// A regexp is written to be a pattern and not a value, so completing it into
	// one would be wrong even where the two look alike.
	if strings.HasPrefix(value, "/") {
		return completion{}
	}
	return completion{Key: key, Prefix: value, At: start + i + n, OK: true}
}

// opChars are the bytes that separate a field from its value, as the query
// lexer reads them.
const opChars = "=!<>~"

// opLen is the length of the comparison s starts with, or zero when it starts
// with something that is not one.
func opLen(s string) int {
	for _, op := range []string{"!=", "!~", ">=", "<=", "=", ">", "<", "~"} {
		if strings.HasPrefix(s, op) {
			return len(op)
		}
	}
	return 0
}

// breaksTerm reports whether a byte ends the term being typed. An operator does
// not: "pod=api" is one term, and which half of it the cursor is in is what
// decides whether a name or a value is wanted.
func breaksTerm(c byte) bool {
	switch c {
	case ' ', '\t', '(', ')', '"':
		return true
	default:
		return false
	}
}

// apply writes value into s in place of what was being completed, and answers
// with the query and where the cursor now belongs.
//
// A field name is inserted with the comparison after it, since a name on its own
// is not a term and the next thing wanted is always the value.
func (c completion) apply(s, value string, isKey bool) (string, int) {
	if !c.OK {
		return s, len(s)
	}
	end := min(c.At+len(c.Prefix), len(s))
	if isKey {
		value += "="
	} else {
		value = query.QuoteValue(value)
	}
	return s[:c.At] + value + s[end:], c.At + len(value)
}

// suggest ranks what the stream is known to offer against what has been typed.
// The two sources are joined rather than chosen between: what the lines already
// read carry is what this stream is actually saying, and what the database knows
// is everything it could say next.
func (m logModel) suggest() (completion, []complete.Candidate) {
	at := completeAt(m.search.Value(), m.search.Position())
	if !at.OK {
		return at, nil
	}

	// An empty prompt is a grep about to be typed far more often than a field
	// name, and a list that took six rows off the view every time "/" was
	// pressed would be in the way. Naming a field is the exception: "pod=" was
	// typed precisely to be finished.
	if at.Prefix == "" && at.Key == "" {
		return at, nil
	}

	var local, remote []string
	if at.Key == "" {
		local, remote = m.store.FieldNames(), m.fields[""]
	} else {
		local, remote = m.store.FieldValues(at.Key), m.fields[at.Key]
	}

	seen := make(map[string]bool, len(local)+len(remote))
	items := make([]complete.Candidate, 0, len(local)+len(remote))
	for _, v := range local {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		items = append(items, complete.Candidate{Value: v})
	}
	// What only the database knows is offered behind what has actually been
	// seen, and marked: a name on screen is the more likely one, and one merely
	// on record may match nothing in the lines this view is holding.
	for _, v := range remote {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		items = append(items, complete.Candidate{Value: v, Detail: "not seen yet"})
	}
	return at, complete.Rank(items, at.Prefix, nil)
}

// fieldsMsg carries what a source answered about its own fields. An empty key
// is the list of names; anything else is the values under that name.
type fieldsMsg struct {
	// cfg identifies the stream that was asked, so an answer arriving after the
	// view moved on is dropped rather than shown for the wrong place.
	cfg    string
	key    string
	values []string
}

// fetchFields asks a source what it is labeled with. A failure is silent: the
// prompt completes by what has been read either way, and a database that will
// not list its fields is not a reason to interrupt somebody typing.
func fetchFields(cfg source.Config, key string) tea.Cmd {
	return func() tea.Msg {
		var (
			values []string
			err    error
		)
		if key == "" {
			values, err = cfg.FieldNames(context.Background())
		} else {
			values, err = cfg.FieldValues(context.Background(), key)
		}
		if err != nil {
			return nil
		}
		return fieldsMsg{cfg: cfg.Title(), key: key, values: values}
	}
}

// wantFields asks the source about what is being completed, once. What was asked
// is recorded before the answer arrives, so a keystroke does not start the same
// request again.
func (m *logModel) wantFields(key string) tea.Cmd {
	if m.asked == nil {
		m.asked = map[string]bool{}
	}
	if m.asked[key] || !m.cfg.Collector.IsRemoteAPI() && m.cfg.Collector != source.CollectorMerge {
		return nil
	}
	m.asked[key] = true
	return fetchFields(m.cfg, key)
}

// takeFields records an answer, unless the view has moved to another place since
// it was asked.
func (m *logModel) takeFields(msg fieldsMsg) {
	if msg.cfg != m.cfg.Title() {
		return
	}
	if m.fields == nil {
		m.fields = map[string][]string{}
	}
	m.fields[msg.key] = msg.values
}
