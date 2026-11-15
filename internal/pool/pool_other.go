//go:build !darwin && !linux

package pool

import (
	"context"
	"errors"
)

func StateDir() (string, error) {
	return "", errors.New("Kiu currently targets macOS and Linux")
}

func Acquire(context.Context, string, func()) (func(), error) {
	return nil, errors.New("Kiu currently targets macOS and Linux")
}
