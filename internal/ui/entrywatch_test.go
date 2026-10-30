package ui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
	"github.com/oteldb/telescope/internal/source"
)

const multilineWatchEvent = `{"watch_event":"ADDED","kind":"Event","api_version":"v1","level":"info","reason":"Ready","object":{"message":"first line\nsecond line"}}`

func eventEntry(t testing.TB) entryModel {
	t.Helper()
	data, err := os.ReadFile("../logs/testdata/kubernetes-event.json")
	require.NoError(t, err)
	e := logs.NewStore(10).Append(source.Line{Data: data})
	m := newEntry(source.Config{Name: "operations events", Collector: source.CollectorKubeWatch, Namespace: "*", KubeConfig: "/private/operations.kubeconfig"}, e)
	m.resize(120, 40)
	return m
}

func documentText(m entryModel) string {
	var lines []string
	for _, it := range m.document(m.docWidth()) {
		lines = append(lines, it.lines...)
	}
	return ansi.Strip(strings.Join(lines, "\n"))
}

func TestEventEntryStartsWithOneReadableSummary(t *testing.T) {
	m := eventEntry(t)
	out := ansi.Strip(m.View())
	for _, value := range []string{"INFO", "ArtifactUpToDate", "OCIRepository/metrics-server", "kube-system", "first seen", "last seen", "1489", "increased by 1", "source-controller", "operations events", "▸ Changes", "▸ Connection", "▸ Object", "▸ Raw entry"} {
		require.Contains(t, out, value)
	}
	require.Equal(t, 1, strings.Count(out, "artifact up-to-date"))
	for _, value := range []string{"watch_event", "lastTimestamp", "managedFields", "fieldsV1", "metrics-server.1234", "/private/operations.kubeconfig", "rendered"} {
		require.NotContains(t, out, value)
	}
	require.Contains(t, out, "space expand/collapse")
}

func TestEventEntryExpandsDetailsOnRequest(t *testing.T) {
	for _, tt := range []struct{ name, key, value string }{
		{"changes", "changes", "1488"},
		{"connection", "connection", "/private/operations.kubeconfig"},
		{"object", "object", "firstTimestamp"},
		{"raw", "raw", "watch_event"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := selectKey(t, eventEntry(t), tt.key)
			require.NotContains(t, documentText(m), tt.value)
			m = press(t, m, " ")
			require.Contains(t, documentText(m), tt.value)
			require.Equal(t, tt.key, selected(m))
			m = press(t, m, "left")
			require.NotContains(t, documentText(m), tt.value)
			m = press(t, m, "right")
			require.Contains(t, documentText(m), tt.value)
		})
	}
}

func TestEventMessagesKeepLineBreaksAndOriginalCopyValues(t *testing.T) {
	e := logs.NewStore(1).Append(source.Line{Data: []byte(multilineWatchEvent)})
	m := newEntry(source.Config{Collector: source.CollectorKubeWatch}, e)
	m.resize(100, 30)
	for _, it := range m.document(m.docWidth()) {
		if it.key != "body" {
			continue
		}
		require.Len(t, it.lines, 2)
		require.Contains(t, ansi.Strip(it.lines[0]), "first line")
		require.Contains(t, ansi.Strip(it.lines[1]), "second line")
		require.Equal(t, "first line\nsecond line", it.copyValue())
		return
	}
	t.Fatal("message row is missing")
}

func TestManagedFieldsStayCollapsedInsideTheObject(t *testing.T) {
	m := press(t, selectKey(t, eventEntry(t), "object"), " ")
	require.NotContains(t, documentText(m), "fieldsV1")
	require.Contains(t, documentText(m), "▸ Managed fields")
	m = press(t, selectKey(t, m, "managedFields"), " ")
	require.Contains(t, documentText(m), "fieldsV1")
}

func TestWatchEntryCopiesFullValuesAndFiltersByIdentity(t *testing.T) {
	m := eventEntry(t)
	var copied string
	stubCopy(t, &copied)
	for _, tt := range []struct{ key, want string }{
		{"body", m.entry.Record.Watch.Event.Message},
		{"resource", "OCIRepository/metrics-server"},
		{"count", "1489"},
		{"object", string(m.entry.Record.Watch.Object)},
		{"raw", string(m.entry.Raw)},
	} {
		t.Run(tt.key, func(t *testing.T) {
			press(t, selectKey(t, m, tt.key), "y")
			require.Equal(t, tt.want, copied)
		})
	}
	for _, key := range []string{"namespace", "reason", "resource", "count"} {
		t.Run("filter "+key, func(t *testing.T) {
			_, cmd := selectKey(t, m, key).Update(k("f"))
			require.NotNil(t, cmd)
			term := cmd().(filterMsg).term
			require.True(t, query.Match(term, m.entry))
			other := logs.NewStore(1).Append(source.Line{Data: []byte(`{"namespace":"other","resource_kind":"Deployment","resource_name":"different","count":1,"reason":"Failed"}`)})
			require.False(t, query.Match(term, other))
		})
	}
	press(t, m, "Y")
	require.Equal(t, string(m.entry.Raw), copied)
}

func TestWatchDetailsWrapAndRemainNavigable(t *testing.T) {
	m := eventEntry(t)
	m.resize(40, 15)
	for _, it := range m.document(m.docWidth()) {
		for _, line := range it.lines {
			require.LessOrEqual(t, ansi.StringWidth(line), m.docWidth(), ansi.Strip(line))
		}
	}
	m = press(t, selectKey(t, m, "raw"), " ")
	previous := m.sel
	m = press(t, m, "pgdown")
	require.Greater(t, m.sel, previous)
	m = press(t, m, "G")
	require.Equal(t, "raw", selected(m))
	require.Greater(t, m.off, 0)
	var copied string
	stubCopy(t, &copied)
	press(t, m, "y")
	require.Equal(t, string(m.entry.Raw), copied)
}

func TestResourceEntryOpensOnStructuredChanges(t *testing.T) {
	line := `{"watch_event":"MODIFIED","msg":"MODIFIED Cluster octo/demo","kind":"Cluster","namespace":"octo","name":"demo","changes":[{"path":"/spec/replicas","before":1,"after":2},{"path":"/spec/labels","after":{"team":"platform"}},{"path":"/spec/removed","before":null}],"object":{"kind":"Cluster","metadata":{"name":"demo"}}}`
	e := logs.NewStore(1).Append(source.Line{Data: []byte(line)})
	m := newEntry(source.Config{Collector: source.CollectorKubeWatch}, e)
	m.resize(100, 40)
	out := documentText(m)
	for _, value := range []string{"MODIFIED", "Cluster/demo", "octo", "▾ Changes", "/spec/replicas", "before", "after", "platform", "null"} {
		require.Contains(t, out, value)
	}
	require.NotContains(t, out, `[{"path"`)
	var copied string
	stubCopy(t, &copied)
	press(t, selectKey(t, m, "before"), "y")
	require.Equal(t, "1", copied)
	m = press(t, selectKey(t, m, "changes"), " ")
	require.NotContains(t, documentText(m), "/spec/replicas")
}

func TestEventCountRemainsVisibleBesideALongMessage(t *testing.T) {
	e := eventEntry(t).entry
	for _, width := range []int{50, 90, 150} {
		line := renderLine(e, []rowField{{RowField: logs.RowField{Key: "watch_event", Value: "MODIFIED"}}}, "", "", 1, false, 0, width)
		out := ansi.Strip(line)
		require.LessOrEqual(t, ansi.StringWidth(line), width)
		require.Contains(t, out, "1489 occurrences")
		require.NotContains(t, out, "watch_event")
	}
}

func TestChangeValuesKeepTheirTypesAndCopyTheirOriginalValue(t *testing.T) {
	for _, tt := range []struct{ name, raw, value string }{
		{"empty string", `""`, ""},
		{"null", `null`, "null"},
		{"string null", `"null"`, "null"},
		{"multiline string", `"one\ntwo"`, "one\ntwo"},
		{"number", `9007199254740993`, "9007199254740993"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := watchDoc{width: 50, indent: 10}
			d.changeValue("after", json.RawMessage(tt.raw), styleOK)
			require.Len(t, d.items, 1)
			require.Contains(t, ansi.Strip(strings.Join(d.items[0].lines, "\n")), tt.raw)
			require.Equal(t, tt.value, d.items[0].copyValue())
			require.Nil(t, d.items[0].term())
		})
	}
}

func TestWatchUpdatesAreNotFoldedAsRepeatedMessages(t *testing.T) {
	first := eventEntry(t).entry
	secondRaw := strings.Replace(string(first.Raw), `"count": 1489`, `"count": 1490`, 1)
	second := logs.NewStore(1).Append(source.Line{Data: []byte(secondRaw)})
	require.Equal(t, first.Record.Body, second.Record.Body)
	require.Len(t, clampRuns([]*logs.Entry{first, second}, true, logs.Origins{}), 2)
}

func FuzzWatchEntry(f *testing.F) {
	f.Add(string(eventEntry(f).entry.Raw))
	f.Add(multilineWatchEvent)
	f.Add(`{"watch_event":"MODIFIED","kind":"Cluster","changes":[{"path":"/spec/a","before":1,"after":2}],"object":{"metadata":{"managedFields":[]}}}`)
	f.Fuzz(func(t *testing.T, line string) {
		if !json.Valid([]byte(line)) {
			return
		}
		e := logs.NewStore(1).Append(source.Line{Data: []byte(line)})
		if e == nil || e.Record.Watch == nil {
			return
		}
		m := newEntry(source.Config{Collector: source.CollectorKubeWatch}, e)
		m.resize(60, 20)
		m.expanded = map[string]bool{"object": true, "raw": true, "changes": true, "managedFields": true}
		_ = m.View()
	})
}
