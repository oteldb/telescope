package source

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func resourceFrame(event, spec string) string {
	return fmt.Sprintf(`{"type":%q,"object":{"apiVersion":"test.example/v1","kind":"Cluster","metadata":{"name":"demo","namespace":"octo","uid":"cluster-1"},"spec":%s}}`, event, spec)
}

func resourceRecords(t *testing.T, w *resourceWatch, frame string) []resourceRecord {
	t.Helper()
	lines, err := w.observe([]byte(frame), time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	var records []resourceRecord
	for _, l := range lines {
		var rec resourceRecord
		require.NoError(t, json.Unmarshal(l.Data, &rec))
		require.Equal(t, rec.Time, l.At)
		records = append(records, rec)
	}
	return records
}

func TestResourceWatchCommand(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, target, want string
		follow             bool
	}{
		{"events by default", "", "kubectl get events --watch --output-watch-events=true -o json", true},
		{"all namespaces", "ns:* events", "kubectl get events --all-namespaces --watch --output-watch-events=true -o json", true},
		{"custom resource", "ns:octo clusters.cluster.x-k8s.io", "kubectl get clusters.cluster.x-k8s.io --namespace=octo --watch --output-watch-events=true -o json", true},
		{"one object snapshot", "ns:octo deployments/api", "kubectl get deployments/api --namespace=octo -o json", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := (Config{Collector: CollectorKubeWatch, Follow: tt.follow}).WithTarget(tt.target)
			require.NoError(t, cfg.Validate())
			require.Equal(t, tt.want, cfg.Command())
			require.False(t, cfg.CanPage())
			require.False(t, cfg.Stamps())
			_, ok := cfg.watchCommand()
			require.False(t, ok, "the primary resource watch needs no auxiliary pod watch")
		})
	}
	cfg := Config{
		Collector: CollectorKubeWatch, KubeConfig: "/config with space", KubeContext: "prod",
		Transport: TransportSSH, Host: "bastion", Elevate: true, Follow: true,
	}
	require.Contains(t, cfg.Command(), "sudo -n kubectl --kubeconfig='/config with space' --context=prod get events")
	args := cfg.Argv()
	require.Equal(t, "ssh", args[0])
	require.Equal(t, "bastion", args[len(args)-2])
	require.Equal(t, cfg.Command(), args[len(args)-1])
	require.Equal(t, cfg, cfg.resume(time.Now()))
}

func TestResourceWatchRejectsInvalidTargetsAndHistory(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"--raw=/secrets", "pods,secrets", "pods foo", "pods/", "x/y/z", "$(touch nope)"} {
		t.Run(target, func(t *testing.T) {
			require.Error(t, (Config{Collector: CollectorKubeWatch, Target: target}).Validate())
		})
	}
	require.ErrorContains(t, (Config{
		Collector: CollectorKubeWatch, Range: Range{Since: time.Now()},
	}).Validate(), "no historical time range")
	require.Error(t, (Config{Collector: CollectorKubeWatch, Container: "api"}).Validate())
}

func TestResourceWatchShowsChanges(t *testing.T) {
	t.Parallel()
	var w resourceWatch
	first := resourceRecords(t, &w, resourceFrame("ADDED", `{"replicas":1,"nullable":null,"remove":"old","big":9007199254740993}`))
	require.Len(t, first, 1)
	require.Equal(t, "ADDED Cluster octo/demo", first[0].Message)
	modified := resourceRecords(t, &w, resourceFrame("MODIFIED", `{"replicas":2,"nullable":"set","new":null,"big":9007199254740994}`))
	require.Len(t, modified, 1)
	require.Equal(t, []ResourceChange{
		{Path: "/spec/big", Before: json.RawMessage(`9007199254740993`), After: json.RawMessage(`9007199254740994`)},
		{Path: "/spec/new", After: json.RawMessage(`null`)},
		{Path: "/spec/nullable", Before: json.RawMessage(`null`), After: json.RawMessage(`"set"`)},
		{Path: "/spec/remove", Before: json.RawMessage(`"old"`)},
		{Path: "/spec/replicas", Before: json.RawMessage(`1`), After: json.RawMessage(`2`)},
	}, modified[0].Changes)
	var object struct {
		Spec json.RawMessage `json:"spec"`
	}
	require.NoError(t, json.Unmarshal(modified[0].Object, &object))
	require.JSONEq(t, `{"replicas":2,"nullable":"set","new":null,"big":9007199254740994}`, string(object.Spec))
	require.Empty(t, resourceRecords(t, &w, resourceFrame("MODIFIED", `{"replicas":2,"nullable":"set","new":null,"big":9007199254740994}`)))
	deleted := resourceRecords(t, &w, resourceFrame("DELETED", `{"replicas":2}`))
	require.Equal(t, "DELETED", deleted[0].Event)
	require.Empty(t, w.states)
	require.Zero(t, w.bytes)
	require.Equal(t, "ADDED", resourceRecords(t, &w, resourceFrame("ADDED", `{"replicas":0}`))[0].Event)
}

func TestResourceWatchReestablishesItsBaseline(t *testing.T) {
	t.Parallel()
	var w resourceWatch
	frame := resourceFrame("ADDED", `{"replicas":1}`)
	resourceRecords(t, &w, frame)
	require.Empty(t, resourceRecords(t, &w, frame), "a reconnect must not replay unchanged objects")
	changed := resourceRecords(t, &w, resourceFrame("ADDED", `{"replicas":2}`))
	require.Equal(t, "MODIFIED", changed[0].Event)
	require.Len(t, changed[0].Changes, 1)
	var fresh resourceWatch
	unknown := resourceRecords(t, &fresh, resourceFrame("MODIFIED", `{"replicas":3}`))
	require.Contains(t, unknown[0].Message, "previous state unavailable")
	require.Empty(t, unknown[0].Changes)
}

func TestResourceWatchIgnoresBookkeeping(t *testing.T) {
	t.Parallel()
	var w resourceWatch
	frame := resourceFrame("ADDED", `{"replicas":1}`)
	resourceRecords(t, &w, frame)
	frame = strings.Replace(frame, `"name":"demo"`, `"name":"demo","resourceVersion":"2","managedFields":[{"manager":"kubectl"}]`, 1)
	require.Empty(t, resourceRecords(t, &w, frame))
	frame = strings.Replace(frame, `"replicas":1`, `"replicas":2`, 1)
	require.Len(t, resourceRecords(t, &w, frame)[0].Changes, 1)
}

func TestResourceChangesEscapePathsAndKeepArrays(t *testing.T) {
	t.Parallel()
	var w resourceWatch
	resourceRecords(t, &w, resourceFrame("ADDED", `{"a/b~c":[1,2]}`))
	changed := resourceRecords(t, &w, resourceFrame("MODIFIED", `{"a/b~c":[1,3]}`))
	require.Equal(t, []ResourceChange{{Path: "/spec/a~1b~0c", Before: json.RawMessage(`[1,2]`), After: json.RawMessage(`[1,3]`)}}, changed[0].Changes)
}

func TestResourceWatchReadsEvents(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, fields, level string
		count               int
	}{
		{"core event", `"message":"waiting for nodes","type":"Warning","lastTimestamp":"2026-10-29T11:00:00Z","count":3,"involvedObject":{"kind":"Cluster","name":"demo"}`, "warn", 3},
		{"events API", `"note":"ready","type":"Normal","eventTime":"2026-10-29T10:00:00Z","series":{"count":4,"lastObservedTime":"2026-10-29T11:00:00Z"},"regarding":{"kind":"Cluster","name":"demo"}`, "info", 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var w resourceWatch
			frame := `{"type":"MODIFIED","object":{"kind":"Event","apiVersion":"v1","metadata":{"name":"demo.123","namespace":"octo"},"reason":"Ready",` + tt.fields + `}}`
			got := resourceRecords(t, &w, frame)
			require.Len(t, got, 1)
			rec := got[0]
			require.Equal(t, tt.level, rec.Level)
			require.Equal(t, tt.count, rec.Count)
			require.Equal(t, "Cluster", rec.ResourceKind)
			require.Equal(t, "demo", rec.ResourceName)
			require.Equal(t, "octo", rec.Namespace)
			require.Equal(t, "2026-10-29T11:00:00Z", rec.Time.Format(time.RFC3339))
			require.Contains(t, rec.Message, "Ready Cluster/demo")
		})
	}
}

func TestResourceWatchReadsSnapshotsAndReportsErrors(t *testing.T) {
	t.Parallel()
	var w resourceWatch
	_, obj := watchEvent([]byte(resourceFrame("ADDED", `{}`)))
	got := resourceRecords(t, &w, `{"kind":"List","items":[`+string(obj)+`]}`)
	require.Len(t, got, 1)
	require.Empty(t, resourceRecords(t, &w, `{"type":"BOOKMARK","object":{}}`))
	for _, frame := range []string{
		`{"type":"ERROR","object":{"message":"too old resource version"}}`,
		`{"type":"UNRECOGNIZED","object":{}}`, `{"kind":"Pod"}`, `null`, `{"`,
	} {
		_, err := w.observe([]byte(frame), time.Time{})
		require.Error(t, err, frame)
	}
}

func TestResourceWatchAcceptsCustomTopLevelFields(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"Cluster", "Event", "TodoList"} {
		t.Run(kind, func(t *testing.T) {
			var w resourceWatch
			frame := fmt.Sprintf(`{"kind":%q,"apiVersion":"custom.example/v1","metadata":{"name":"demo"},"object":{"data":1},"type":42,"message":{},"count":"many","items":false}`, kind)
			records := resourceRecords(t, &w, frame)
			require.Len(t, records, 1)
			require.Equal(t, "ADDED "+kind+" demo", records[0].Message)
			require.JSONEq(t, frame, string(records[0].Object))
		})
	}
}

func TestResourceFramesKeepDiagnosticsAndDetectTruncation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, input         string
		frames, diagnostics int
		truncated           bool
	}{
		{"compact", resourceFrame("ADDED", `{}`) + "\n", 1, 0, false},
		{"pretty and PTY", "warning\r\n{\r\n  \"kind\": \"Pod\"\r\n}\r\n", 1, 1, false},
		{"cut off", "{\n  \"kind\": \"Pod\"", 0, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var frames, diagnostics int
			err := readResourceFrames(strings.NewReader(tt.input), func(frame []byte, diagnostic bool) bool {
				if diagnostic {
					diagnostics++
				} else {
					frames++
					require.True(t, json.Valid(frame))
				}
				return true
			})
			if tt.truncated {
				require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.frames, frames)
			require.Equal(t, tt.diagnostics, diagnostics)
		})
	}
}

func TestResourceStateIsBounded(t *testing.T) {
	t.Parallel()
	var w resourceWatch
	for i := range maxResourceStates + 10 {
		w.remember(fmt.Sprint(i), []byte(`{}`), false)
	}
	require.Len(t, w.states, maxResourceStates)
	require.Equal(t, maxResourceStates*2, w.bytes)
	large := make([]byte, maxResourceBytes/2)
	w.remember("large1", large, false)
	w.remember("large2", large, false)
	w.remember("large3", large, false)
	require.LessOrEqual(t, w.bytes, maxResourceBytes)
	var size int
	for _, state := range w.states {
		size += len(state)
	}
	require.Equal(t, size, w.bytes)
}

func FuzzResourceWatch(f *testing.F) {
	for _, seed := range []string{resourceFrame("ADDED", `{"replicas":1}`), resourceFrame("MODIFIED", `{"replicas":2}`), resourceFrame("DELETED", `{}`), `{"kind":"List","items":[]}`, `{"type":"BOOKMARK","object":{}}`, `{"type":"ERROR","object":{"message":"expired"}}`, `null`} {
		f.Add(seed)
	}
	f.Add(`{"kind":"TodoList","apiVersion":"custom.example/v1","metadata":{"name":"demo"},"object":{"data":1},"type":42,"message":{},"count":"many","items":false}`)
	f.Fuzz(func(t *testing.T, frame string) {
		var w resourceWatch
		_, _ = w.observe([]byte(resourceFrame("ADDED", `{"replicas":1}`)), time.Time{})
		lines, _ := w.observe([]byte(frame), time.Time{})
		for _, l := range lines {
			require.True(t, json.Valid(l.Data))
		}
	})
}

func FuzzResourceFrames(f *testing.F) {
	for _, seed := range []string{resourceFrame("ADDED", `{}`), "warning\n{\n}\n", "{\n", "", "{}\r\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_ = readResourceFrames(strings.NewReader(input), func([]byte, bool) bool { return true })
	})
}

func TestResourceStreamReconnectsWithoutReplayingObjects(t *testing.T) {
	fakeCollector(t, func(attempt int) string {
		switch attempt {
		case 0:
			return "printf '%s\\n' " + Quote(resourceFrame("ADDED", `{"replicas":1}`))
		case 1:
			return "printf '%s\\n' " + Quote(resourceFrame("ADDED", `{"replicas":1}`)) + " " + Quote(resourceFrame("MODIFIED", `{"replicas":2}`))
		default:
			return "exit 0"
		}
	})
	s, err := Start(t.Context(), Config{Collector: CollectorKubeWatch, Follow: true})
	require.NoError(t, err)
	defer s.Close()
	var got []resourceRecord
	for line := range s.Lines() {
		require.False(t, line.Kind.IsNote(), line.Reason)
		var rec resourceRecord
		require.NoError(t, json.Unmarshal(line.Data, &rec))
		got = append(got, rec)
	}
	require.NoError(t, <-s.Done())
	require.Len(t, got, 2)
	require.Equal(t, "MODIFIED", got[1].Event)
	require.Len(t, got[1].Changes, 1)
}
