package logs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreColorsWellKnownFields(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "a status code by its class",
			line: `{"level":"info","msg":"served","http.response.status_code":500}`,
			want: ansiErr + "500" + ansiReset,
		},
		{
			name: "a method",
			line: `{"level":"info","msg":"served","method":"POST"}`,
			want: ansiMethod + "POST" + ansiReset,
		},
		{
			name: "a grpc code, with the name nobody remembers",
			line: `{"level":"info","msg":"called","rpc.grpc.status_code":7}`,
			want: ansiErr + "7 PERMISSION_DENIED" + ansiReset,
		},
		{
			name: "a topic",
			line: `{"level":"info","msg":"produced","messaging.destination.name":"orders"}`,
			want: ansiName + "orders" + ansiReset,
		},
		{
			name: "a pod",
			line: `{"level":"info","msg":"restarted","k8s.pod.name":"nginx-7d8f"}`,
			want: ansiPod + "nginx-7d8f" + ansiReset,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewStore(1).Append(line(tt.line))
			require.NotNil(t, e)
			require.Contains(t, e.Text, tt.want)
		})
	}
}

// TestStoreLeavesUnknownFieldsAsPlRenderedThem: the coloring is a rewrite of
// somebody else's rendering, so the rendering a line gets when none of its
// fields mean anything has to be the one it got before there was any of this.
func TestStoreLeavesUnknownFieldsAsPlRenderedThem(t *testing.T) {
	s := NewStore(1)
	const raw = `{"level":"info","msg":"hello","user":"bob","count":3}`

	want, ok := s.fmt.Format([]byte(raw))
	require.True(t, ok)

	e := s.Append(line(raw))
	require.NotNil(t, e)
	require.Equal(t, want, e.Text)
}

// TestHighlightRecordSurvivesARenderingItCannotRead: the field is found in pl's
// output by the shape pl writes it in, and a rendering that does not have that
// shape is one to leave alone rather than one to guess at.
func TestHighlightRecordSurvivesARenderingItCannotRead(t *testing.T) {
	rec := Parse([]byte(`{"msg":"served","status":500,"pod":"a b","user":"bob"}`))
	require.True(t, rec.Structured)

	const text = "served status=500 pod=a b user=bob"
	require.Equal(t, text, highlightRecord(text, rec.Fields), "no colored key, nothing found")

	colored := plFieldKey + "status" + ansiReset + "=500"
	require.True(t, strings.HasSuffix(highlightRecord("served "+colored, rec.Fields),
		plFieldKey+"status"+ansiReset+"="+ansiErr+"500"+ansiReset))
}
