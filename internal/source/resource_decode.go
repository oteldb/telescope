package source

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	maxResourceStates = 4096
	maxResourceBytes  = 32 << 20
)

type resourceWatch struct {
	states map[string][]byte
	bytes  int
}

type resourceObject struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
	} `json:"metadata"`
}

type resourceEvent struct {
	Type           string `json:"type"`
	Reason         string `json:"reason"`
	Message        string `json:"message"`
	Note           string `json:"note"`
	EventTime      string `json:"eventTime"`
	LastTimestamp  string `json:"lastTimestamp"`
	FirstTimestamp string `json:"firstTimestamp"`
	Count          int    `json:"count"`
	Series         struct {
		Count            int    `json:"count"`
		LastObservedTime string `json:"lastObservedTime"`
	} `json:"series"`
	InvolvedObject resourceReference `json:"involvedObject"`
	Regarding      resourceReference `json:"regarding"`
}

type resourceReference struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type resourceRecord struct {
	Time         time.Time        `json:"time"`
	Level        string           `json:"level"`
	Message      string           `json:"msg"`
	Event        string           `json:"watch_event"`
	APIVersion   string           `json:"api_version"`
	Kind         string           `json:"kind"`
	Namespace    string           `json:"namespace,omitempty"`
	Name         string           `json:"name"`
	Reason       string           `json:"reason,omitempty"`
	Count        int              `json:"count,omitempty"`
	ResourceKind string           `json:"resource_kind,omitempty"`
	ResourceName string           `json:"resource_name,omitempty"`
	Changes      []ResourceChange `json:"changes,omitempty"`
	Object       json.RawMessage  `json:"object"`
}

func (w *resourceWatch) observe(frame []byte, now time.Time) ([]Line, error) {
	var obj resourceObject
	if err := json.Unmarshal(frame, &obj); err != nil {
		return nil, fmt.Errorf("decode resource: %w", err)
	}
	var event string
	raw := frame
	if obj.Kind == "" {
		event, raw = watchEvent(frame)
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("decode resource: %w", err)
		}
	}
	switch event {
	case "BOOKMARK":
		return nil, nil
	case "ERROR":
		var status struct{ Message string }
		if err := json.Unmarshal(raw, &status); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("kubernetes watch: %s", status.Message)
	case "", "ADDED", "MODIFIED", "DELETED":
	default:
		return nil, fmt.Errorf("unknown Kubernetes watch event %q", event)
	}
	if event == "" && obj.Metadata.Name == "" && strings.HasSuffix(obj.Kind, "List") {
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("decode resource list: %w", err)
		}
		var lines []Line
		for _, item := range list.Items {
			got, err := w.observe(item, now)
			if err != nil {
				return nil, err
			}
			lines = append(lines, got...)
		}
		return lines, nil
	}
	if obj.Kind == "" || obj.Metadata.Name == "" {
		return nil, fmt.Errorf("resource has no kind or metadata.name")
	}
	if event == "" {
		event = "ADDED"
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	current := resourceState(state)
	key := strings.Join([]string{obj.APIVersion, obj.Kind, obj.Metadata.Namespace, obj.Metadata.Name, obj.Metadata.UID}, "/")
	previous := w.states[key]
	if event != "DELETED" && bytes.Equal(previous, current) {
		return nil, nil
	}
	if event == "ADDED" && previous != nil {
		event = "MODIFIED"
	}
	rec := resourceRecord{
		Time: now, Level: "info", Event: event, APIVersion: obj.APIVersion,
		Kind: obj.Kind, Namespace: obj.Metadata.Namespace, Name: obj.Metadata.Name, Object: raw,
	}
	name := obj.Metadata.Name
	if obj.Metadata.Namespace != "" {
		name = obj.Metadata.Namespace + "/" + name
	}
	rec.Message = event + " " + obj.Kind + " " + name
	if event == "MODIFIED" && previous != nil {
		var oldState, newState map[string]json.RawMessage
		_ = json.Unmarshal(previous, &oldState)
		_ = json.Unmarshal(current, &newState)
		rec.Changes = resourceChanges(oldState, newState, "")
	}
	if obj.Kind == "Event" && (obj.APIVersion == "v1" || strings.HasPrefix(obj.APIVersion, "events.k8s.io/")) {
		var ev resourceEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("decode Kubernetes Event: %w", err)
		}
		rec.Reason, rec.Count = ev.Reason, max(ev.Count, ev.Series.Count)
		ref := ev.InvolvedObject
		if ev.Regarding.Name != "" {
			ref = ev.Regarding
		}
		rec.ResourceKind, rec.ResourceName = ref.Kind, ref.Name
		message := ev.Message
		if ev.Note != "" {
			message = ev.Note
		}
		rec.Message = strings.TrimSpace(strings.Join([]string{ev.Reason, ref.Kind + "/" + ref.Name, message}, " "))
		if ev.Type == "Warning" {
			rec.Level = "warn"
		}
		for _, stamp := range []string{ev.Series.LastObservedTime, ev.LastTimestamp, ev.EventTime, ev.FirstTimestamp} {
			if at, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
				rec.Time = at
				break
			}
		}
	}
	if event == "MODIFIED" && previous == nil {
		rec.Message += " (previous state unavailable)"
	}
	w.remember(key, current, event == "DELETED")
	encoded, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	return []Line{{Data: encoded, At: rec.Time}}, nil
}

func (w *resourceWatch) remember(key string, state []byte, deleted bool) {
	w.bytes -= len(w.states[key])
	delete(w.states, key)
	if deleted || len(state) > maxResourceBytes {
		return
	}
	if w.states == nil {
		w.states = make(map[string][]byte)
	}
	for len(w.states) >= maxResourceStates || w.bytes+len(state) > maxResourceBytes {
		for k, v := range w.states {
			w.bytes -= len(v)
			delete(w.states, k)
			break
		}
	}
	w.states[key] = state
	w.bytes += len(state)
}

// SSH followers use a PTY, so warnings arrive between JSON objects on stdout.
// Keep them visible while framing kubectl's pretty-printed or compact JSON.
func readResourceFrames(r io.Reader, emit func([]byte, bool) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	var frame []byte
	for sc.Scan() {
		line := bytes.TrimSuffix(sc.Bytes(), []byte("\r"))
		if frame == nil {
			if bytes.Equal(line, []byte("{")) {
				frame = append(frame, line...)
				continue
			}
			if len(bytes.TrimSpace(line)) > 0 && !emit(line, !json.Valid(line)) {
				return nil
			}
			continue
		}
		frame = append(frame, '\n')
		frame = append(frame, line...)
		if len(frame) > maxLineSize {
			return fmt.Errorf("kubernetes resource exceeds %d bytes", maxLineSize)
		}
		if bytes.Equal(line, []byte("}")) {
			if !emit(frame, false) {
				return nil
			}
			frame = nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(frame) > 0 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
