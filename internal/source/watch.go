package source

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// watchCommand is the command that reports what the pods behind this config are
// doing, and whether there is one worth running.
//
// Only for a kubectl place that is following: a restart is something that
// happens while somebody is watching, and a window of yesterday's logs is
// finished. Only for a pod or a selector, too — a workload target is a name
// "kubectl get pods" does not take, and resolving it to a selector is another
// round trip for a thing that is an extra to begin with.
func (c Config) watchCommand() (string, bool) {
	if c.Collector != CollectorKubectl || !c.following() {
		return "", false
	}
	target := strings.TrimSpace(c.Target)
	// "pod/api" names the same thing "kubectl get pods" takes as "api"; any
	// other kind is a workload, and the pods under it are a lookup away.
	if kind, name, ok := strings.Cut(target, "/"); ok && IsKubeKind(kind) {
		if kind != "pod" && kind != "po" {
			return "", false
		}
		target = name
	}
	if target == "" {
		return "", false
	}

	args := []string{"kubectl"}
	if k := strings.TrimSpace(c.KubeConfig); k != "" {
		args = append(args, "--kubeconfig="+Quote(k))
	}
	if k := strings.TrimSpace(c.KubeContext); k != "" {
		args = append(args, "--context="+Quote(k))
	}
	args = append(args, "get", "pods")
	if ns := strings.TrimSpace(c.Namespace); ns != "" {
		args = append(args, "-n", Quote(ns))
	}
	if strings.Contains(target, "=") {
		args = append(args, "-l", Quote(target))
	} else {
		args = append(args, Quote(target))
	}
	// --output-watch-events says which pod went away, so a name Kubernetes
	// hands to a new pod is counted from its own zero. It is also the newer
	// flag here, and a kubectl too old for it runs nothing: the annotations are
	// an extra, and the stream they annotate is not theirs to break.
	args = append(args, "-w", "--output-watch-events=true", "-o", "json")
	return c.elevated(args), true
}

// startWatch runs the pod watch beside the stream and puts what it finds into
// the same channel the lines go to.
//
// It reports nothing about itself. A watch that will not run — an old kubectl,
// a role that may read logs and not pods — costs the annotations and must not
// cost the stream or fill it with complaints about a thing the reader never
// asked for.
func startWatch(ctx context.Context, cfg Config, out chan<- Line, wg *sync.WaitGroup) {
	cmd, ok := cfg.watchCommand()
	if !ok {
		return
	}
	argv := cfg.argvFor(cmd)

	wg.Go(func() {
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		isolate(c)
		c.Cancel = func() error { return terminate(c) }
		c.WaitDelay = 2 * time.Second

		stdout, err := c.StdoutPipe()
		if err != nil {
			return
		}
		if err := c.Start(); err != nil {
			return
		}
		watchFrames(ctx, stdout, out)
		_ = c.Wait()
	})
}

// watchFrames reads pod objects off a watch and sends what each is worth
// saying.
func watchFrames(ctx context.Context, r io.Reader, out chan<- Line) {
	var seen restarts
	for frame := range podFrames(r) {
		// The event wrapper is unwrapped here rather than in the decoder: what
		// a restart is does not depend on how kubectl was asked for it.
		kind, obj := watchEvent(frame)
		if kind == "DELETED" {
			seen.forget(obj)
			continue
		}
		for _, l := range seen.observe(obj) {
			select {
			case out <- l:
			case <-ctx.Done():
				return
			}
		}
	}
}

// maxFrame bounds one pod object. A frame that never ends is a stream that is
// not the one we asked for, and reading it to the end of memory is not a way to
// find that out.
const maxFrame = 4 << 20

// podFrames yields each JSON object in a watch's output.
//
// It frames on the braces at column zero that kubectl's pretty-printed JSON
// puts one object between, rather than decoding the stream as JSON, because the
// stream is not only JSON: over ssh a pty folds the remote stderr in with it,
// and a decoder that met one warning line would never read another object. A
// frame here is skipped and the next one is still found.
func podFrames(r io.Reader) func(yield func([]byte) bool) {
	return func(yield func([]byte) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), maxFrame)

		var frame []byte
		for sc.Scan() {
			line := bytes.TrimSuffix(sc.Bytes(), []byte("\r"))
			switch {
			case frame == nil && string(line) == "{":
				frame = append(frame, line...)
			case frame == nil:
				// Between objects, which is where anything else in the stream
				// belongs.
			case string(line) == "}":
				frame = append(frame, '\n', '}')
				if !yield(frame) {
					return
				}
				frame = nil
			case len(frame) > maxFrame:
				frame = nil
			default:
				frame = append(frame, '\n')
				frame = append(frame, line...)
			}
		}
	}
}

// watchEvent unwraps the envelope --output-watch-events puts each pod in. A
// frame that is not one is the pod itself, which is what a kubectl too old for
// the flag would have answered had it run at all.
func watchEvent(frame []byte) (kind string, obj []byte) {
	var ev struct {
		Type   string          `json:"type"`
		Object json.RawMessage `json:"object"`
	}
	if err := json.Unmarshal(frame, &ev); err != nil || len(ev.Object) == 0 {
		return "", frame
	}
	return ev.Type, ev.Object
}

// reopens reports whether the end of this collector is the end of what it
// reads.
//
// For kubectl it is not: "kubectl logs -f" ends when the container does, and a
// container ending is the most ordinary thing in a cluster. Everywhere else an
// exit is an exit — journalctl -f does not stop while the journal exists, and a
// command a place named is a command that was asked to run once.
func (c Config) reopens() bool {
	return c.Collector == CollectorKubectl && c.following()
}

// resume is this config asked for what comes after at, which is how a collector
// is opened again without replaying what has been read.
func (c Config) resume(at time.Time) Config {
	if at.IsZero() {
		// Nothing dated has been read, so there is nothing to ask after: a
		// place not stamping its lines opens again as it opened.
		return c
	}
	c.Range.Since = at
	c.Range.Until = time.Time{}
	// The tail was how far back to start, and reading has since gone past it.
	c.Tail = 0
	return c
}
