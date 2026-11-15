//go:build darwin || linux

package pool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLockChild(t *testing.T) {
	dir := os.Getenv("KIU_POOL_TEST_DIR")
	if dir == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := Acquire(ctx, dir, func() {
		_ = os.WriteFile(filepath.Join(dir, "waiting"), nil, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.WriteFile(filepath.Join(dir, "started"), nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentProcessWaitsAndStarts(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestLockChild$")
	cmd.Env = append(os.Environ(), "KIU_POOL_TEST_DIR="+dir)
	cmd.Dir = t.TempDir()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "waiting")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not reach waiting state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child started while slot occupied: %v", err)
	}
	release()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); err != nil {
		t.Fatal("child failed to start after release:", err)
	}
}

func TestCancelledWaitDoesNotAcquire(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	other, err := Acquire(ctx, dir, cancel)
	if !errors.Is(err, context.Canceled) || other != nil {
		t.Fatalf("cancelled acquisition: release=%v err=%v", other != nil, err)
	}
	release()
	next, err := Acquire(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	next()
}
