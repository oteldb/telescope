package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// entryOf opens the entry view on one line, at a size everything fits in.
func entryOf(t *testing.T, line string, labels ...source.Label) entryModel {
	t.Helper()
	e := logs.NewStore(10).Append(source.Line{Data: []byte(line), Labels: labels})
	m := newEntry(source.Config{Collector: source.CollectorDocker, Container: "app"}, e)
	m.resize(100, 40)
	return m
}

// stubCopy takes the clipboard away from the terminal running the tests and
// points it at a string.
func stubCopy(t *testing.T, got *string) {
	t.Helper()
	prev := copyValue
	copyValue = func(s string) error {
		*got = s
		return nil
	}
	t.Cleanup(func() { copyValue = prev })
}

// press delivers a key and runs whatever it asked for, which is how a copy
// reports back what it did.
func press(t *testing.T, m entryModel, key string) entryModel {
	t.Helper()
	m, cmd := m.Update(k(key))
	if cmd == nil {
		return m
	}
	msg := cmd()
	if _, ok := msg.(noteMsg); !ok {
		return m
	}
	m, _ = m.Update(msg)
	return m
}

// selected is the key of the row the cursor is on.
func selected(m entryModel) string {
	doc := m.document(m.docWidth())
	sel := picks(doc)
	if len(sel) == 0 {
		return ""
	}
	return doc[sel[m.clamp(sel)]].key
}

// TestEntryCursorWalksTheAttributes: the cursor stops on the things an entry is
// made of, and not on the headings between them.
func TestEntryCursorWalksTheAttributes(t *testing.T) {
	m := entryOf(t, `{"level":"info","msg":"boom","caller":"internal/ui/start.go:42"}`)
	require.Equal(t, "received", selected(m), "the cursor starts on the first row, not on the title")

	var walked []string
	for range len(picks(m.document(m.docWidth()))) + 1 {
		walked = append(walked, selected(m))
		m = press(t, m, "down")
	}
	require.NotContains(t, walked, "", "no heading and no blank line takes the cursor")
	require.Subset(t, walked, []string{"received", "level", "body", "caller", "rendered", "raw"})
	require.Equal(t, "raw", walked[len(walked)-1], "and the last row holds")

	m = press(t, m, "up")
	require.Equal(t, "caller", selected(m), "up steps back to the field above the raw line")

	m = press(t, m, "g")
	require.Equal(t, "received", selected(m))
	require.Zero(t, m.off, "and the frame is back at the top of the document")
}

// TestEntryLeavesOutWhatTheLineDoesNotHave: a line with no trace is a line
// with no trace_id row. Drawing the label with nothing after it invites the
// cursor onto it and narrowing by it asks for a value nothing carries.
func TestEntryLeavesOutWhatTheLineDoesNotHave(t *testing.T) {
	m := entryOf(t, `{"msg":"boom"}`)

	var keys []string
	for _, idx := range picks(m.document(m.docWidth())) {
		keys = append(keys, m.document(m.docWidth())[idx].key)
	}
	require.NotContains(t, keys, "trace_id")
	require.NotContains(t, keys, "span_id")
	require.NotContains(t, ansi.Strip(m.View()), "trace_id")

	traced := entryOf(t, `{"msg":"boom","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"}`)
	require.Contains(t, ansi.Strip(traced.View()), "4bf92f3577b34da6a3ce929d0e0e4736")
}

// TestEntryMarksWhatIsSelected: a cursor nobody can see is not a cursor.
func TestEntryMarksWhatIsSelected(t *testing.T) {
	m := entryOf(t, `{"msg":"boom","caller":"internal/ui/start.go:42"}`)

	painted := func(m entryModel) []string {
		var out []string
		for l := range strings.SplitSeq(m.View(), "\n") {
			if len(bgSeqs(l, -1)) > 0 {
				out = append(out, ansi.Strip(l))
			}
		}
		return out
	}

	rows := painted(selectKey(t, m, "caller"))
	require.Len(t, rows, 1, "one row is selected, and it is one line long")
	require.Contains(t, rows[0], "internal/ui/start.go:42")

	rows = painted(selectKey(t, m, "body"))
	require.Len(t, rows, 1)
	require.Contains(t, rows[0], "boom")
	require.NotContains(t, rows[0], "start.go", "the row beside it is not selected")
}

// selectKey puts the cursor on the row for key.
func selectKey(t *testing.T, m entryModel, key string) entryModel {
	t.Helper()
	doc := m.document(m.docWidth())
	for i, idx := range picks(doc) {
		if doc[idx].key == key {
			m.sel = i
			return m
		}
	}
	t.Fatalf("no row for %q", key)
	return m
}

// TestCopyTakesTheValueAsReceived: what is copied is what the line said, not
// what the screen made of it. A path wrapped to the frame is not a path, and a
// value escaped for display is not the value.
func TestCopyTakesTheValueAsReceived(t *testing.T) {
	var got string
	stubCopy(t, &got)

	m := entryOf(t, `{"msg":"boom","caller":"internal/ui/start.go:42"}`)
	m = press(t, selectKey(t, m, "caller"), "y")

	require.Equal(t, "internal/ui/start.go:42", got)
	require.Contains(t, ansi.Strip(m.View()), "copied caller", "and it says so")

	// A long value is wrapped across the frame; the newlines belong to the
	// drawing and must not follow it out.
	long := strings.Repeat("abcdefghij", 30)
	m = entryOf(t, `{"msg":"boom"}`, source.Label{Key: "url", Value: long})
	press(t, selectKey(t, m, "url"), "y")
	require.Equal(t, long, got)

	// An escape is shown as an escape, and copied as the byte it is.
	m = entryOf(t, `{"msg":"boom"}`, source.Label{Key: "note", Value: "one\ntwo"})
	press(t, selectKey(t, m, "note"), "y")
	require.Equal(t, "one\ntwo", got)
	require.Contains(t, ansi.Strip(m.View()), `one\ntwo`, "even though the screen shows it escaped")
}

// TestCopyEntryTakesTheWholeLine: Y is the whole entry as it arrived, from
// wherever the cursor is standing.
func TestCopyEntryTakesTheWholeLine(t *testing.T) {
	var got string
	stubCopy(t, &got)

	const line = `{"level":"info","msg":"boom","caller":"internal/ui/start.go:42"}`
	m := entryOf(t, line)
	m = press(t, selectKey(t, m, "level"), "Y")

	require.Equal(t, line, got, "the line, not the pretty-printed rendering of it")
	require.Contains(t, ansi.Strip(m.View()), "copied entry")

	// The raw row is the same thing under the cursor.
	got = ""
	press(t, selectKey(t, m, "raw"), "y")
	require.Equal(t, line, got)
}

// TestCopyReportsWhatWentWrong: the clipboard cannot be read back, so a failed
// copy that says nothing is a value the user believes they have.
func TestCopyReportsWhatWentWrong(t *testing.T) {
	prev := copyValue
	copyValue = func(string) error { return os.ErrPermission }
	t.Cleanup(func() { copyValue = prev })

	m := entryOf(t, `{"msg":"boom"}`)
	m = press(t, m, "y")
	require.Contains(t, ansi.Strip(m.View()), "could not copy")

	m = press(t, m, "down")
	require.NotContains(t, ansi.Strip(m.View()), "could not copy", "and the next key clears it")
}

// TestCopyGoesThroughTheSessionsOwnTool: telescope is the local end of what it
// reads, so a clipboard program on this machine is a pipe and nothing more.
func TestCopyGoesThroughTheSessionsOwnTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh to stand in for a clipboard program")
	}
	out := filepath.Join(t.TempDir(), "clipboard")
	require.NoError(t, copyTo([]string{"sh", "-c", "cat > " + out}, "internal/ui/start.go:42"))

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "internal/ui/start.go:42", string(got))
}

// TestCopyFallsBackToTheTerminal: with no clipboard of its own — telescope run
// over ssh on the far side — the terminal is the only thing that can reach one.
func TestCopyFallsBackToTheTerminal(t *testing.T) {
	stdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = stdout })

	require.NoError(t, copyTo(nil, "boom"))
	require.NoError(t, w.Close())

	buf := make([]byte, 64)
	n, _ := r.Read(buf)
	require.Contains(t, string(buf[:n]), "\x1b]52;", "an OSC 52 copy")
}

// TestClipboardToolFollowsTheDisplay: a clipboard program is only a clipboard
// program where there is a display for it to talk to.
func TestClipboardToolFollowsTheDisplay(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the display variables only decide this on linux")
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	require.Nil(t, clipboardTool(), "nothing to pipe to, so OSC 52 it is")

	t.Setenv("DISPLAY", ":0")
	require.Equal(t, []string{"xclip", "-selection", "clipboard"}, clipboardTool())

	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	require.Equal(t, []string{"wl-copy"}, clipboardTool(), "wayland wins where both are set")
}

// TestEntryTallValueShowsItsHead: a stacktrace is one row and taller than the
// frame, and scrolling to its end would put its first line off the top.
func TestEntryTallValueShowsItsHead(t *testing.T) {
	stack := strings.Repeat("goroutine 1 [running]\\n\\tmain.main()\\n", 20)
	m := entryOf(t, `{"msg":"boom","stack":"`+stack+`"}`)
	m.resize(100, 12)
	m = selectKey(t, m, "rendered")

	require.Contains(t, ansi.Strip(m.View()), "boom", "the head of the value, not its tail")
}
