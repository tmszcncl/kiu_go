package pool

import "syscall"

func bootID() (string, error) { return syscall.Sysctl("kern.bootsessionuuid") }
