//go:build darwin || linux

package pool

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestHistoryRetentionAndConcurrentWriters(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	results, err := ReadHistory(directory)
	if err != nil || len(results) != 0 {
		t.Fatalf("empty history = %v, err = %v", results, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var writers sync.WaitGroup
	for worker := range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for index := worker; index < historyRetention+8; index += 4 {
				result := Result{ID: strconv.Itoa(index), FinishedAt: time.Unix(int64(index), 0)}
				if err := RecordResult(ctx, directory, result); err != nil {
					t.Errorf("record %d: %v", index, err)
					return
				}
			}
		}()
	}
	writers.Wait()
	results, err = ReadHistory(directory)
	if err != nil || len(results) != historyRetention {
		t.Fatalf("read history: got %d entries, err = %v", len(results), err)
	}
	for index, result := range results {
		if want := strconv.Itoa(historyRetention + 7 - index); result.ID != want {
			t.Fatalf("entry %d = %s, want %s", index, result.ID, want)
		}
	}
	info, err := os.Stat(filepath.Join(directory, "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("history permissions = %o", info.Mode().Perm())
	}
}
