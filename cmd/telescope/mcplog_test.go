package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheLogNeverGoesToStandardOutput: that is the MCP session itself on this
// transport, so a log written into it would corrupt the very conversation it
// was opened to explain.
func TestTheLogNeverGoesToStandardOutput(t *testing.T) {
	for _, path := range []string{"-", "stdout", "/dev/stdout"} {
		_, _, err := openLog(path)
		require.ErrorContains(t, err, "writes into the MCP session itself", path)
	}
}

// TestNoLogFileIsANilWriterAndNotANilFile: a typed nil reads as present
// everywhere it is checked, which would wrap the transport in a logger that
// writes to nothing and panics on the first message.
func TestNoLogFileIsANilWriterAndNotANilFile(t *testing.T) {
	w, done, err := openLog("")
	require.NoError(t, err)
	require.Nil(t, w, "nil as an interface, so the caller's check means what it says")
	require.NotNil(t, done)
	done()
}

// TestStderrIsNotClosedOnTheWayOut: it is not ours, and the rest of the process
// still needs it.
func TestStderrIsNotClosedOnTheWayOut(t *testing.T) {
	w, done, err := openLog("stderr")
	require.NoError(t, err)
	require.Equal(t, os.Stderr, w)
	done()

	_, err = os.Stderr.Stat()
	require.NoError(t, err, "still open")
}

// TestALogFileIsAppendedTo: a second session is more evidence and not a reason
// to throw the first away.
func TestALogFileIsAppendedTo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.log")
	require.NoError(t, os.WriteFile(path, []byte("first\n"), 0o600))

	w, done, err := openLog(path)
	require.NoError(t, err)
	_, err = w.Write([]byte("second\n"))
	require.NoError(t, err)
	done()

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "first\nsecond\n", string(got))
}
