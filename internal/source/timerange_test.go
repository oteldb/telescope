package source

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// now is the clock every range in this file is resolved against: a Wednesday
// afternoon, far enough into the day that midnight is unambiguous.
var now = time.Date(2026, 8, 11, 14, 30, 0, 0, time.UTC)

func TestParseRange(t *testing.T) {
	day := func(d, h, m int) time.Time {
		return time.Date(2026, 8, d, h, m, 0, 0, time.UTC)
	}
	for _, tt := range []struct {
		spec  string
		since time.Time
		until time.Time
	}{
		{spec: ""},
		{spec: "all"},
		{spec: "1h", since: day(11, 13, 30)},
		{spec: "-1h", since: day(11, 13, 30)},
		{spec: "90m", since: day(11, 13, 0)},
		{spec: "2d", since: day(9, 14, 30)},
		{spec: "1w", since: day(4, 14, 30)},
		{spec: "6h..1h", since: day(11, 8, 30), until: day(11, 13, 30)},
		{spec: "1h..now", since: day(11, 13, 30)},
		{spec: "1h..", since: day(11, 13, 30)},
		{spec: "today", since: day(11, 0, 0)},
		{spec: "yesterday", since: day(10, 0, 0), until: day(11, 0, 0)},
		{spec: "10:00", since: day(11, 10, 0)},
		{spec: "10:00..12:00", since: day(11, 10, 0), until: day(11, 12, 0)},
		{spec: "2026-08-09", since: day(9, 0, 0)},
		{spec: "2026-08-09 10:00..2026-08-09 12:00", since: day(9, 10, 0), until: day(9, 12, 0)},
		{spec: "2026-08-09T10:00:00Z", since: day(9, 10, 0)},
	} {
		t.Run(tt.spec, func(t *testing.T) {
			r, err := ParseRange(tt.spec, now)
			require.NoError(t, err)
			require.True(t, r.Since.Equal(tt.since), "since: got %s, want %s", r.Since, tt.since)
			require.True(t, r.Until.Equal(tt.until), "until: got %s, want %s", r.Until, tt.until)
			require.Equal(t, !tt.until.IsZero(), r.Closed())
		})
	}
}

func TestParseRangeErrors(t *testing.T) {
	for _, spec := range []string{
		"yesteryear",
		"1 hour",
		"0h",
		"-h",
		"1h..6h",       // ends before it starts
		"10:00..09:00", // likewise, spelled as clock times
		"2026-13-01",
		"1h..bogus",
	} {
		t.Run(spec, func(t *testing.T) {
			_, err := ParseRange(spec, now)
			require.Error(t, err)
		})
	}
}

// TestRangeLabel: a duration reads as the window it names, and anything else as
// what was written, since that is already how the person thinks of it.
func TestRangeLabel(t *testing.T) {
	for _, tt := range []struct{ spec, want string }{
		{"", "all"},
		{"1h", "last 1h"},
		{"-30m", "last 30m"},
		{"today", "today"},
		{"6h..1h", "6h..1h"},
	} {
		r, err := ParseRange(tt.spec, now)
		require.NoError(t, err)
		require.Equal(t, tt.want, r.Label())
	}
}

// TestRangeIsRelative: the same spec resolved twice moves with the clock, which
// is what makes "1h" mean the last hour on every run rather than the hour that
// had passed when it was written down.
func TestRangeIsRelative(t *testing.T) {
	first, err := ParseRange("1h", now)
	require.NoError(t, err)
	later, err := ParseRange("1h", now.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, time.Hour, later.Since.Sub(first.Since))
}

// mustRange resolves a spec against [now] for a test that is about what the
// range does, not about how it is written.
func mustRange(t *testing.T, spec string) Range {
	t.Helper()
	r, err := ParseRange(spec, now)
	require.NoError(t, err)
	return r
}

// TestRangeInCommand: every collector that runs a command has its own spelling
// of the same window.
func TestRangeInCommand(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "journalctl reads a local timestamp",
			cfg:  Config{Collector: CollectorJournal, Unit: "kubelet", Range: mustRange(t, "6h..1h")},
			want: "journalctl --no-pager -o cat -u kubelet " +
				"--since '2026-08-11 08:30:00' --until '2026-08-11 13:30:00'",
		},
		{
			name: "kubectl takes rfc 3339",
			cfg:  Config{Collector: CollectorKubectl, Target: "api", Range: mustRange(t, "1h"), Follow: true},
			want: "kubectl logs api --since-time=2026-08-11T13:30:00Z -f",
		},
		{
			name: "docker bounds both ends",
			cfg:  Config{Collector: CollectorDocker, Container: "ch", Range: mustRange(t, "yesterday")},
			want: "docker logs --since 2026-08-10T00:00:00Z --until 2026-08-11T00:00:00Z ch",
		},
		{
			// A window that has already closed has nothing left to arrive in it,
			// whatever the follow toggle says.
			name: "a closed range does not follow",
			cfg: Config{
				Collector: CollectorJournal, Unit: "kubelet",
				Range: mustRange(t, "yesterday"), Follow: true,
			},
			want: "journalctl --no-pager -o cat -u kubelet " +
				"--since '2026-08-10 00:00:00' --until '2026-08-11 00:00:00'",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, tt.cfg.Validate())
			require.Equal(t, tt.want, tt.cfg.Command())
		})
	}
}

// TestRangeUnsupported: where a collector cannot honor a window, saying so
// beats reading a different one than was asked for.
func TestRangeUnsupported(t *testing.T) {
	kube := Config{Collector: CollectorKubectl, Target: "api", Range: mustRange(t, "6h..1h")}
	require.ErrorContains(t, kube.Validate(), "end bound")

	cmd := Config{Collector: CollectorCommand, Args: "dmesg -w", Range: mustRange(t, "1h")}
	require.ErrorContains(t, cmd.Validate(), "command has no time range")
}

// TestRangeInVictoriaLogs: a window narrows the query itself, so the tail limit
// applies within it, and a closed one is never followed.
func TestRangeInVictoriaLogs(t *testing.T) {
	var (
		paths []string
		got   url.Values
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		got = r.URL.Query()
		_, _ = w.Write([]byte(vlogsEntry("2026-08-11T10:00:01Z", "hi")))
	}))
	defer srv.Close()

	cfg := vlogsConfig(srv.URL, true)
	cfg.Range = mustRange(t, "6h..1h")

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	_, err = drain(t, s)
	require.NoError(t, err)

	require.Equal(t, []string{vlogsQueryPath}, paths, "a closed window has nothing to tail")
	require.Equal(t, "2026-08-11T08:30:00Z", got.Get("start"))
	require.Equal(t, "2026-08-11T13:30:00Z", got.Get("end"))
	require.Equal(t, "100", got.Get("limit"))
}

// TestRangeInLoki: Loki reads unix nanoseconds, and the "since" default only
// stands in until there is a window to send instead.
func TestRangeInLoki(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(lokiResult()))
	}))
	defer srv.Close()

	cfg := lokiConfig(srv.URL, true)
	cfg.Range = mustRange(t, "yesterday")

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)
	_, err = drain(t, s)
	require.NoError(t, err)

	require.Empty(t, got.Get("since"))
	require.Equal(t, lokiNanos(cfg.Range.Since), got.Get("start"))
	require.Equal(t, lokiNanos(cfg.Range.Until), got.Get("end"))
	require.Equal(t, "2026-08-10T00:00:00Z", cfg.Range.Since.Format(time.RFC3339))
}

func FuzzParseRange(f *testing.F) {
	for _, s := range []string{"", "1h", "6h..1h", "today", "10:00..12:00", "..", "1h..", "-"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		r, err := ParseRange(spec, now)
		if err != nil {
			require.True(t, r.IsZero())
			return
		}
		if r.Closed() {
			require.True(t, r.Until.After(r.Since), "a closed range covers time")
		}
	})
}
