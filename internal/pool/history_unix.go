//go:build darwin || linux

package pool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const (
	historyRetention = 100
	historySizeLimit = 8 << 20
)

type historyFile struct {
	SchemaVersion int      `json:"schema_version"`
	Results       []Result `json:"results"`
}

func ReadHistory(directory string) ([]Result, error) {
	var history historyFile
	path := filepath.Join(directory, "history.json")
	err := readLimitedJSON(path, &history, historySizeLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if history.SchemaVersion != 1 || len(history.Results) > historyRetention {
		return nil, fmt.Errorf("invalid history format in %s", path)
	}
	return history.Results, nil
}

func RecordResult(ctx context.Context, directory string, result Result) error {
	lock, err := lockState(ctx, directory)
	if err != nil {
		return err
	}
	defer lock.Close()

	results, err := ReadHistory(directory)
	if err != nil {
		return err
	}
	results = append(results, result)
	slices.SortStableFunc(results, func(left, right Result) int {
		return right.FinishedAt.Compare(left.FinishedAt)
	})
	if len(results) > historyRetention {
		results = results[:historyRetention]
	}
	return atomicJSON(directory, filepath.Join(directory, "history.json"), historyFile{1, results})
}
