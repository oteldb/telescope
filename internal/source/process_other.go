//go:build windows

package source

import "os/exec"

func isolate(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
