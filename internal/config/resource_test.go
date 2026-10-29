package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func TestResourceWatchPlace(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, target, namespace string }{
		{"events", "", "*"},
		{"custom resource", "clusters.cluster.x-k8s.io", "octo"},
		{"compact target", "ns:octo deployments/api", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := Place{Name: "watch", Type: "kubectl-watch", Target: tt.target, Namespace: tt.namespace, KubeConfig: "prod", Context: "admin", Via: "ssh://bastion", Sudo: true}
			require.NoError(t, p.Validate())
			cfg, ready, err := p.Stream()
			require.NoError(t, err)
			require.True(t, ready)
			require.Equal(t, source.CollectorKubeWatch, cfg.Collector)
			require.Equal(t, "prod", cfg.KubeConfig)
			require.Equal(t, "admin", cfg.KubeContext)
			require.Equal(t, "bastion", cfg.Host)
			require.True(t, cfg.Follow)
			require.Equal(t, cfg, cfg.WithTarget(Target(cfg)))
		})
	}
	require.ErrorContains(t, (Place{Name: "bad", Type: "kubectl-watch", Range: "1h"}).Validate(), "no historical time range")
	require.Error(t, (Place{Name: "bad", Type: "kubectl-watch", Target: "--raw=/"}).Validate())
}
