//go:build darwin || linux

package pool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

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

func TestInvalidReservationFailsClosed(t *testing.T) {
	for _, data := range []string{"{", `{"schema_version":99,"pgid":123}`, `{"schema_version":1,"pgid":0}`, strings.Repeat("x", 4097)} {
		dir := privateTempDir(t)
		if err := os.WriteFile(filepath.Join(dir, "active.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		lease, err := AcquireLease(ctx, dir, nil)
		cancel()
		if err == nil {
			lease.Close()
			t.Fatalf("accepted malformed reservation %q", data[:1])
		}
		if !strings.Contains(err.Error(), filepath.Join(dir, "active.json")) {
			t.Fatalf("missing actionable path: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "active.json")); err != nil {
			t.Fatal("removed unverified reservation")
		}
	}
}

func TestPreviousBootReservationIsCleared(t *testing.T) {
	dir := privateTempDir(t)
	group, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(reservation{SchemaVersion: 1, PGID: group, BootID: "previous-boot"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "active.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := Acquire(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("old boot reservation remains")
	}
}

func TestCurrentBootLiveGroupIsNeverCleared(t *testing.T) {
	dir := privateTempDir(t)
	lease, err := AcquireLease(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	group, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Reserve(group); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	other, err := AcquireLease(ctx, dir, nil)
	if !errors.Is(err, context.DeadlineExceeded) || other != nil {
		t.Fatalf("unsafe admission: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "active.json")); err != nil {
		t.Fatal("live reservation removed")
	}
}

func TestRejectsUnsafeStateFiles(t *testing.T) {
	dir := privateTempDir(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "slot.lock")); err != nil {
		t.Fatal(err)
	}
	if release, err := Acquire(context.Background(), dir, nil); err == nil {
		release()
		t.Fatal("accepted symlink lock")
	}
	dir = t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if release, err := Acquire(context.Background(), dir, nil); err == nil {
		release()
		t.Fatal("accepted non-private state directory")
	}
}

func TestRejectsFIFOReservationWithoutHanging(t *testing.T) {
	dir := privateTempDir(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "active.json"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if lease, err := AcquireLease(ctx, dir, nil); err == nil {
		lease.Close()
		t.Fatal("accepted FIFO reservation")
	}
}

func TestIndependentProcessWaitsAndStarts(t *testing.T) {
	dir := privateTempDir(t)
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
	dir := privateTempDir(t)
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
