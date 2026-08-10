//go:build !windows

package source

import (
	"os/exec"
	"syscall"
)

// isolate puts the command in its own process group so the whole pipeline can
// be signalled, not just the shell that spawned it.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate interrupts the command's process group, falling back to the
// process itself if the group is already gone.
func terminate(cmd *exec.Cmd) error {
	p := cmd.Process
	if p == nil {
		return nil
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGINT); err == nil {
		return nil
	}
	return p.Signal(syscall.SIGINT)
}
