package ui

import (
	"strconv"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/trace"
)

// asks presses the key that opens a trace and runs everything the root model
// does about it, the fetch included — which is the point, since what is being
// counted is whether the fetch happened at all.
func asks(t *testing.T, m tea.Model, key string) tea.Model {
	t.Helper()
	m, cmd := m.Update(k(key))
	require.NotNil(t, cmd, "%s did nothing", key)

	m, cmd = m.Update(cmd())
	if cmd == nil {
		return m
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	return m
}

// The walk between a request and the lines it explains goes both ways, and the
// trace at the end of the second leg is the one already on the wire once.
func TestATraceReadTwiceIsFetchedOnce(t *testing.T) {
	srv := tempoServer(t)
	m := tracingLogs(t, srv.URL, tracedLine)

	m = asks(t, m, "T")
	require.Contains(t, screen(t, m), "gateway POST /checkout")

	m = send(t, m, k("esc"))
	require.Equal(t, stateLogs, m.(Model).state)

	m = asks(t, m, "T")
	require.Equal(t, stateTrace, m.(Model).state)
	require.Contains(t, screen(t, m), "gateway POST /checkout", "and it is drawn, not merely remembered")
	require.NotContains(t, screen(t, m), "fetching trace", "with nothing to wait for")
	require.Equal(t, int64(1), srv.asked.Load())
}

// A request still being served grows after it was first read, and the reader is
// the only one who knows whether it has.
func TestReloadAsksTheStoreAgain(t *testing.T) {
	srv := tempoServer(t)
	m := tracingLogs(t, srv.URL, tracedLine)
	m = asks(t, m, "T")

	m = asks(t, m, "r")
	require.Equal(t, stateTrace, m.(Model).state)
	require.Contains(t, screen(t, m), "gateway POST /checkout")
	require.Equal(t, int64(2), srv.asked.Load())

	// And what it answered replaces what was held, rather than the next open
	// serving the trace the reload was meant to be rid of.
	m = send(t, m, k("esc"))
	asks(t, m, "T")
	require.Equal(t, int64(2), srv.asked.Load())
}

// A trace read from a file has nowhere to ask again.
func TestATraceThatCameFromNowhereCannotBeReloaded(t *testing.T) {
	m := traceModelOf(t, checkout())
	m = pressRoot(t, m, "r")

	require.Equal(t, stateTrace, m.(Model).state)
	require.Contains(t, screen(t, m), "nothing to reload")
}

func tree(id string) *trace.Tree { return &trace.Tree{ID: id} }

// The same id means one request to one system and nothing at all to another.
func TestTwoStoresWithTheSameTraceIDAreTwoTraces(t *testing.T) {
	var c traceCache
	c.put("https://tempo.eu.example.com", "abc", tree("eu"))
	c.put("https://tempo.us.example.com", "abc", tree("us"))

	eu, ok := c.get("https://tempo.eu.example.com", "abc")
	require.True(t, ok)
	require.Equal(t, "eu", eu.ID)

	us, ok := c.get("https://tempo.us.example.com", "abc")
	require.True(t, ok)
	require.Equal(t, "us", us.ID)
}

// A trace is spans all the way down, so what is held is bounded — and what
// falls out is the reading nobody came back to.
func TestTheOldestTraceIsTheOneDropped(t *testing.T) {
	var c traceCache
	const url = "https://tempo.example.com"
	for i := range traceCacheSize + 1 {
		c.put(url, strconv.Itoa(i), tree(strconv.Itoa(i)))
	}

	_, ok := c.get(url, "0")
	require.False(t, ok, "the first read is the first forgotten")
	require.Len(t, c.trees, traceCacheSize)

	for i := 1; i <= traceCacheSize; i++ {
		_, ok := c.get(url, strconv.Itoa(i))
		require.True(t, ok, "and the rest are still there")
	}
}

// Reading one again is not a reason to hold two of it.
func TestReadingTheSameTraceAgainDoesNotFillTheCache(t *testing.T) {
	var c traceCache
	const url = "https://tempo.example.com"
	for range traceCacheSize * 2 {
		c.put(url, "abc", tree("abc"))
	}
	require.Len(t, c.order, 1)
}

func TestDroppingATraceForgetsItEntirely(t *testing.T) {
	var c traceCache
	const url = "https://tempo.example.com"
	c.put(url, "a", tree("a"))
	c.put(url, "b", tree("b"))
	c.drop(url, "a")

	_, ok := c.get(url, "a")
	require.False(t, ok)
	require.Equal(t, []string{traceKey(url, "b")}, c.order, "and leaves no hole behind it")
}
