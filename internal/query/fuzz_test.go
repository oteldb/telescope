package query

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

// FuzzParse checks that a query survives being written back and read again.
//
// The status bar shows a filter as [Expr.String] writes it, and the prompt
// takes it back: a query that does not round-trip is one the view describes as
// something it is not.
func FuzzParse(f *testing.F) {
	for _, tt := range parseTests {
		f.Add(tt.query)
	}
	for _, s := range []string{
		"(a or b) and not c", `msg="a\"b"`, `/\d+/`, "level!=info", "a=b=c",
	} {
		f.Add(s)
	}

	rec := record{
		text:     []string{"connection reset", "pod=api"},
		fields:   map[string]string{"pod": "api"},
		level:    zapcore.WarnLevel,
		hasLevel: true,
	}
	f.Fuzz(func(t *testing.T, query string) {
		e, err := Parse(query)
		if err != nil {
			return
		}
		if e == nil {
			return
		}
		Match(e, rec)

		written := e.String()
		again, err := Parse(written)
		if err != nil {
			t.Fatalf("wrote back %q, which does not parse: %v", written, err)
		}
		if again == nil {
			t.Fatalf("wrote back %q, which parses as nothing", written)
		}
		if got := again.String(); got != written {
			t.Fatalf("wrote back %q, which writes back %q", written, got)
		}
	})
}
