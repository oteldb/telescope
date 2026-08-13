package trace

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"
)

// FuzzDecodeJaeger checks that nothing a database or a proxy can put on the
// wire gets past the decoder as a panic.
//
// What it is really guarding is [Build] behind it: the recovery there is a
// pile of special cases for traces that are not trees, and every one of them
// is reachable from a document somebody could serve.
func FuzzDecodeJaeger(f *testing.F) {
	files, err := filepath.Glob(filepath.Join("testdata", "checkout*.json"))
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
			tr.Walk(func(n *Node) bool {
				if n.Duration < 0 {
					t.Fatalf("span %s lasted %s", n.SpanID, n.Duration)
				}
				return true
			})
			tr.ClampSkew()
			Fit(tr)
		}
	})
}

// FuzzDecodeOTLP checks the same for the OTLP payloads, in both the encodings
// telescope reads and against bytes that are neither.
//
// The sniff between them is one byte, so anything can arrive at either decoder,
// and the JSON path rewrites the document before it parses it — a rewrite that
// walks arbitrary nesting is exactly the shape that recurses off a cliff on
// input nobody wrote by hand.
func FuzzDecodeOTLP(f *testing.F) {
	files, err := filepath.Glob(filepath.Join("testdata", "otlp-*.json"))
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
	pb, err := proto.Marshal(sampleTracesData())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(pb)
	for _, seed := range []string{
		`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7"}]}]}]}`,
		`{"trace":{"resourceSpans":[]}}`,
		`{"resourceSpans":[{"scopeSpans":[{"spans":[{"spanId":"","parentSpanId":"zzzz"}]}]}]}`,
		`{"resourceSpans":[{"scopeSpans":[{"spans":[{"startTimeUnixNano":"18446744073709551615","endTimeUnixNano":"0"}]}]}]}`,
		`{}`,
		``,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		found, err := DecodeOTLP(data)
		if err != nil {
			return
		}
		for _, tr := range found {
			var seen int
			tr.Walk(func(*Node) bool {
				seen++
				return true
			})
			if seen != tr.Len() {
				t.Fatalf("walked %d of %d spans", seen, tr.Len())
			}
			// A duration that ran backwards would make a bar the arithmetic
			// cannot place.
			tr.Walk(func(n *Node) bool {
				if n.Duration < 0 {
					t.Fatalf("span %s lasted %s", n.SpanID, n.Duration)
				}
				return true
			})
			tr.ClampSkew()
			Fit(tr)
		}
	})
}
