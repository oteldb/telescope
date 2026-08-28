package view

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestARealShellSplitsALinkTheSameWay: the quoting is a claim about what sh
// does, and the only thing that settles it is sh. The hand-written splitter
// beside it says what we meant; this says whether we were right.
func TestARealShellSplitsALinkTheSameWay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a link is quoted for a POSIX shell")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to ask")
	}

	v := View{
		Place: `humo observer`,
		Query: `"connection refused" pod=api-* && it's $HOME ` + "`id`" + ` ;rm -rf /`,
		Range: "2026-01-02 10:00..2026-01-02 12:00",
	}
	// The link is fed to sh with telescope replaced by a printf that writes each
	// argument on its own line, so what comes back is exactly the argv a shell
	// would have handed the real binary.
	script := strings.Replace(v.Link(), Program, `printf '%s\n'`, 1)

	out, err := exec.Command(sh, "-c", script).Output()
	require.NoError(t, err)
	require.Equal(t,
		[]string{v.Place, "--" + FlagQuery, v.Query, "--" + FlagRange, v.Range},
		strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"),
		"every argument survives, and nothing in the filter was run")
}
