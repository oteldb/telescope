package ui

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// TestStackFramesReadsEveryRuntime: the trace is whatever the language that
// panicked writes, and the frames have to come out of all of them in the order
// they were thrown.
func TestStackFramesReadsEveryRuntime(t *testing.T) {
	for _, tt := range []struct {
		name  string
		trace string
		want  []site
	}{
		{
			name: "zap",
			trace: "github.com/oteldb/telescope/internal/ui.(*logModel).apply\n" +
				"\t/home/runner/work/telescope/internal/ui/logview.go:341\n" +
				"github.com/oteldb/telescope/internal/ui.run\n" +
				"\t/home/runner/work/telescope/internal/ui/app.go:88",
			want: []site{
				{path: "/home/runner/work/telescope/internal/ui/logview.go", line: 341},
				{path: "/home/runner/work/telescope/internal/ui/app.go", line: 88},
			},
		},
		{
			name: "go panic",
			trace: "goroutine 1 [running]:\n" +
				"main.main()\n" +
				"\t/src/main.go:12 +0x1d\n" +
				"exit status 2",
			want: []site{{path: "/src/main.go", line: 12}},
		},
		{
			name: "java",
			trace: "java.lang.IllegalStateException: boom\n" +
				"\tat com.example.Bar.baz(Bar.java:42)\n" +
				"\tat com.example.Foo.run(Foo.java:7)\n" +
				"\tat java.base/java.lang.Thread.run(Unknown Source)",
			want: []site{{path: "Bar.java", line: 42}, {path: "Foo.java", line: 7}},
		},
		{
			name: "python",
			trace: "Traceback (most recent call last):\n" +
				`  File "/src/app.py", line 12, in handler` + "\n" +
				"    raise ValueError(name)\n" +
				"ValueError: boom",
			want: []site{{path: "/src/app.py", line: 12}},
		},
		{
			name: "node",
			trace: "Error: boom\n" +
				"    at handler (/src/app.js:12:9)\n" +
				"    at /src/server.js:4:3",
			want: []site{{path: "/src/app.js", line: 12}, {path: "/src/server.js", line: 4}},
		},
		{
			name:  "prose is not a frame",
			trace: "boom: connection refused\nretrying in 5s",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, stackFrames(tt.trace))
		})
	}
}

// TestStackFramesStopsReadingEventually: a trace has no bound, and every frame
// that is not on disk costs a listing to find out.
func TestStackFramesStopsReadingEventually(t *testing.T) {
	var b strings.Builder
	for i := range 200 {
		b.WriteString("main.f\n\t/src/main.go:")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString("\n")
	}
	require.Len(t, stackFrames(b.String()), maxStackFrames)
}

// TestOpenFollowsAStacktrace: the frame worth opening is the innermost one that
// is a file in the checkout, whatever the machine that wrote it called the rest.
func TestOpenFollowsAStacktrace(t *testing.T) {
	l := repo(t, "internal/ui/logview.go")
	want := site{path: filepath.Join(l.root, "internal", "ui", "logview.go"), line: 341}

	trace := "runtime.gopanic\n" +
		"\t/usr/local/go/src/runtime/panic.go:770\n" +
		"github.com/oteldb/telescope/internal/ui.(*logModel).apply\n" +
		"\t/home/runner/work/telescope/internal/ui/logview.go:341"

	t.Run("from the row holding it", func(t *testing.T) {
		m := entryOf(t, `{"msg":"boom"}`, source.Label{Key: "stacktrace", Value: trace})
		got, ok := l.linkOf(m.entry, rowOf(t, m, "stacktrace"))
		require.True(t, ok)
		require.Equal(t, want, got.file,
			"the runtime frame above it is not in this checkout")
	})

	t.Run("and from a row that points nowhere itself", func(t *testing.T) {
		m := entryOf(t, `{"msg":"boom"}`,
			source.Label{Key: "stack_trace", Value: trace},
			source.Label{Key: "pod", Value: "api-0"},
		)
		got, ok := l.linkOf(m.entry, rowOf(t, m, "pod"))
		require.True(t, ok, "the entry came with a trace, which is what o is for")
		require.Equal(t, want, got.file)
	})

	t.Run("but not to a file that is not there", func(t *testing.T) {
		m := entryOf(t, `{"msg":"boom"}`, source.Label{
			Key:   "stacktrace",
			Value: "main.main()\n\t/src/nowhere.go:12 +0x1d",
		})
		_, ok := l.linkOf(m.entry, rowOf(t, m, "stacktrace"))
		require.False(t, ok)
	})
}
