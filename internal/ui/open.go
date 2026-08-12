package ui

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/logs"
)

// target is what a value turned out to point at.
type target struct {
	url  string
	file site
}

// Where a line number lives. OTEL keeps it in an attribute of its own beside
// the path, under the names it has had; zap and friends write "start.go:42" and
// there is nothing to look up.
var (
	fileKeys = []string{"code.file.path", "code.filepath"}
	lineKeys = []string{"code.line.number", "code.lineno"}
)

// linkOf says what the selected row points at, reading the rest of the entry
// where the row alone does not say: a path and the line in it are one value in
// a zap caller and two attributes in an OTEL record, and the same key opens the
// same file either way.
func (l locator) linkOf(e *logs.Entry, it item) (target, bool) {
	if isHTTP(it.value) {
		return target{url: it.value}, true
	}

	s := splitSite(it.value)
	switch {
	// A line number on its own row opens the file it is a line of.
	case anyKey(it.key, lineKeys):
		path, ok := fieldOf(e, fileKeys)
		n, err := strconv.Atoi(strings.TrimSpace(it.value))
		if !ok || err != nil {
			return target{}, false
		}
		s = site{path: path, line: n}
	case anyKey(it.key, fileKeys) && s.line == 0:
		if n, ok := lineOf(e); ok {
			s.line = n
		}
	}

	found, ok := l.locate(s)
	if !ok {
		return target{}, false
	}
	return target{file: found}, true
}

// isHTTP reports whether a value is a URL worth handing to a browser.
//
// Only http and https. Everything in an entry is somebody else's bytes, and the
// desktop opener will launch a handler for whatever scheme it is given — a log
// line is not allowed to choose which program runs.
func isHTTP(v string) bool {
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

func anyKey(key string, names []string) bool {
	for _, n := range names {
		if strings.EqualFold(key, n) {
			return true
		}
	}
	return false
}

func fieldOf(e *logs.Entry, names []string) (string, bool) {
	for _, n := range names {
		if v, ok := e.Field(n); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

func lineOf(e *logs.Entry) (int, bool) {
	v, ok := fieldOf(e, lineKeys)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// openBrowser hands a URL to the desktop. A variable so tests do not launch
// one, and Run rather than Start because the opener returns as soon as it has
// handed the URL on — waiting is what reaps it.
var openBrowser = func(url string) error {
	argv := browserArgv(url)
	if err := exec.Command(argv[0], argv[1:]...).Run(); err != nil {
		return errors.Wrapf(err, "run %s", argv[0])
	}
	return nil
}

func browserArgv(url string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"open", url}
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", url}
	default:
		return []string{"xdg-open", url}
	}
}

// editorArgv builds the command that opens a site in the configured editor.
//
// VISUAL before EDITOR, since EDITOR is allowed to be a line editor and this
// needs a screen. Neither set is reported rather than guessed at: dropping
// somebody into vi they did not ask for, inside a full-screen program they
// cannot see it from, is worse than saying so.
func editorArgv(env func(string) string, s site) ([]string, error) {
	editor := env("VISUAL")
	if editor == "" {
		editor = env("EDITOR")
	}
	if strings.TrimSpace(editor) == "" {
		return nil, errors.New("no $VISUAL or $EDITOR to open it with")
	}

	argv := strings.Fields(editor)
	if s.line <= 0 {
		return append(argv, s.path), nil
	}
	line := strconv.Itoa(s.line)

	// Editors agree on nothing here, and one given the wrong form opens a file
	// named after the line number.
	name := strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
	switch name {
	case "code", "code-insiders", "codium", "cursor", "windsurf":
		return append(argv, "--goto", s.path+":"+line), nil
	case "hx", "helix", "subl", "sublime_text":
		return append(argv, s.path+":"+line), nil
	case "idea", "goland", "pycharm", "webstorm", "rustrover", "phpstorm":
		return append(argv, "--line", line, s.path), nil
	case "vi", "vim", "nvim", "nano", "emacs", "emacsclient", "kak", "micro":
		return append(argv, "+"+line, s.path), nil
	default:
		// An editor nobody here knows opens the file, which is most of the way
		// there and cannot be wrong.
		return append(argv, s.path), nil
	}
}

// openCmd works out what a value points at. It reads the disk and may ask git,
// so it is a command and not part of an update.
func openCmd(e *logs.Entry, it item) tea.Cmd {
	return func() tea.Msg {
		t, ok := newLocator().linkOf(e, it)
		if !ok {
			return noteMsg{"nothing here to open"}
		}
		return openMsg{target: t}
	}
}

// openMsg is a value that turned out to point somewhere, on its way to whatever
// opens it. The editor takes the terminal, which only an update can arrange.
type openMsg struct{ target target }
