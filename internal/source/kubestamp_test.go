package source

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStandaloneKubernetesLogsHavePagingTimestamps(t *testing.T) {
	for _, tt := range []struct{ name, target, payload string }{
		{"plain pod log", "api-0", "started listening"},
		{"structured pod log", "api-0", `{"level":"info","msg":"started listening"}`},
		{"deployment", "deploy/api", "started listening"},
		{"selector", "app=api", "started listening"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Collector: CollectorKubectl, Target: tt.target}
			stamp := "2026-10-30T00:10:00.123456789Z"
			line := stamp + " " + tt.payload
			if cfg.Prefixes() {
				line = "[pod/api-0/api] " + line
			}
			fakeCollector(t, func(int) string { return "printf '%s\\n' " + Quote(line) })
			s, err := Start(t.Context(), cfg)
			require.NoError(t, err)
			defer s.Close()
			var lines []Line
			for l := range s.Lines() {
				lines = append(lines, l)
			}
			require.NoError(t, <-s.Done())
			require.Len(t, lines, 1)
			require.Equal(t, tt.payload, string(lines[0].Data))
			at, err := time.Parse(time.RFC3339Nano, stamp)
			require.NoError(t, err)
			require.Equal(t, at, lines[0].At)
			require.Contains(t, cfg.Command(), "--timestamps")
			if cfg.Prefixes() {
				require.Len(t, lines[0].Labels, 2)
			}
		})
	}
}
