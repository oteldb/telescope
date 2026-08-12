package ui

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// site is a place in a file, which is what a logger writes when it says where
// it was called from.
type site struct {
	path string
	line int
}

// splitSite takes the ":42" or ":42:9" a logger appends off a path.
//
// Only digits count as a line, so a value that ends in something else keeps all
// of itself: an image tag is not a line number. The column is read and dropped —
// no editor is worth the argument, and the line is what anyone wanted.
func splitSite(s string) site {
	out := site{path: s}
	for range 2 {
		i := strings.LastIndexByte(out.path, ':')
		if i <= 0 {
			break
		}
		n, err := strconv.Atoi(out.path[i+1:])
		if err != nil || n <= 0 {
			break
		}
		out = site{path: out.path[:i], line: n}
	}
	return out
}

// locator finds the files a log line names.
type locator struct {
	// dir is where a relative path is tried first, which is where telescope was
	// started and so usually the repository being worked in.
	dir string
	// root is that repository, if it is one.
	root string
	// tracked lists the files of a repository matching a glob. A function so a
	// test can say what a checkout holds without being one.
	tracked func(root, glob string) []string
}

// newLocator reads where telescope is standing. A variable so tests can put it
// somewhere of their own making.
var newLocator = func() locator {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	return locator{dir: dir, root: gitRoot(dir), tracked: gitTracked}
}

// locate finds the file a log line named, which is often not the path it wrote.
//
// zap writes the package-relative "ui/start.go"; a binary built somewhere else
// carries the build machine's absolute path; a container writes the path it had
// inside the image. None of those is where the code is now, and what they have
// in common with the checkout is their tail — so when nothing is at the path as
// written, a tracked file that ends with it is the answer.
func (l locator) locate(s site) (site, bool) {
	if s.path == "" {
		return site{}, false
	}

	// A relative path is tried against where telescope stands and against the
	// repository, never bare: a bare one would be resolved against whatever
	// directory the process happens to be in, which is the same thing by luck
	// rather than the same thing by rule.
	var tries []string
	if filepath.IsAbs(s.path) {
		tries = append(tries, s.path)
	} else {
		tries = append(tries, filepath.Join(l.dir, s.path))
		if l.root != "" {
			tries = append(tries, filepath.Join(l.root, s.path))
		}
	}
	for _, try := range tries {
		if isFile(try) {
			return site{path: try, line: s.line}, true
		}
	}

	if l.root == "" || l.tracked == nil {
		return site{}, false
	}
	// The basename bounds the listing; the rest of the path picks between what
	// comes back.
	want := components(s.path)
	if len(want) == 0 {
		return site{}, false
	}

	var (
		best  []string
		depth int
	)
	for _, f := range l.tracked(l.root, "*"+want[len(want)-1]) {
		got := components(f)
		// The most of the written path a file accounts for, so that a caller
		// naming a directory is not answered by a file of the same name at the
		// top of the tree. Then the shallowest of those, and then by name — never
		// whichever git happened to list first, since the answer has to be the
		// same one every time.
		n := overlap(want, got)
		switch {
		case n == 0 || n < depth:
			continue
		case n > depth, len(got) < len(best),
			len(got) == len(best) && path.Join(got...) < path.Join(best...):
			best, depth = got, n
		}
	}
	if best == nil {
		return site{}, false
	}
	return site{path: filepath.Join(l.root, filepath.Join(best...)), line: s.line}, true
}

// components splits a path into the names along it, dropping what says nothing
// about which file it is.
func components(p string) []string {
	var out []string
	for _, c := range strings.Split(filepath.ToSlash(p), "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out
}

// overlap is how many components two paths share at their ends, and zero unless
// one of them ends in the whole of the other.
//
// It has to hold both ways round, because the two ways a path arrives wrong are
// opposites: zap writes the package-relative "ui/start.go", which the checkout's
// "internal/ui/start.go" ends with, and a binary built elsewhere writes
// "/home/runner/work/telescope/internal/ui/start.go", which ends with the
// checkout's path instead. Matching on the basename alone would answer for any
// file that shares a name with another.
func overlap(a, b []string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[len(a)-1-i] != b[len(b)-1-i] {
			return 0
		}
	}
	return n
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func gitRoot(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitTracked lists the repository files matching glob. Only tracked files, so
// a build directory holding a copy of the tree cannot answer for it.
func gitTracked(root, glob string) []string {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--", glob).Output()
	if err != nil {
		return nil
	}
	var files []string
	for f := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}
