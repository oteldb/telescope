package source

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKubectlSaysWhichPodWroteIt: a deployment is several pods read as one
// command, and without the prefix nothing downstream can tell them apart.
func TestKubectlSaysWhichPodWroteIt(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target string
		want   bool
	}{
		{"a pod is one stream", "api-0", false},
		{"named as a resource, still one", "pod/api-0", false},
		{"plural, still one", "pods/api-0", false},
		{"a deployment is every pod of it", "deployment/api", true},
		{"short kinds too", "deploy/api", true},
		{"and other owners", "statefulset/db", true},
		{"a selector is whatever matches", "app=api", true},
		{"nothing named", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Collector: CollectorKubectl, Target: tt.target}
			require.Equal(t, tt.want, cfg.Prefixes())
		})
	}

	require.False(t, Config{Collector: CollectorDocker, Container: "deploy/api"}.Prefixes(),
		"only kubectl writes one")
}

func TestUnprefix(t *testing.T) {
	for _, tt := range []struct {
		name   string
		line   string
		want   string
		labels []Label
	}{
		{
			name: "pod and container",
			line: "[pod/api-6b8d4f-2xk9w/api] hello",
			want: "hello",
			labels: []Label{
				{Key: kubePodLabel, Value: "api-6b8d4f-2xk9w"},
				{Key: kubeContainerLabel, Value: "api"},
			},
		},
		{
			name: "in front of the timestamp the server wrote",
			line: "[pod/api-0/api] 2026-08-10T01:00:00Z hello",
			want: "2026-08-10T01:00:00Z hello",
			labels: []Label{
				{Key: kubePodLabel, Value: "api-0"},
				{Key: kubeContainerLabel, Value: "api"},
			},
		},
		{
			name: "a line of its own that starts with a bracket",
			line: "[warn] upstream timed out",
			want: "[warn] upstream timed out",
		},
		{
			name: "a bracket that never closes",
			line: "[pod/api-0/api hello",
			want: "[pod/api-0/api hello",
		},
		{
			name: "something else in brackets",
			line: "[deployment/api] hello",
			want: "[deployment/api] hello",
		},
		{
			name: "no container named",
			line: "[pod/api-0] hello",
			want: "[pod/api-0] hello",
		},
		{
			name: "nothing after it",
			line: "[pod/api-0/api] ",
			want: "",
			labels: []Label{
				{Key: kubePodLabel, Value: "api-0"},
				{Key: kubeContainerLabel, Value: "api"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, labels := unprefix([]byte(tt.line))
			require.Equal(t, tt.want, string(data))
			require.Equal(t, tt.labels, labels)
		})
	}
}

// FuzzUnprefix: the prefix is somebody else's bytes on the front of somebody
// else's line, and taking it off must never lose any of the line or invent a
// label out of one that was not there.
func FuzzUnprefix(f *testing.F) {
	for _, seed := range []string{
		"[pod/api-0/api] hello",
		"[warn] upstream timed out",
		"[pod/api-0] hello",
		"[pod//api] hello",
		"[",
		"",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		rest, labels := unprefix(data)
		require.True(t, bytes.HasSuffix(data, rest), "what is left is the end of the line")
		if labels == nil {
			require.Equal(t, data, rest, "a line with no prefix is untouched")
			return
		}
		require.Len(t, labels, 2)
		for _, l := range labels {
			require.NotEmpty(t, l.Value)
		}
		require.True(t, bytes.HasPrefix(data, []byte("[pod/")))
	})
}
