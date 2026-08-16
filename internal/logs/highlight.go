package logs

import (
	"regexp"
	"strings"
)

// ANSI SGR sequences used for unstructured lines. Raw escapes keep this
// consistent with pl's own output, which telescope passes through untouched.
const (
	ansiReset = "\x1b[0m"
	// A message is the sentence on the row and the fields beside it are not, so
	// it is bold and they are not. pl says the same thing the same way.
	ansiBold = "\x1b[1m"
	// A field's name, where it has to be written out at all. pl paints one this
	// color, and a row this composed sitting above a row pl rendered should not
	// look like it came from somewhere else.
	ansiKey  = "\x1b[36m"
	ansiTime = "\x1b[38;5;245m"
	ansiNum  = "\x1b[38;5;180m"
	ansiStr  = "\x1b[38;5;108m"
	ansiPath = "\x1b[38;5;110m"

	// An outcome is colored by whether it is one to worry about, so a screenful
	// of requests reads as a shape before it reads as text.
	ansiOK   = "\x1b[38;5;114m"
	ansiInfo = "\x1b[38;5;39m"
	ansiWarn = "\x1b[38;5;214m"
	ansiErr  = "\x1b[38;5;203m"

	// A name is what a line is about rather than how it went: the verb, the
	// service, the topic.
	ansiMethod = "\x1b[38;5;39m"
	ansiName   = "\x1b[38;5;176m"

	// Kubernetes draws each of its dimensions apart, so two lines about one pod
	// carry the same color whether they came out of a label or out of the text.
	ansiNamespace = "\x1b[38;5;141m"
	ansiPod       = "\x1b[38;5;111m"
	ansiContainer = "\x1b[38;5;79m"
	ansiNode      = "\x1b[38;5;144m"
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

// klogPattern is the prefix klog writes, as kubectl and every control plane
// component hand it over: a severity letter, the date and clock run together,
// the thread, and where in the source it was logged from.
const klogPattern = `(?:^)(?P<klogsev>(?-i:[IWEF]))(?P<klogtime>\d{4} \d{2}:\d{2}:\d{2}(?:\.\d+)?)(?:\s+\d+\s+)(?P<klogsrc>[\w./-]+\.go:\d+)(?:\])`

// requestPattern is the request line of an access log and the status that came
// back. It is tried before the string alternative because the request is itself
// a quoted string, and reading it as one would hide the two things in it worth
// telling apart.
const requestPattern = `(?:")(?P<method>(?-i:GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|TRACE|CONNECT))(?:\s+)(?P<route>[^"\s]*)(?:\s+HTTP/\d(?:\.\d)?")(?:\s+(?P<status>[1-5]\d{2})\b)?`

// bareRequestPattern is the same pair written without the quotes, which is what
// a framework's own request log looks like.
const bareRequestPattern = `(?P<bmethod>(?-i:GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS))(?:\s+)(?P<broute>/[^\s"]*)`

// statusPattern is a status code named by the word in front of it. A bare three
// digit number says nothing; the same number after "status=" does.
const statusPattern = `(?:\b(?:status|status_code|http_status|code)\s*[=:]\s*)(?P<lstatus>[1-5]\d{2})\b`

// grpcPattern is a gRPC status by name. The bare words — OK, UNKNOWN, INTERNAL
// — are deliberately absent: they are ordinary English in an ordinary sentence,
// and a viewer that painted every "internal" red would be wrong most of the
// time.
const grpcPattern = `(?P<grpc>(?-i:CANCELLED|CANCELED|` + //nolint:misspell // google.rpc.Code.CANCELLED is spelled this way.
	`INVALID_ARGUMENT|DEADLINE_EXCEEDED|NOT_FOUND|ALREADY_EXISTS|PERMISSION_DENIED|RESOURCE_EXHAUSTED|FAILED_PRECONDITION|ABORTED|OUT_OF_RANGE|UNIMPLEMENTED|UNAVAILABLE|DATA_LOSS|UNAUTHENTICATED))\b`

// reasonPattern is the reason of a Kubernetes event, which is the word the
// reader of `kubectl get events` is actually scanning for. Matching is
// case-sensitive: these are identifiers, and the lowercase spelling of most of
// them is a word a sentence may use for something else entirely.
const reasonPattern = `(?P<reason>(?-i:BackOff|Killing|Unhealthy|FailedScheduling|FailedMount|FailedAttachVolume|FailedCreatePodSandBox|FailedKillPod|CrashLoopBackOff|ImagePullBackOff|ErrImagePull|OOMKilling|Evicted|Preempted|Unschedulable|NodeNotReady|NodeReady|SandboxChanged|SuccessfulCreate|SuccessfulDelete|ScalingReplicaSet|BackoffLimitExceeded))\b`

// The Kubernetes coordinates as text names them. klog writes them as fields on
// a line it otherwise formats itself, so they are found by the key in front of
// the value exactly as a structured field is.
const (
	namespacePattern = `(?:\b(?:k8s[._])?namespace(?:[._]?name)?\s*[=:]\s*"?)(?P<ns>[\w.-]+)`
	podPattern       = `(?:\b(?:k8s[._])?pod(?:[._]?name)?\s*[=:]\s*"?)(?P<pod>[\w.-]+(?:/[\w.-]+)?)`
	containerPattern = `(?:\b(?:k8s[._])?container(?:[._]?name)?\s*[=:]\s*"?)(?P<container>[\w.-]+)`
	nodePattern      = `(?:\b(?:k8s[._])?node(?:[._]?name)?\s*[=:]\s*"?)(?P<node>[\w.-]+)`
)

// highlightRe matches the tokens worth coloring in an unstructured line.
// Order matters: the longest, most specific alternatives come first.
//
// An alternative colors its named groups and not the whole of what it matched,
// so a pattern can take in the context that makes a token recognizable — the
// key in front of a value, the quotes around a request — and leave it uncolored.
var highlightRe = regexp.MustCompile(`(?im)` + strings.Join([]string{
	klogPattern,
	requestPattern,
	`(?P<str>"(?:[^"\\]|\\.)*")`,
	`(?P<ts>\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)`,
	`(?P<clock>\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b)`,
	`(?P<level>\b(?:TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|ERR|FATAL|PANIC|CRITICAL|CRIT)\b)`,
	grpcPattern,
	reasonPattern,
	namespacePattern,
	podPattern,
	containerPattern,
	nodePattern,
	statusPattern,
	bareRequestPattern,
	`(?P<path>\b(?:/[\w.@%+-]+){2,}(?::\d+)?)`,
	`(?P<num>\b\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h|[kmg]i?b)?\b)`,
}, "|"))

// groupNames names each capture group by its index, resolved once.
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
		for i := 1; i < len(groupNames); i++ {
			start, end := m[2*i], m[2*i+1]
			if start < last || end <= start {
				continue
			}
			color := colorFor(groupNames[i], line[start:end])
			if color == "" {
				continue
			}
			b.WriteString(line[last:start])
			b.WriteString(color)
			b.WriteString(line[start:end])
			b.WriteString(ansiReset)
			last = end
		}
	}
	b.WriteString(line[last:])
	return b.String()
}

// klogSeverity is the letter klog leads a line with.
var klogSeverity = map[string]string{
	"I": "info",
	"W": "warn",
	"E": "error",
	"F": "fatal",
}

// reasonColors read an event by what it means for the thing it happened to:
// what went wrong, what is still going wrong, and what worked.
var reasonColors = map[string]string{
	"BackOff":                ansiWarn,
	"Killing":                ansiWarn,
	"Unhealthy":              ansiWarn,
	"Preempted":              ansiWarn,
	"SandboxChanged":         ansiWarn,
	"ImagePullBackOff":       ansiWarn,
	"FailedScheduling":       ansiErr,
	"FailedMount":            ansiErr,
	"FailedAttachVolume":     ansiErr,
	"FailedCreatePodSandBox": ansiErr,
	"FailedKillPod":          ansiErr,
	"CrashLoopBackOff":       ansiErr,
	"ErrImagePull":           ansiErr,
	"OOMKilling":             ansiErr,
	"Evicted":                ansiErr,
	"Unschedulable":          ansiErr,
	"NodeNotReady":           ansiErr,
	"BackoffLimitExceeded":   ansiErr,
	"NodeReady":              ansiOK,
	"SuccessfulCreate":       ansiOK,
	"SuccessfulDelete":       ansiOK,
	"ScalingReplicaSet":      ansiOK,
}

func colorFor(group, text string) string {
	switch group {
	case "str":
		return ansiStr
	case "ts", "clock", "klogtime":
		return ansiTime
	case "path", "klogsrc", "route", "broute":
		return ansiPath
	case "num":
		return ansiNum
	case "method", "bmethod":
		return ansiMethod
	case "status", "lstatus":
		return httpStatusColor(text)
	case "grpc":
		return grpcNameColor(strings.ToUpper(text))
	case "reason":
		return reasonColors[text]
	case "ns":
		return ansiNamespace
	case "pod":
		return ansiPod
	case "container":
		return ansiContainer
	case "node":
		return ansiNode
	case "klogsev":
		return levelColors[klogSeverity[strings.ToUpper(text)]]
	case "level":
		l := strings.ToLower(text)
		switch l {
		case "warning":
			l = "warn"
		case "notice":
			l = "info"
		case "err", "critical", "crit":
			l = "error"
		}
		if c, ok := levelColors[l]; ok {
			return c
		}
	}
	return ""
}
