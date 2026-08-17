package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// searchSeg is a run of a field's text drawn in one style.
//
// The form draws its own fields rather than letting the text inputs draw
// themselves, because a tag filter is three things — a key, a comparison, a
// value — and reads as what it is only when they are colored the way the rest
// of the screen colors them. A text input has one style for the whole value.
type searchSeg struct {
	text  string
	style lipgloss.Style
}

// fieldSegs colors what has been typed into one field.
func fieldSegs(f searchField, value string) []searchSeg {
	if f == fieldTags {
		return tagSegs(value)
	}
	return []searchSeg{{value, styleFilter}}
}

// tagSegs reads the tag field as the run of key=value pairs it is, by the same
// rules the filter prompt reads a query by: what separates a key from its value
// is the comparison, and what separates one pair from the next is a space.
func tagSegs(value string) []searchSeg {
	var out []searchSeg
	for i := 0; i < len(value); {
		j := i
		for j < len(value) && value[j] == ' ' {
			j++
		}
		if j > i {
			out = append(out, searchSeg{value[i:j], styleFilter})
			i = j
		}
		for j < len(value) && value[j] != ' ' {
			j++
		}
		if j > i {
			out = append(out, pairSegs(value[i:j])...)
			i = j
		}
	}
	return out
}

func pairSegs(term string) []searchSeg {
	at := strings.IndexAny(term, opChars)
	if at < 0 {
		// Still being named: a key with nothing to compare it to yet.
		return []searchSeg{{term, styleKey}}
	}
	n := opLen(term[at:])
	if at == 0 || n == 0 {
		return []searchSeg{{term, styleFilter}}
	}
	return []searchSeg{
		{term[:at], styleKey},
		{term[at : at+n], styleDim},
		{term[at+n:], styleFilter},
	}
}

// renderInput draws the styled text with the cursor in it, windowed so the
// cursor is always on screen: a field is narrower than what can be typed into
// it, and a tag filter that has scrolled out from under the caret is a field
// nobody can edit.
func renderInput(segs []searchSeg, cursor int, showCursor bool, width int) string {
	var (
		runes []rune
		owner []int
	)
	for i, s := range segs {
		for _, r := range s.text {
			runes = append(runes, r)
			owner = append(owner, i)
		}
	}
	// The caret past the last character has nothing to sit on, so it is given a
	// space of its own.
	cursor = max(cursor, 0)
	if showCursor && cursor >= len(runes) {
		runes = append(runes, ' ')
		owner = append(owner, -1)
		cursor = len(runes) - 1
	}

	from := 0
	if width > 0 && len(runes) > width {
		if cursor >= width {
			from = cursor - width + 1
		}
	}
	to := len(runes)
	if width > 0 {
		to = min(from+width, len(runes))
	}

	var b strings.Builder
	for i := from; i < to; {
		if showCursor && i == cursor {
			b.WriteString(styleAt(segs, owner[i]).Reverse(true).Render(string(runes[i])))
			i++
			continue
		}
		j := i
		for j < to && owner[j] == owner[i] && !(showCursor && j == cursor) {
			j++
		}
		b.WriteString(styleAt(segs, owner[i]).Render(string(runes[i:j])))
		i = j
	}
	return b.String()
}

func styleAt(segs []searchSeg, i int) lipgloss.Style {
	if i < 0 || i >= len(segs) {
		return styleFilter
	}
	return segs[i].style
}

// formRows draws the form. A field that says something is lit and one that is
// still its own example is not, so what the search is actually narrowed by can
// be read off the screen without reading the placeholders to find out which of
// them are placeholders.
func (m searchModel) formRows(width int) []string {
	rows := make([]string, 0, searchFields)
	for f := range searchFields {
		rows = append(rows, m.fieldRow(f, width))
	}
	return rows
}

func (m searchModel) fieldRow(f searchField, width int) string {
	focused := m.focus == int(f)
	value := m.value(f)

	label := styleLabel
	switch {
	case focused:
		label = styleSelected
	case strings.TrimSpace(value) != "":
		label = styleFilter
	}

	segs := fieldSegs(f, value)
	if value == "" {
		segs = []searchSeg{{m.inputs[f].Placeholder, styleHint}}
	}
	// The caret belongs to whatever has the keys: while the suggestions are
	// being walked it is the list that is being moved through, not the text.
	body := renderInput(segs, m.inputs[f].Position(), focused && m.sug < 0, m.fieldWidth())

	row := label.Render(padRight(f.label(), labelWidth)) + " " + body
	if focused {
		return cursorRow(row, width)
	}
	return ansi.Truncate(row, width, "…")
}

func (m searchModel) fieldWidth() int { return max(m.bodyWidth()-labelWidth-1, 10) }
