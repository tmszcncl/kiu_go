//go:build !darwin && !linux

package pool

import (
	"context"
	"errors"
)

func ReadStatus(context.Context, string) (Snapshot, error) {
	return Snapshot{}, errors.New("Kiu currently targets macOS and Linux")
}
