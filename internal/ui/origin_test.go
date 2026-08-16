package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// podLine is a line as a prefixing kubectl reports it: the message, and which
// pod of the deployment wrote it.
func podLine(pod, container, text string) source.Line {
	return source.Line{
		Data: []byte(text),
		Labels: []source.Label{
			{Key: "k8s.pod.name", Value: pod},
			{Key: "k8s.container.name", Value: container},
		},
	}
}

// deployModel opens a view on a deployment, which is one command and several
// pods.
func deployModel(t *testing.T, lines ...source.Line) tea.Model {
	t.Helper()
	cfg := source.Config{
		Collector: source.CollectorKubectl,
		Namespace: "apps", Target: "deploy/api", Follow: true,
	}
	m := send(t, New(), size(), connectMsg{cfg: cfg})
	return send(t, m, linesMsg{lines: lines, closed: true})
}

// TestOneStreamGetsNoColumn: a view of a single pod looks as it always did.
// Width is the log's, and a column repeating what the title says is width
// spent on nothing.
func TestOneStreamGetsNoColumn(t *testing.T) {
	m := deployModel(t,
		podLine("api-6b8d4f-2xk9w", "api", `{"ts":"2026-08-10T10:00:00Z","msg":"started"}`),
		podLine("api-6b8d4f-2xk9w", "api", `{"ts":"2026-08-10T10:00:01Z","msg":"serving"}`),
	)
	out := screen(t, m)
	require.Contains(t, out, "started")
	require.NotContains(t, out, "2xk9w", "there is nothing to tell apart")
	require.Equal(t, "  "+stampOf(m, 0), drawnRow(t, out, "started")[:2+len(stampOf(m, 0))],
		"and the line begins where it always did")
}

// TestSeveralStreamsAreNamedDownTheLeft: the pods of one deployment are what
// the reader cannot otherwise tell apart, and the name is only the part that
// differs.
func TestSeveralStreamsAreNamedDownTheLeft(t *testing.T) {
	m := deployModel(t,
		podLine("api-6b8d4f-2xk9w", "api", `{"ts":"2026-08-10T10:00:00Z","msg":"started"}`),
		podLine("api-6b8d4f-lq7pv", "api", `{"ts":"2026-08-10T10:00:01Z","msg":"serving"}`),
	)
	out := screen(t, m)
	require.Contains(t, drawnRow(t, out, "started"), "2xk9w")
	require.Contains(t, drawnRow(t, out, "serving"), "lq7pv")
	require.NotContains(t, out, "api-6b8d4f-", "what every pod shares says nothing")
}

// TestAStreamKeepsItsColor: the color is hashed from what the stream is, so a
// filter that drops one pod does not repaint the others.
func TestAStreamKeepsItsColor(t *testing.T) {
	m := deployModel(t,
		podLine("api-6b8d4f-2xk9w", "api", `{"ts":"2026-08-10T10:00:00Z","msg":"alpha"}`),
		podLine("api-6b8d4f-lq7pv", "api", `{"ts":"2026-08-10T10:00:01Z","msg":"beta"}`),
		podLine("api-6b8d4f-w3n8t", "api", `{"ts":"2026-08-10T10:00:02Z","msg":"gamma"}`),
	)
	// Not the row the cursor is on: that one is drawn over a gradient, which is
	// what the cursor is.
	painted := originStyle("api-6b8d4f-lq7pv").Render("lq7pv")
	require.Contains(t, m.View(), painted)

	m = send(t, m, k("/"))
	m = typed(t, m, "beta or gamma")
	// Applying a filter puts the cursor at the top, and G is back to the end of
	// the list — where it was, and off the row being read here.
	m = send(t, m, k("enter"), k("G"))
	require.NotContains(t, screen(t, m), "alpha", "the filter took one away")
	require.Contains(t, m.View(), painted, "and the rest are the color they were")
}

// TestTwoStreamsAreTwoColors: telling them apart is the whole point of the
// column.
func TestTwoStreamsAreTwoColors(t *testing.T) {
	require.NotEqual(t,
		originStyle("api-6b8d4f-2xk9w").Render("x"),
		originStyle("api-6b8d4f-lq7pv").Render("x"),
	)
}

// drawnRow is the drawn row carrying text, without the screen's own padding.
func drawnRow(t *testing.T, screen, text string) string {
	t.Helper()
	for row := range strings.SplitSeq(screen, "\n") {
		if strings.Contains(row, text) {
			return strings.TrimPrefix(strings.TrimPrefix(ansi.Strip(row), "  "), "│")
		}
	}
	t.Fatalf("no row says %q", text)
	return ""
}
