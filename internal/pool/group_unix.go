//go:build darwin || linux

package pool

import (
	"errors"
	"syscall"
)

// GroupAlive is conservative: permission errors must never free capacity.
func GroupAlive(pgid int) (bool, error) {
	if pgid <= 1 {
		return false, errors.New("invalid process group")
	}
	err := syscall.Kill(-pgid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return true, err
	}
	return true, nil
}
