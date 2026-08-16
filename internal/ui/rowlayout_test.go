package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// otlpStream is what a well-instrumented service sends: one sentence, and
// everything that actually happened beside it as a label.
func otlpStream(t *testing.T, w, h int) logModel {
	t.Helper()
	cfg := source.Config{Collector: source.CollectorLoki, Target: `{service_name="checkout-api"}`}
	lg := newLogs(cfg, logs.NewStore(100), "")
	lg.resize(w, h)

	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	for i, req := range []struct {
		method string
		path   string
		status int
		ms     string
	}{
		{"GET", "/cart/items", 200, "1.66"},
		{"POST", "/orders/9f21", 503, "30.0"},
		{"GET", "/cart/items", 200, "2.10"},
	} {
		lg.append(source.Line{
			Data: []byte("got http request"),
			At:   at.Add(time.Duration(i) * time.Second),
			Labels: []source.Label{
				{Key: "detected_level", Value: "info"},
				{Key: "service_name", Value: "checkout-api"},
				{Key: "service_version", Value: "2.14.0"},
				{Key: "os_description", Value: "Linux 6.11.0 #1 SMP PREEMPT_DYNAMIC storefront build 42"},
				{Key: "host_name", Value: "vm-checkout-01"},
				{Key: "code_function_name", Value: "example.test/internal/httpx.(*Server).handleRequest"},
				{Key: "code_lineno", Value: "212"},
				{Key: "content_type", Value: "application/json"},
				{Key: "trace_id", Value: "9f1d8c7b6a5e4d3c2b1a09f8e7d6c5b" + strconv.Itoa(i)},
				{Key: "method", Value: req.method},
				{Key: "path", Value: req.path},
				{Key: "status", Value: strconv.Itoa(req.status)},
				{Key: "duration_ms", Value: req.ms},
				{Key: "response_size", Value: strconv.Itoa(1284 + i)},
			},
		})
	}
	return lg
}

// rowWith is the drawn row holding word, colors and all.
func rowWith(t *testing.T, view, word string) string {
	t.Helper()
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(ansi.Strip(line), word) {
			return line
		}
	}
	require.Failf(t, "no row", "nothing on screen says %q", word)
	return ""
}

// TestARowShowsWhatTheMessageDoesNotSay: three hundred lines reading "got http
// request" are three hundred different requests, and the method, the route, the
// status and the timing are all in the label set.
func TestARowShowsWhatTheMessageDoesNotSay(t *testing.T) {
	lg := otlpStream(t, 120, 30)
	out := ansi.Strip(lg.View())

	require.Contains(t, out, "got http request  POST  /orders/9f21  503  30ms")
	require.Contains(t, out, "got http request  GET  /cart/items  200  1.66ms")

	// What the whole stream shares is not news about a line of it, whether it
	// is a resource attribute, a code site, or the content type this particular
	// service happens to always answer with.
	for _, word := range []string{
		"checkout-api", "Linux 6.11.0", "vm-checkout-01",
		"handleRequest", "application/json", "9f1d8c7b",
	} {
		require.NotContains(t, out, word, "the list has no room for what every row would repeat")
	}
}

// TestARowFieldIsColoredByWhatItMeans: the status is the thing scanned for, so
// a screenful of requests reads as a shape before it reads as text.
func TestARowFieldIsColoredByWhatItMeans(t *testing.T) {
	view := otlpStream(t, 120, 30).View()

	for _, tt := range []struct{ key, value, word string }{
		{"status", "503", "/orders/9f21"},
		{"status", "200", "/cart/items"},
		{"method", "POST", "/orders/9f21"},
		{"path", "/cart/items", "/cart/items"},
	} {
		colored, ok := logs.HighlightField(tt.key, tt.value)
		require.True(t, ok)
		// The color the field is drawn in and not the whole colored string:
		// the row paints its own background over the text, a run at a time.
		color, _, _ := strings.Cut(colored, tt.value)
		require.NotEmpty(t, color)
		require.Contains(t, rowWith(t, view, tt.word), color, tt.key)
	}
}

// TestARowSpendsTheWidthItHasNow: what the fields are is settled when the line
// arrives and how many of them fit is not, so dragging the window changes what
// is on the row.
func TestARowSpendsTheWidthItHasNow(t *testing.T) {
	lg := otlpStream(t, 120, 30)
	require.Contains(t, ansi.Strip(lg.View()), "503  30ms")

	lg.resize(60, 30)
	narrow := ansi.Strip(rowWith(t, lg.View(), "15:16:37.000"))
	require.Contains(t, narrow, "+4", "and it says how much it could not show")
	require.NotContains(t, narrow, "30ms")

	lg.resize(120, 30)
	require.Contains(t, ansi.Strip(lg.View()), "503  30ms")
}

// TestARowIsStillTheOneLineItWas: the marks for what a row stands for are the
// row's promise about the rest of the entry, so the fields are laid out in what
// is left after them rather than pushing them off the screen.
func TestARowIsStillTheOneLineItWas(t *testing.T) {
	lg := otlpStream(t, 70, 30)
	lg.append(source.Line{
		Data: []byte(`{"level":"error","msg":"boom","error":"wrapped:\n  a.go:1","attempt":2}`),
		At:   time.Now(),
	})
	require.Contains(t, ansi.Strip(rowWith(t, lg.View(), "boom")), "⏎1")
}
