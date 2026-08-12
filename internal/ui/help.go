package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// helpModel is the filter language, written out where it is typed.
//
// It is a reference and not a tutorial: every row is something that can be typed
// into the prompt as it stands, so reading one line is enough to use it.
type helpModel struct {
	w, h int
	// top is the first row drawn, since the reference is longer than a short
	// terminal.
	top int
}

// helpRow is one line of the reference: what to type, and what it means. A row
// with no syntax is prose about the rows above it.
type helpRow struct {
	syntax string
	desc   string
}

// helpSection groups the rows under a heading.
type helpSection struct {
	title string
	rows  []helpRow
}

// helpSections is the language itself. It is written here rather than generated
// from the parser because what a term means is not something the parser knows:
// it knows that a bare word is a [query.Text] and not that a bare word is how
// you grep.
var helpSections = []helpSection{{
	title: "terms",
	rows: []helpRow{
		{"error", "a case-insensitive substring of the line and of the labels beside it"},
		{`"connection reset"`, "a phrase: quoting is how spaces and punctuation are searched for literally"},
		{"/timeout|deadline/", "a regexp over the same text, case-insensitive like the rest"},
		{"", "A word is read greedily, so deploy/api and api-server are typed as themselves."},
	},
}, {
	title: "fields",
	rows: []helpRow{
		{"pod=api-7", "the field is the value, whole and ignoring case"},
		{"pod!=api-7", "it is not — and a line carrying no pod at all passes"},
		{"pod~api-.", "the field matches a regexp"},
		{"pod!~api-.", "it does not"},
		{"pod=/api-./", "slashes make a value a regexp whichever way it was compared"},
		{"", "A field is a label the source reported, a key of a structured line, or one of"},
		{"", "msg, level, time, trace_id, span_id, source and stream."},
	},
}, {
	title: "level",
	rows: []helpRow{
		{"level>=warn", "severity is the one field that compares"},
		{"level=error", "and the only one where an ordering makes sense"},
		{"", "debug, info, warn and error, plus the spellings around them — trace,"},
		{"", "warning, err, crit, notice — and OTEL or syslog severity numbers."},
		{"", "A line that reported no level of its own passes no level term: an"},
		{"", "unlevelled line is not quietly an info one."},
	},
}, {
	title: "combining",
	rows: []helpRow{
		{"error timeout", "next to each other is and, which is why most queries never type it"},
		{"error and timeout", "the same thing, said out loud"},
		{"error or timeout", "either"},
		{"not error", "neither — and -error is the short way to write it"},
		{"(a or b) c", "brackets where the grouping would otherwise be the other one"},
	},
}, {
	title: "where it runs",
	rows: []helpRow{
		{"", "Every term is answered by looking at one line, so a filter selects the"},
		{"", "same entries whichever source produced them. A database that can answer"},
		{"", "part of the query is asked it — that is an optimization and never a"},
		{"", "different result, and a server that will not read it is simply asked"},
		{"", "for less."},
	},
}}

// helpKeys are the keys the reference itself answers to.
const helpKeys = "↑↓ scroll · esc close"

// openHelp asks for the reference.
func openHelp() tea.Msg { return openHelpMsg{} }

func newHelp(w, h int) helpModel {
	return helpModel{w: w, h: h}
}

func (m *helpModel) resize(w, h int) { m.w, m.h = w, h }

func (m helpModel) Update(msg tea.Msg) (helpModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc", "?", "q", "enter":
		return m, func() tea.Msg { return backMsg{} }
	case "up", "k":
		m.top--
	case "down", "j":
		m.top++
	case "pgup":
		m.top -= m.bodyHeight()
	case "pgdown":
		m.top += m.bodyHeight()
	case "home", "g":
		m.top = 0
	case "end", "G":
		m.top = len(m.lines())
	}
	m.clamp()
	return m, nil
}

// bodyHeight is what is left for the reference once its frame, its title and its
// help line are taken out.
func (m helpModel) bodyHeight() int {
	if h := m.h - 5; h > 0 {
		return h
	}
	return 1
}

func (m helpModel) width() int { return max(m.w-2*screenPad, 20) }

// clamp keeps the window inside the reference, and shows all of it at once when
// it fits.
func (m *helpModel) clamp() {
	m.top = min(max(m.top, 0), max(len(m.lines())-m.bodyHeight(), 0))
}

// syntaxWidth is the column the descriptions start in. It is fixed rather than
// measured so the reference reads as a table on a narrow terminal too, where the
// widest row is the one that wraps away.
const syntaxWidth = 20

// lines renders the reference, one screen line per entry. It is rebuilt per
// frame, which costs nothing next to a log list and keeps the width honest when
// the terminal is resized.
func (m helpModel) lines() []string {
	inner := max(m.width()-4, 20)
	var out []string
	for i, section := range helpSections {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, styleTitle.Render(section.title))
		for _, row := range section.rows {
			out = append(out, m.renderRow(row, inner)...)
		}
	}
	return out
}

// renderRow draws one row, wrapping prose to the width it has and indenting what
// wraps under the column it started in.
func (m helpModel) renderRow(row helpRow, inner int) []string {
	if row.syntax == "" {
		return wrapHelp(styleHint, row.desc, "  ", inner)
	}
	syntax := row.syntax
	if pad := syntaxWidth - lipgloss.Width(syntax); pad > 0 {
		syntax += strings.Repeat(" ", pad)
	}
	head := "  " + styleFilter.Render(syntax) + "  "
	desc := wrapHelp(styleHint, row.desc, strings.Repeat(" ", syntaxWidth+4), max(inner-syntaxWidth-4, 10))
	if len(desc) == 0 {
		return []string{head}
	}
	desc[0] = head + strings.TrimLeft(desc[0], " ")
	return desc
}

// wrapHelp wraps text to width and indents every line of it.
func wrapHelp(style lipgloss.Style, text, indent string, width int) []string {
	if text == "" {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(ansi.Wrap(text, width, ""), "\n") {
		out = append(out, indent+style.Render(line))
	}
	return out
}

func (m helpModel) View() string {
	lines := m.lines()
	height := m.bodyHeight()

	body := make([]string, 0, height)
	for i := m.top; i < len(lines) && i < m.top+height; i++ {
		body = append(body, lines[i])
	}
	for len(body) < height {
		body = append(body, "")
	}

	inner := max(m.width()-2, 1)
	title := ansi.Truncate(styleTitle.Render("filter syntax"), inner, "…")
	return padScreen(strings.Join([]string{
		title,
		styleBox.Width(m.width()).Padding(0, 1).Render(strings.Join(body, "\n")),
		ansi.Truncate(styleHint.Render(helpKeys), m.width(), ""),
	}, "\n"))
}
