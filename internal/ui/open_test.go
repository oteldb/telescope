package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func TestSplitSite(t *testing.T) {
	for _, tt := range []struct {
		in   string
		path string
		line int
	}{
		{"internal/ui/start.go:42", "internal/ui/start.go", 42},
		{"/src/main.go:42:9", "/src/main.go", 42},
		{"start.go", "start.go", 0},
		// Nothing that is not a line number is read as one.
		{"ghcr.io/oteldb/telescope:v0.1.0", "ghcr.io/oteldb/telescope:v0.1.0", 0},
		{"nginx:1.25", "nginx:1.25", 0},
		{"api-0:8080", "api-0", 8080},
		{"start.go:0", "start.go:0", 0},
		{`C:\src\main.go`, `C:\src\main.go`, 0},
		{"", "", 0},
	} {
		t.Run(tt.in, func(t *testing.T) {
			require.Equal(t, site{path: tt.path, line: tt.line}, splitSite(tt.in))
		})
	}
}

// repo builds a checkout to look files up in.
func repo(t *testing.T, files ...string) locator {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		full := filepath.Join(root, filepath.FromSlash(f))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("package ui\n"), 0o644))
	}
	return locator{
		dir:  root,
		root: root,
		tracked: func(_, glob string) []string {
			var out []string
			for _, f := range files {
				if ok, _ := filepath.Match(filepath.FromSlash(glob), filepath.FromSlash(f)); ok {
					out = append(out, f)
					continue
				}
				// The glob git is given matches whole paths, so a leading * has
				// to cross separators the way its pathspec does.
				if m, _ := filepath.Match(glob, filepath.Base(f)); m {
					out = append(out, f)
				}
			}
			return out
		},
	}
}

// TestLocateFindsTheFileWhereItActuallyIs: what a logger writes is a path on
// the machine that built it, and what is wanted is the same file here.
func TestLocateFindsTheFileWhereItActuallyIs(t *testing.T) {
	l := repo(t, "internal/ui/start.go", "internal/logs/parse.go", "start.go")

	t.Run("as written, relative to where telescope stands", func(t *testing.T) {
		got, ok := l.locate(site{path: "internal/ui/start.go", line: 42})
		require.True(t, ok)
		require.Equal(t, filepath.Join(l.root, "internal", "ui", "start.go"), got.path)
		require.Equal(t, 42, got.line, "and the line comes along")
	})

	t.Run("a package-relative caller, which is what zap writes", func(t *testing.T) {
		got, ok := l.locate(site{path: "ui/start.go", line: 42})
		require.True(t, ok)
		require.Equal(t, filepath.Join(l.root, "internal", "ui", "start.go"), got.path)
	})

	t.Run("an absolute path from the machine that built it", func(t *testing.T) {
		got, ok := l.locate(site{path: "/home/runner/work/telescope/internal/logs/parse.go"})
		require.True(t, ok)
		require.Equal(t, filepath.Join(l.root, "internal", "logs", "parse.go"), got.path)
	})

	t.Run("the shallowest of several, and the same one every time", func(t *testing.T) {
		got, ok := l.locate(site{path: "start.go"})
		require.True(t, ok)
		require.Equal(t, filepath.Join(l.root, "start.go"), got.path)
	})

	t.Run("nothing that is not there", func(t *testing.T) {
		_, ok := l.locate(site{path: "internal/ui/nowhere.go"})
		require.False(t, ok)

		_, ok = l.locate(site{path: "api-0"})
		require.False(t, ok, "a pod name is not a file")
	})
}

// TestLocateOutsideACheckout: telescope is not always run where the code is,
// and a path that resolves to nothing is not opened.
func TestLocateOutsideACheckout(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "here.go"), []byte("x"), 0o644))
	l := locator{dir: dir}

	got, ok := l.locate(site{path: "here.go", line: 7})
	require.True(t, ok, "what is under foot is still found")
	require.Equal(t, filepath.Join(dir, "here.go"), got.path)
	require.Equal(t, 7, got.line)

	_, ok = l.locate(site{path: "ui/start.go"})
	require.False(t, ok, "and with no repository there is nothing to search")
}

// TestLinkReadsTheLineFromWhereverItIs: a caller carries its line in the value,
// OTEL keeps it in an attribute beside the path, and both open the same place.
func TestLinkReadsTheLineFromWhereverItIs(t *testing.T) {
	l := repo(t, "internal/ui/start.go")
	want := filepath.Join(l.root, "internal", "ui", "start.go")

	t.Run("a zap caller says both in one value", func(t *testing.T) {
		m := entryOf(t, `{"msg":"boom","caller":"ui/start.go:42"}`)
		got, ok := l.linkOf(m.entry, rowOf(t, m, "caller"))
		require.True(t, ok)
		require.Equal(t, site{path: want, line: 42}, got.file)
	})

	t.Run("OTEL keeps them apart", func(t *testing.T) {
		m := entryOf(t, `{"msg":"boom"}`,
			source.Label{Key: "code.file.path", Value: "internal/ui/start.go"},
			source.Label{Key: "code.line.number", Value: "42"},
		)
		got, ok := l.linkOf(m.entry, rowOf(t, m, "code.file.path"))
		require.True(t, ok)
		require.Equal(t, site{path: want, line: 42}, got.file,
			"the line comes off the attribute beside it")

		// And from the other end: a line number opens the file it is a line of.
		got, ok = l.linkOf(m.entry, rowOf(t, m, "code.line.number"))
		require.True(t, ok)
		require.Equal(t, site{path: want, line: 42}, got.file)
	})

	t.Run("the names OTEL used to use", func(t *testing.T) {
		m := entryOf(t, `{"msg":"boom"}`,
			source.Label{Key: "code.filepath", Value: "internal/ui/start.go"},
			source.Label{Key: "code.lineno", Value: "42"},
		)
		got, ok := l.linkOf(m.entry, rowOf(t, m, "code.filepath"))
		require.True(t, ok)
		require.Equal(t, site{path: want, line: 42}, got.file)
	})

	t.Run("a path with no line at all", func(t *testing.T) {
		m := entryOf(t, `{"msg":"boom"}`,
			source.Label{Key: "code.file.path", Value: "internal/ui/start.go"})
		got, ok := l.linkOf(m.entry, rowOf(t, m, "code.file.path"))
		require.True(t, ok)
		require.Equal(t, site{path: want}, got.file, "opened at the top rather than not at all")
	})
}

// TestOnlyHTTPReachesTheBrowser: an entry is somebody else's bytes, and the
// desktop opener runs whatever program a scheme is registered to.
func TestOnlyHTTPReachesTheBrowser(t *testing.T) {
	l := repo(t)
	link := func(value string) (target, bool) {
		m := entryOf(t, `{"msg":"boom"}`, source.Label{Key: "url", Value: value})
		return l.linkOf(m.entry, rowOf(t, m, "url"))
	}

	got, ok := link("https://grafana.example/explore?traceId=abc")
	require.True(t, ok)
	require.Equal(t, "https://grafana.example/explore?traceId=abc", got.url)

	for _, hostile := range []string{
		"file:///etc/shadow",
		"javascript:alert(1)",
		"ms-msdt:/id",
		"smb://attacker/share",
	} {
		_, ok := link(hostile)
		require.False(t, ok, "%s is not handed to the desktop", hostile)
	}
}

func TestEditorArgv(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	const path = "/src/main.go"

	for _, tt := range []struct {
		editor string
		line   int
		want   []string
	}{
		{"vim", 42, []string{"vim", "+42", path}},
		{"nvim", 42, []string{"nvim", "+42", path}},
		{"nano", 42, []string{"nano", "+42", path}},
		{"emacsclient", 42, []string{"emacsclient", "+42", path}},
		{"code", 42, []string{"code", "--goto", path + ":42"}},
		{"code -w", 42, []string{"code", "-w", "--goto", path + ":42"}},
		{"hx", 42, []string{"hx", path + ":42"}},
		{"subl", 42, []string{"subl", path + ":42"}},
		{"goland", 42, []string{"goland", "--line", "42", path}},
		{"/usr/bin/vim", 42, []string{"/usr/bin/vim", "+42", path}},
		{"code.exe", 42, []string{"code.exe", "--goto", path + ":42"}},
		// An editor nobody here knows still opens the file.
		{"acme", 42, []string{"acme", path}},
		// And with no line there is nothing to say about one.
		{"vim", 0, []string{"vim", path}},
		{"code", 0, []string{"code", path}},
	} {
		t.Run(tt.editor, func(t *testing.T) {
			got, err := editorArgv(env(map[string]string{"EDITOR": tt.editor}), site{path: path, line: tt.line})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	t.Run("VISUAL wins, since EDITOR may be a line editor", func(t *testing.T) {
		got, err := editorArgv(env(map[string]string{"VISUAL": "nvim", "EDITOR": "ed"}), site{path: path})
		require.NoError(t, err)
		require.Equal(t, []string{"nvim", path}, got)
	})

	t.Run("neither is said rather than guessed at", func(t *testing.T) {
		_, err := editorArgv(env(nil), site{path: path})
		require.ErrorContains(t, err, "EDITOR")
	})
}

// TestOpenSaysWhenThereIsNothingToOpen: most rows point nowhere, and o has to
// be safe to press on them.
func TestOpenSaysWhenThereIsNothingToOpen(t *testing.T) {
	m := entryOf(t, `{"msg":"boom"}`, source.Label{Key: "pod", Value: "api-0"})
	m = press(t, selectKey(t, m, "pod"), "o")
	require.Contains(t, ansi.Strip(m.View()), "nothing here to open")
}

// TestOpenHandsAURLToTheBrowser: the one target that does not take the terminal.
func TestOpenHandsAURLToTheBrowser(t *testing.T) {
	var got string
	prev := openBrowser
	openBrowser = func(url string) error {
		got = url
		return nil
	}
	t.Cleanup(func() { openBrowser = prev })

	const url = "https://grafana.example/explore"
	m := entryOf(t, `{"msg":"boom"}`, source.Label{Key: "url", Value: url})
	m = selectKey(t, m, "url")

	m, cmd := m.Update(k("o"))
	require.NotNil(t, cmd)
	m, cmd = m.Update(cmd())
	require.NotNil(t, cmd, "the browser is asked for")
	m, _ = m.Update(cmd())

	require.Equal(t, url, got)
	require.Contains(t, ansi.Strip(m.View()), "opened "+url)
}

// TestOpenSuspendsForAnEditor: an editor wants the terminal telescope is drawing
// on, so the program has to stand aside for it.
func TestOpenSuspendsForAnEditor(t *testing.T) {
	t.Setenv("VISUAL", "vim")
	t.Setenv("EDITOR", "")

	l := repo(t, "internal/ui/start.go")
	m := entryOf(t, `{"msg":"boom","caller":"ui/start.go:42"}`)
	got, ok := l.linkOf(m.entry, rowOf(t, m, "caller"))
	require.True(t, ok)

	_, cmd := m.open(got)
	require.NotNil(t, cmd, "which only an exec can arrange")

	// With nothing configured it says so instead of choosing an editor.
	t.Setenv("VISUAL", "")
	m2, cmd := m.open(got)
	require.NotNil(t, cmd)
	m2, _ = m2.Update(cmd())
	require.Contains(t, ansi.Strip(m2.View()), "EDITOR")
}

