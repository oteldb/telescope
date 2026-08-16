package source

import (
	"bytes"
	"strings"
)

// The names the pod and the container are reported under. They are the
// conventions' rather than kubectl's own words for them, so a line read here
// carries what the same line read out of a log database carries.
const (
	kubePodLabel       = "k8s.pod.name"
	kubeContainerLabel = "k8s.container.name"
)

// Prefixes reports whether the collector was asked to say which pod each line
// came from.
//
// It is asked wherever the target can resolve to more than one: "kubectl logs"
// on a deployment or a selector tails every pod it matches and writes whichever
// spoke first, so without this the lines of a dozen pods arrive as one
// undifferentiated stream — which is what they are not.
func (c Config) Prefixes() bool {
	return c.Collector == CollectorKubectl && kubeFansOut(c.Target)
}

// kubeFansOut reports whether a kubectl target names more than one pod: a label
// selector, or a resource that owns pods rather than a pod itself.
func kubeFansOut(target string) bool {
	t := strings.TrimSpace(target)
	if strings.Contains(t, "=") {
		return true
	}
	kind, _, ok := strings.Cut(t, "/")
	return ok && IsKubeKind(kind) && !isKubePod(kind)
}

func isKubePod(s string) bool {
	kind, _, _ := strings.Cut(strings.ToLower(s), ".")
	return kind == "po" || strings.TrimSuffix(kind, "s") == "pod"
}

// unprefix takes off the "[pod/api-7d9f-b2k4l/api] " kubectl writes in front of
// each line when it is tailing several, and reports what it said as labels.
//
// The line is left alone unless the prefix is exactly the shape kubectl writes.
// A log line may begin with a bracket for its own reasons, and one that does is
// worth more than a label guessed off it.
func unprefix(data []byte) ([]byte, []Label) {
	if len(data) == 0 || data[0] != '[' {
		return data, nil
	}
	end := bytes.IndexByte(data, ']')
	if end < 0 {
		return data, nil
	}
	inside, ok := strings.CutPrefix(string(data[1:end]), "pod/")
	if !ok {
		return data, nil
	}
	pod, container, ok := strings.Cut(inside, "/")
	if !ok || pod == "" || container == "" {
		return data, nil
	}
	rest, _ := bytes.CutPrefix(data[end+1:], []byte(" "))
	return rest, []Label{
		{Key: kubePodLabel, Value: pod},
		{Key: kubeContainerLabel, Value: container},
	}
}
