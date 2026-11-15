//go:build !darwin && !linux

package cli

import "os/exec"

func childExitCode(err *exec.ExitError) int {
	return err.ExitCode()
}
