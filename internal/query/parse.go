package query

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/go-faster/errors"
)

// levelKey is the one field that compares rather than equates, since severities
// are ordered and nothing else a line carries is known to be.
const levelKey = "level"

// Parse reads a query.
//
//	error and not /timeout|deadline/
//	level>=warn pod=api-7
//	"connection reset" or (retry and -giving)
//
// A bare word or a quoted phrase is a case-insensitive substring of the line
// and of the labels beside it. Terms sitting next to each other are and-ed.
// An empty query is a nil [Expr], which admits everything.
func Parse(s string) (Expr, error) {
	tokens, err := lex(s)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 1 {
		return nil, nil
	}
	p := parser{tokens: tokens}
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tokEOF {
		return nil, errors.Errorf("unexpected %s", t.describe())
	}
	return e, nil
}

type parser struct {
	tokens []token
	i      int
}

func (p *parser) peek() token { return p.tokens[p.i] }

func (p *parser) next() token {
	t := p.tokens[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

func (p *parser) parseOr() (Expr, error) {
	first, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokOr {
		return first, nil
	}
	out := Or{first}
	for p.peek().kind == tokOr {
		p.next()
		e, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// parseAnd reads operands until something that cannot start one. Juxtaposition
// is a conjunction, so the and is optional and most queries never type it.
func (p *parser) parseAnd() (Expr, error) {
	first, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	var out And
	for {
		if p.peek().kind == tokAnd {
			p.next()
		} else if !starts(p.peek().kind) {
			break
		}
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = And{first}
		}
		out = append(out, e)
	}
	if out == nil {
		return first, nil
	}
	return out, nil
}

// starts reports whether a token can begin an operand.
func starts(k tokenKind) bool {
	switch k {
	case tokWord, tokString, tokRegexp, tokLParen, tokNot:
		return true
	default:
		return false
	}
}

func (p *parser) parseUnary() (Expr, error) {
	if p.peek().kind == tokNot {
		p.next()
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return Not{Expr: e}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.next()
	switch t.kind {
	case tokLParen:
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if got := p.next(); got.kind != tokRParen {
			return nil, errors.Errorf("unclosed (, got %s", got.describe())
		}
		return e, nil
	case tokRegexp:
		return newRegexp(t.text)
	case tokString:
		return newText(t.text), nil
	case tokWord:
		if p.peek().kind == tokOp {
			return p.parseField(t.text)
		}
		return newText(t.text), nil
	case tokEOF:
		return nil, errors.New("query ends where a term was expected")
	default:
		return nil, errors.Errorf("unexpected %s", t.describe())
	}
}

func (p *parser) parseField(key string) (Expr, error) {
	op := Op(p.next().text)
	value := p.next()
	switch value.kind {
	case tokWord, tokString, tokRegexp:
	default:
		return nil, errors.Errorf("%s%s wants a value, got %s", key, op, value.describe())
	}
	if strings.EqualFold(key, levelKey) {
		return newLevel(op, value.text)
	}
	if op.ordering() {
		return nil, errors.Errorf(
			"%s compares only %s: severities are ordered and %s is not", op, levelKey, key)
	}
	// A regexp written as one is matched as one, whichever way it was compared:
	// nothing else could have been meant by the slashes.
	if value.kind == tokRegexp && (op == OpEq || op == OpNe) {
		if op == OpEq {
			op = OpMatch
		} else {
			op = OpNotMatch
		}
	}
	field := Field{Key: key, Op: op, Value: value.text}
	if op == OpMatch || op == OpNotMatch {
		if value.kind != tokRegexp {
			field.Value = escapeDelim(value.text)
		}
		re, err := compile(field.Value)
		if err != nil {
			return nil, err
		}
		field.re = re
	}
	return field, nil
}

func newLevel(op Op, name string) (Expr, error) {
	level, ok := ParseLevel(name)
	if !ok {
		return nil, errors.Errorf("unknown level %q: want debug, info, warn or error", name)
	}
	return Level{Op: op, Level: level}, nil
}

func newText(value string) Expr {
	return Text{Value: value, lower: bytes.ToLower([]byte(value))}
}

func newRegexp(src string) (Expr, error) {
	re, err := compile(src)
	if err != nil {
		return nil, err
	}
	return Regexp{Source: src, re: re}, nil
}

// compile builds a case-insensitive regexp, which is what the rest of the
// language is, and what a filter typed in a hurry expects.
func compile(src string) (*regexp.Regexp, error) {
	re, err := regexp.Compile("(?i)" + src)
	if err != nil {
		return nil, errors.Wrapf(err, "bad regexp /%s/", src)
	}
	return re, nil
}
