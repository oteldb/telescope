package query

import (
	"strconv"
	"strings"

	"github.com/go-faster/errors"
)

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokWord
	tokString
	tokRegexp
	tokOp
	tokLParen
	tokRParen
	tokAnd
	tokOr
	tokNot
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

// describe names a token for the message that says it was not expected.
func (t token) describe() string {
	switch t.kind {
	case tokEOF:
		return "end of query"
	case tokString, tokWord:
		return strconv.Quote(t.text)
	case tokRegexp:
		return "/" + t.text + "/"
	default:
		return t.text
	}
}

// opChars are the bytes that end a bare word, since they start a comparison.
const opChars = "=!<>~"

// lex splits a query into tokens.
//
// A term is read greedily, so a dash or a slash inside a word stays part of it:
// only a leading one is punctuation. That is what lets deploy/api and
// api-server be typed as themselves.
func lex(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ', c == '\t', c == '\n', c == '\r':
			i++
		case c == '(':
			out = append(out, token{kind: tokLParen, text: "(", pos: i})
			i++
		case c == ')':
			out = append(out, token{kind: tokRParen, text: ")", pos: i})
			i++
		// A dash negates the term it leads, except where a value is expected:
		// there it is part of the value, so pod=-1 reads as one.
		case c == '-' && !after(out, tokOp):
			out = append(out, token{kind: tokNot, text: "-", pos: i})
			i++
		case c == '"':
			text, n, err := lexQuoted(s[i:])
			if err != nil {
				return nil, errors.Wrapf(err, "at %d", i)
			}
			out = append(out, token{kind: tokString, text: text, pos: i})
			i += n
		case c == '/':
			text, n, err := lexDelimited(s[i:])
			if err != nil {
				return nil, errors.Wrapf(err, "at %d", i)
			}
			out = append(out, token{kind: tokRegexp, text: text, pos: i})
			i += n
		case strings.IndexByte(opChars, c) >= 0:
			op, n, err := lexOp(s[i:])
			if err != nil {
				return nil, errors.Wrapf(err, "at %d", i)
			}
			out = append(out, token{kind: tokOp, text: op, pos: i})
			i += n
		default:
			word := lexWord(s[i:])
			out = append(out, token{kind: wordKind(word), text: word, pos: i})
			i += len(word)
		}
	}
	return append(out, token{kind: tokEOF, pos: len(s)}), nil
}

// wordKind tells the connectives from everything else. They are only words:
// quoting one is how a line that says "not found" is searched for.
func wordKind(word string) tokenKind {
	switch strings.ToLower(word) {
	case "and":
		return tokAnd
	case "or":
		return tokOr
	case "not":
		return tokNot
	default:
		return tokWord
	}
}

func after(out []token, kind tokenKind) bool {
	return len(out) > 0 && out[len(out)-1].kind == kind
}

func lexWord(s string) string {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == ' ', c == '\t', c == '\n', c == '\r',
			c == '(', c == ')', c == '"',
			strings.IndexByte(opChars, c) >= 0:
			return s[:i]
		}
	}
	return s
}

func lexOp(s string) (op string, n int, err error) {
	for _, want := range []string{"!=", "!~", ">=", "<=", "=", ">", "<", "~"} {
		if strings.HasPrefix(s, want) {
			return want, len(want), nil
		}
	}
	return "", 0, errors.Errorf("unknown operator %q", s[:1])
}

// lexQuoted reads a double-quoted string, which is how anything the lexer would
// otherwise read as punctuation is searched for literally.
func lexQuoted(s string) (text string, n int, err error) {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			text, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return "", 0, errors.Wrap(err, "bad string")
			}
			return text, i + 1, nil
		}
	}
	return "", 0, errors.New("unterminated string")
}

// lexDelimited reads a /regexp/ verbatim, escapes and all: an escaped delimiter
// does not end it, and the regexp engine reads \/ as the slash it stands for.
// Keeping the source as it was typed is what lets it be written back exactly.
func lexDelimited(s string) (text string, n int, err error) {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '/':
			return s[1:i], i + 1, nil
		}
	}
	return "", 0, errors.New("unterminated regexp")
}
