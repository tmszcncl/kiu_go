//go:build !darwin && !linux

package process

import (
	"fmt"
	"io"
)

func Internal([]string) (bool, int) { return false, 0 }
func Run(_ []string, _ io.Reader, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, "kiu: currently targets macOS and Linux")
	return 1
}
