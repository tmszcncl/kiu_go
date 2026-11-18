//go:build !darwin && !linux

package cli

import "io"

func liveStatus(_ io.Reader, stdout, stderr io.Writer) int {
	return status(stdout, stderr, false)
}
