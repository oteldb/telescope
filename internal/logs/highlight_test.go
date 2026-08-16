package logs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHighlightLeavesAnUnremarkableLineAlone(t *testing.T) {
	for _, line := range []string{
		"",
		"connect refused, retrying",
		"the quick brown fox",
	} {
		require.Equal(t, line, Highlight(line))
	}
}

// TestHighlightKeepsUpstreamColor: a collector that colored its own output has
// already decided how it reads, and a second opinion over the top of it is not
// an improvement.
func TestHighlightKeepsUpstreamColor(t *testing.T) {
	line := "\x1b[31mERROR\x1b[0m listen tcp :8080: bind: address already in use"
	require.Equal(t, line, Highlight(line))
}

func TestHighlightColorsText(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{
			name: "a number is a number",
			line: "connect failed after 3 retries",
			want: []string{ansiNum + "3" + ansiReset},
		},
		{
			name: "a klog prefix reads as its severity, its clock and where it came from",
			line: `E0817 12:00:00.123456       1 kubelet.go:2451] Failed to start container`,
			want: []string{
				levelColors["error"] + "E" + ansiReset,
				ansiTime + "0817 12:00:00.123456" + ansiReset,
				ansiPath + "kubelet.go:2451" + ansiReset,
			},
		},
		{
			name: "an access log tells the verb, the route and how it went apart",
			line: `10.0.0.1 - - [17/Aug/2026:12:00:00 +0000] "GET /v1/users HTTP/1.1" 503 128`,
			want: []string{
				ansiMethod + "GET" + ansiReset,
				ansiPath + "/v1/users" + ansiReset,
				ansiErr + "503" + ansiReset,
			},
		},
		{
			name: "a request written without quotes reads the same",
			line: "handled GET /healthz in 2ms",
			want: []string{
				ansiMethod + "GET" + ansiReset,
				ansiPath + "/healthz" + ansiReset,
			},
		},
		{
			name: "a status is a status once something says so",
			line: "upstream replied status=404 for /favicon.ico",
			want: []string{ansiWarn + "404" + ansiReset},
		},
		{
			name: "a grpc status by name",
			line: "rpc error: code = DEADLINE_EXCEEDED desc = context deadline exceeded",
			want: []string{ansiErr + "DEADLINE_EXCEEDED" + ansiReset},
		},
		{
			name: "an event reason worth worrying about",
			line: "Back-off restarting failed container, reason: CrashLoopBackOff",
			want: []string{ansiErr + "CrashLoopBackOff" + ansiReset},
		},
		{
			name: "and one that only means it is being looked at",
			line: "Killing container with a grace period",
			want: []string{ansiWarn + "Killing" + ansiReset},
		},
		{
			name: "the kubernetes coordinates klog writes beside a line",
			line: `Unhealthy pod=default/nginx-7d8f container=app node=ip-10-0-1-7`,
			want: []string{
				ansiWarn + "Unhealthy" + ansiReset,
				ansiPod + "default/nginx-7d8f" + ansiReset,
				ansiContainer + "app" + ansiReset,
				ansiNode + "ip-10-0-1-7" + ansiReset,
			},
		},
		{
			name: "a namespace however it was spelled",
			line: `evicting namespace="kube-system"`,
			want: []string{ansiNamespace + "kube-system" + ansiReset},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Highlight(tt.line)
			for _, want := range tt.want {
				require.Contains(t, got, want)
			}
		})
	}
}

func FuzzHighlight(f *testing.F) {
	f.Add("connect failed after 3 retries")
	f.Add(`E0817 12:00:00.123456       1 kubelet.go:2451] Failed to start container`)
	f.Add(`10.0.0.1 - - "GET /v1/users HTTP/1.1" 503 128`)
	f.Add("rpc error: code = DEADLINE_EXCEEDED")
	f.Add(`Unhealthy pod=default/nginx-7d8f container=app`)
	f.Fuzz(func(t *testing.T, line string) {
		out := Highlight(line)
		if strings.ContainsRune(line, 0x1b) {
			require.Equal(t, line, out, "a line that colored itself is left as it is")
			return
		}
		// Coloring adds escapes and takes nothing away: whatever the line said,
		// it still says once the color is stripped back off.
		require.Equal(t, line, stripSGR(out))
	})
}

// stripSGR removes the color sequences from a rendering, leaving what was
// colored.
func stripSGR(s string) string {
	var out []byte
	for i := 0; i < len(s); {
		if n := sgrLen(s[i:]); n > 0 {
			i += n
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}
