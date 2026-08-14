package source

import (
	"encoding/json"
	"fmt"
	"time"
)

// podStatus is the part of a pod a restart is visible in. Everything else
// kubectl reports about one is ignored: this watches a number per container.
type podStatus struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Status struct {
		ContainerStatuses []containerStatus `json:"containerStatuses"`
	} `json:"status"`
}

type containerStatus struct {
	Name         string `json:"name"`
	RestartCount int    `json:"restartCount"`
	LastState    struct {
		Terminated *struct {
			Reason     string    `json:"reason"`
			ExitCode   int       `json:"exitCode"`
			FinishedAt time.Time `json:"finishedAt"`
		} `json:"terminated"`
	} `json:"lastState"`
}

// restarts turns what a pod watch reports into the notes a reader wants.
//
// Kubernetes has no restart event to subscribe to: it reports a count per
// container and leaves the difference to whoever was counting. So this
// remembers what each container was at when it was last seen, and a container
// seen for the first time is recorded and not announced — a pod that had
// restarted twice before telescope opened did not restart just now, and saying
// so on the first frame of the watch would put an old crash in the middle of
// today's timeline.
type restarts struct {
	seen map[string]int
}

// observe reads one pod as the watch reported it and returns what is worth
// saying about it, which is usually nothing.
//
// Anything it cannot read it ignores. A watch is a long-lived stream of
// somebody else's JSON, and a frame that does not parse is a frame to skip
// rather than a reason to stop counting.
func (r *restarts) observe(obj []byte) []Line {
	var pod podStatus
	if err := json.Unmarshal(obj, &pod); err != nil || pod.Metadata.Name == "" {
		return nil
	}
	if r.seen == nil {
		r.seen = map[string]int{}
	}

	var out []Line
	for _, c := range pod.Status.ContainerStatuses {
		if c.Name == "" {
			continue
		}
		key := pod.Metadata.Name + "/" + c.Name
		was, known := r.seen[key]
		r.seen[key] = c.RestartCount
		if !known || c.RestartCount <= was {
			continue
		}
		out = append(out, restartLine(key, c, c.RestartCount-was))
	}
	return out
}

// forget drops a pod the watch says is gone, so a name Kubernetes gives to a
// different pod later is counted from its own zero rather than from the dead
// one's total.
func (r *restarts) forget(obj []byte) {
	var pod podStatus
	if err := json.Unmarshal(obj, &pod); err != nil || pod.Metadata.Name == "" {
		return
	}
	for _, c := range pod.Status.ContainerStatuses {
		delete(r.seen, pod.Metadata.Name+"/"+c.Name)
	}
}

// restartLine is one container coming back, dated when the old one died: that
// is where in the log the restart belongs, and it is a moment the lines around
// it can be read against.
func restartLine(key string, c containerStatus, delta int) Line {
	said := key
	var at time.Time
	if t := c.LastState.Terminated; t != nil {
		switch {
		case t.Reason != "":
			said += ": " + t.Reason
		default:
			said += ": exited"
		}
		said += fmt.Sprintf(" (exit %d)", t.ExitCode)
		at = t.FinishedAt
	}
	if delta > 1 {
		// The watch can miss frames, and a container that came back three times
		// while nobody was looking is not one restart.
		said += fmt.Sprintf(" · %d restarts", delta)
	}
	return Line{Kind: KindRestarted, Reason: said, Stderr: true, At: at}
}
