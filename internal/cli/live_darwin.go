package cli

import "syscall"

const (
	getTerminalMode     = syscall.TIOCGETA
	setTerminalMode     = syscall.TIOCSETA
	restoreTerminalMode = syscall.TIOCSETAF
)
