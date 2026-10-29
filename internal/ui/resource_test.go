package ui

import (
	"slices"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

func TestResourceWatchCanBeOpenedFromThePicker(t *testing.T) {
	m := newStart()
	m.step = stepCollector
	m.collector = slices.Index(collectors, source.CollectorKubeWatch)
	m.target.SetValue("ns:* clusters.cluster.x-k8s.io")
	m.kubeconfig.SetValue("prod")
	m.kubecontext.SetValue("reader")
	require.True(t, m.kubectlSelected())
	require.False(t, rangeSupported(source.CollectorKubeWatch))
	cfg := m.config()
	require.NoError(t, cfg.Validate())
	require.Equal(t, "*", cfg.Namespace)
	require.Equal(t, "clusters.cluster.x-k8s.io", cfg.Target)
	require.Equal(t, "prod", cfg.KubeConfig)
	require.Equal(t, "reader", cfg.KubeContext)
	require.Equal(t, "ns:* events", complete.Target("ns:* events", source.CollectorKubeWatch))
	m.target.SetValue("ns:octo clus")
	require.Equal(t, "clus", m.narrowing())
	m.filtered = []complete.Candidate{{Value: "clusters.cluster.x-k8s.io"}}
	m.sel = 0
	m.accept()
	require.Equal(t, "ns:octo clusters.cluster.x-k8s.io", m.target.Value())
}

func TestResourceWatchChangesAppearOnScreen(t *testing.T) {
	lg := newLogs(source.Config{Collector: source.CollectorKubeWatch}, logs.NewStore(100), "namespace=octo kind=Cluster")
	lg.resize(160, 30)
	lg.append(source.Line{Data: []byte(`{"watch_event":"MODIFIED","msg":"MODIFIED Cluster octo/demo","namespace":"octo","kind":"Cluster","changes":[{"path":"/spec/replicas","before":1,"after":2}]}`)})
	view := lg.View()
	require.Contains(t, ansi.Strip(view), "MODIFIED Cluster octo/demo · /spec/replicas: 1 → 2")
	row := rowWith(t, view, "MODIFIED Cluster")
	require.Contains(t, row, "\x1b[", "the update is colored in the actual log view")
}
