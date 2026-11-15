//go:build darwin || linux

package cli

import (
	"os/exec"
	"syscall"
)

func childExitCode(err *exec.ExitError) int {
	if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return err.ExitCode()
}
