package mcp

import (
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

func summaryOf(t *testing.T, cfg config.Config, in summaryInput) (string, summaryOutput) {
	t.Helper()
	res, out, err := summaryHandler(cfg)(t.Context(), nil, in)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	return res.Content[0].(*sdk.TextContent).Text, out
}

// spike is a quiet window with a burst of errors in the middle of it, which is
// the shape a summary exists to show.
func spike(t *testing.T) []entry {
	t.Helper()
	var out []entry
	at := func(i int) string {
		return time.Date(2026, 8, 17, 12, i/60, i%60, 0, time.UTC).Format(time.RFC3339Nano)
	}
	for i := range 120 {
		out = append(out, entry{at: at(i), level: "info", pod: "api-1", msg: "got http request"})
		if i >= 60 && i < 70 {
			out = append(out, entry{
				at: at(i), level: "error", pod: "api-2", msg: "connect: connection refused",
			})
		}
	}
	return out
}

// TestSummaryCountsRatherThanLists: the first question about an incident is
// what happened and not which fifty lines to read, and a count is a hundredth
// of the size of the lines it was counted over.
func TestSummaryCountsRatherThanLists(t *testing.T) {
	srv, _ := logStore(t, spike(t))
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	text, out := summaryOf(t, cfg, summaryInput{Place: "prod"})
	require.Equal(t, 130, out.Counted)
	require.True(t, out.Covered)
	require.Equal(t, []tally{{Name: "INFO", Count: 120}, {Name: "ERROR", Count: 10}}, out.Levels)
	require.Equal(t, tally{Name: "got http request", Count: 120}, out.Messages[0])
	require.Contains(t, text, "levels: INFO=120 ERROR=10")
	require.Less(t, len(text), 2000, "a summary of 130 lines is not the size of 130 lines")
}

// TestSummaryShowsWhereTheVolumeWent: a thousand errors spread evenly is a
// broken dependency and a thousand in one bucket is an incident, and the total
// cannot tell them apart.
func TestSummaryShowsWhereTheVolumeWent(t *testing.T) {
	srv, _ := logStore(t, spike(t))
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	_, out := summaryOf(t, cfg, summaryInput{Place: "prod"})
	require.NotEmpty(t, out.Buckets)

	var errors int
	for _, b := range out.Buckets {
		if b.Errors > 0 {
			errors++
		}
	}
	require.LessOrEqual(t, errors, 2, "the burst is in one part of the window, not spread over it")
}

// TestSummaryCountsOneField: what a summary names is what the next filter is
// written against.
func TestSummaryCountsOneField(t *testing.T) {
	srv, _ := logStore(t, spike(t))
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	text, out := summaryOf(t, cfg, summaryInput{Place: "prod", By: "pod"})
	require.Equal(t, []tally{{Name: "api-1", Count: 120}, {Name: "api-2", Count: 10}}, out.By)
	require.Contains(t, text, "by pod:")
}

// TestSummaryOverAFilter: the counts are of what the filter selected, so
// narrowing and counting again is how a suspicion is checked.
func TestSummaryOverAFilter(t *testing.T) {
	srv, _ := logStore(t, spike(t))
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	_, out := summaryOf(t, cfg, summaryInput{Place: "prod", Filter: "level>=error"})
	require.Equal(t, 10, out.Counted)
	require.Equal(t, []tally{{Name: "ERROR", Count: 10}}, out.Levels)
}

// TestSummarySaysWhenItCountedASample: a count over part of a window is a
// shape and not a total, and only one of those can be quoted.
func TestSummarySaysWhenItCountedASample(t *testing.T) {
	var many []entry
	for i := range summaryReach + 10 {
		many = append(many, entry{
			at:    time.Date(2026, 8, 17, 0, 0, 0, i*int(time.Millisecond), time.UTC).Format(time.RFC3339Nano),
			level: "info", pod: "api-1", msg: "got http request",
		})
	}
	srv, _ := logStore(t, many)
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	text, out := summaryOf(t, cfg, summaryInput{Place: "prod"})
	require.False(t, out.Covered)
	require.Equal(t, summaryReach, out.Counted)
	require.Contains(t, out.Note, "are not totals")
	require.True(t, strings.HasSuffix(text, out.Note+"\n"))
}
