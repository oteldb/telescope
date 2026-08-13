package trace

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzDecodeJaeger checks that nothing a database or a proxy can put on the
// wire gets past the decoder as a panic.
//
// What it is really guarding is [Build] behind it: the recovery there is a
// pile of special cases for traces that are not trees, and every one of them
// is reachable from a document somebody could serve.
func FuzzDecodeJaeger(f *testing.F) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil {
		f.Fatal(err)
	}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, seed := range []string{
		`{"data":[{"traceID":"t","spans":[{"spanID":"a","references":[{"refType":"CHILD_OF","spanID":"a"}]}]}]}`,
		`{"traceID":"t","spans":[{"spanID":"a","startTime":0,"duration":-1}]}`,
		`{"traceID":"t","spans":[{"spanID":"a","references":[{"refType":"CHILD_OF","spanID":"b"}]},
			{"spanID":"b","references":[{"refType":"CHILD_OF","spanID":"a"}]}]}`,
		`{"data":[]}`,
		`{}`,
		``,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		found, err := DecodeJaeger(data)
		if err != nil {
			return
		}
		for _, tr := range found {
			// Every span is reachable or the tree lost one, and the walk itself
			// is what a cycle would hang.
			var seen int
			tr.Walk(func(*Node) bool {
				seen++
				return true
			})
			if seen != tr.Len() {
				t.Fatalf("walked %d of %d spans", seen, tr.Len())
			}
			if rows := len(tr.Rows(nil)); rows != tr.Len() {
				t.Fatalf("drew %d of %d spans", rows, tr.Len())
			}
			tr.ClampSkew()
			Fit(tr)
		}
	})
}
