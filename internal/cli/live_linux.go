package cli

import "syscall"

const (
	getTerminalMode     = syscall.TCGETS
	setTerminalMode     = syscall.TCSETS
	restoreTerminalMode = syscall.TCSETS
)
