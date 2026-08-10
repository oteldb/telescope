package logs

import (
	"regexp"
	"strings"
)

// ANSI SGR sequences used for unstructured lines. Raw escapes keep this
// consistent with pl's own output, which telescope passes through untouched.
const (
	ansiReset = "\x1b[0m"
	ansiTime  = "\x1b[38;5;245m"
	ansiNum   = "\x1b[38;5;180m"
	ansiStr   = "\x1b[38;5;108m"
	ansiPath  = "\x1b[38;5;110m"
)

var levelColors = map[string]string{
	"trace": "\x1b[38;5;244m",
	"debug": "\x1b[38;5;244m",
	"info":  "\x1b[38;5;39m",
	"warn":  "\x1b[38;5;214m",
	"error": "\x1b[38;5;203m",
	"fatal": "\x1b[38;5;201m",
	"panic": "\x1b[38;5;201m",
}

// highlightRe matches the tokens worth coloring in an unstructured line.
// Order matters: the longest, most specific alternatives come first.
var highlightRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`(?P<str>"(?:[^"\\]|\\.)*")`,
	`(?P<ts>\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)`,
	`(?P<clock>\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b)`,
	`(?P<level>\b(?:TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|ERR|FATAL|PANIC|CRITICAL|CRIT)\b)`,
	`(?P<path>\b(?:/[\w.@%+-]+){2,}(?::\d+)?)`,
	`(?P<num>\b\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h|[kmg]i?b)?\b)`,
}, "|"))

// levelGroup is the index of the "level" capture group, resolved once.
var groupNames = highlightRe.SubexpNames()

// Highlight colors an unstructured line. It is a no-op for lines that already
// carry ANSI escapes, so upstream coloring is never doubled.
func Highlight(line string) string {
	if strings.ContainsRune(line, 0x1b) {
		return line
	}
	matches := highlightRe.FindAllStringSubmatchIndex(line, -1)
	if len(matches) == 0 {
		return line
	}

	var b strings.Builder
	b.Grow(len(line) + len(matches)*12)
	last := 0
	for _, m := range matches {
		name, start, end := matchedGroup(m)
		if name == "" {
			continue
		}
		b.WriteString(line[last:start])
		b.WriteString(colorFor(name, line[start:end]))
		b.WriteString(line[start:end])
		b.WriteString(ansiReset)
		last = end
	}
	b.WriteString(line[last:])
	return b.String()
}

// matchedGroup returns which named alternative of highlightRe matched.
func matchedGroup(m []int) (name string, start, end int) {
	for i := 1; i < len(groupNames); i++ {
		if s, e := m[2*i], m[2*i+1]; s >= 0 {
			return groupNames[i], s, e
		}
	}
	return "", 0, 0
}

func colorFor(group, text string) string {
	switch group {
	case "str":
		return ansiStr
	case "ts", "clock":
		return ansiTime
	case "path":
		return ansiPath
	case "num":
		return ansiNum
	case "level":
		l := strings.ToLower(text)
		switch l {
		case "warning":
			l = "warn"
		case "err", "critical", "crit":
			l = "error"
		}
		if c, ok := levelColors[l]; ok {
			return c
		}
	}
	return ""
}
