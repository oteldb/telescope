package query

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// parseTests are the queries the language is defined by, and the corpus the
// fuzzer starts from. Want is the query as [Expr.String] writes it back.
var parseTests = []struct {
	name  string
	query string
	want  string
}{
	{"empty query admits everything", "", ""},
	{"blank query admits everything", "   ", ""},
	{"a bare word is a substring", "reset", "reset"},
	{"a quoted phrase keeps its spaces", `"connection reset"`, `"connection reset"`},
	{"adjacent terms are and-ed", "connection reset", "connection reset"},
	{"and may be written out", "connection and reset", "connection reset"},
	{"and is not case sensitive", "connection AND reset", "connection reset"},
	{"or separates alternatives", "reset or refused", "reset or refused"},
	{"and binds tighter than or", "a b or c", "(a b) or c"},
	{"parentheses regroup", "a (b or c)", "a (b or c)"},
	{"not inverts a term", "not reset", "not reset"},
	{"a dash inverts a term", "-reset", "not reset"},
	{"a dash inside a word is part of it", "api-server", "api-server"},
	{"not applies to a group", "-(a or b)", "not (a or b)"},
	{"a slash delimits a regexp", "/timeout|deadline/", "/timeout|deadline/"},
	{"a slash inside a word is part of it", "deploy/api", "deploy/api"},
	{"an escaped slash stays in the regexp", `/a\/b/`, `/a\/b/`},
	{"a field is compared by equality", "pod=api-7", "pod=api-7"},
	{"a field may be denied", "pod!=api-7", "pod!=api-7"},
	{"a field may be matched", "pod~/api-.*/", "pod~/api-.*/"},
	{"a bare value of a match is a regexp", "pod~api", "pod~/api/"},
	{"a regexp value is matched however it is compared", "pod=/api/", "pod~/api/"},
	{"a denied regexp value is a negated match", "pod!=/api/", "pod!~/api/"},
	{"a value may be quoted", `msg="not found"`, `msg="not found"`},
	{"a value may start with a dash", "code=-1", "code=-1"},
	{"level compares", "level>=warn", "level>=warn"},
	{"level names are normalized", "level>=WARNING", "level>=warn"},
	{"a level may be a severity number", "level>=17", "level>=error"},
	{"a keyword may be searched for quoted", `"not"`, `"not"`},
	{"everything at once", `level>=warn (pod=api or pod=worker) -/health/`,
		`level>=warn (pod=api or pod=worker) not /health/`},
}

func TestParse(t *testing.T) {
	for _, tt := range parseTests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := Parse(tt.query)
			require.NoError(t, err)
			if tt.want == "" {
				require.Nil(t, e)
				return
			}
			require.Equal(t, tt.want, e.String())
		})
	}
}

// TestParseWritesBackWhatItRead checks that a query survives being rendered and
// read again, which is what lets the status bar show a filter as a query.
func TestParseWritesBackWhatItRead(t *testing.T) {
	for _, tt := range parseTests {
		if tt.want == "" {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			e, err := Parse(tt.want)
			require.NoError(t, err)
			require.Equal(t, tt.want, e.String())
		})
	}
}

func TestParseErrors(t *testing.T) {
	for _, tt := range []struct {
		name  string
		query string
	}{
		{"an unclosed group", "(a or b"},
		{"an unopened group", "a)"},
		{"an empty group", "()"},
		{"an unterminated string", `"reset`},
		{"an unterminated regexp", "/reset"},
		{"a broken regexp", "/a(/"},
		{"a broken field regexp", "pod~/a(/"},
		{"a comparison with nothing to compare", "pod="},
		{"a comparison of something unordered", "pod>=api"},
		{"a level that is not one", "level>=loud"},
		{"an or with nothing after it", "a or"},
		{"a not with nothing after it", "a and not"},
		{"a bare operator", "="},
		{"an unknown operator", "pod!api"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.query)
			require.Error(t, err)
		})
	}
}
