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

func TestRegistrationCrashChild(t *testing.T) {
	directory := os.Getenv("KIU_REGISTRATION_TEST_DIR")
	if directory == "" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(97)
	}
	if _, err := Register(context.Background(), directory, "abandoned waiter", "test", cwd, os.Getppid()); err != nil {
		os.Exit(98)
	}
	os.Exit(0) // Simulate a supervisor exiting without registration cleanup.
}

func abandonedRegistration(t *testing.T, directory string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestRegistrationCrashChild$")
	cmd.Env = append(os.Environ(), "KIU_REGISTRATION_TEST_DIR="+directory)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("registration helper: %v: %s", err, output)
	}
}

func TestNewRegistrationPrunesAbandonedOwnerLocks(t *testing.T) {
	directory := privateTempDir(t)
	abandonedRegistration(t, directory)
	cwd, _ := os.Getwd()
	r, err := Register(context.Background(), directory, "new job", "test", cwd, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(directory, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("abandoned metadata remains: %v", entries)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(filepath.Join(directory, "tasks"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cleanup: %v, %v", entries, err)
	}
}

func TestStatusUsesOwnershipLocksInsteadOfPIDExistence(t *testing.T) {
	directory := privateTempDir(t)
	abandonedRegistration(t, directory)
	paths, err := filepath.Glob(filepath.Join(directory, "tasks", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("fixture: %v %v", paths, err)
	}
	var task Task
	if err := readJSON(paths[0], &task); err != nil {
		t.Fatal(err)
	}
	// Model PID reuse by an unrelated live process. The released ownership lock,
	// rather than this live PID, proves that the waiting record is abandoned.
	task.SupervisorPID = os.Getpid()
	data, _ := json.Marshal(task)
	if err := os.WriteFile(paths[0], data, 0600); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if status.WaitingCount != 0 || status.RunningCount != 0 {
		t.Fatalf("ghost job from reused PID: %+v", status)
	}
}

func TestStatusRejectsSymlinkTaskDirectory(t *testing.T) {
	directory := privateTempDir(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(directory, "tasks")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(context.Background(), directory); err == nil {
		t.Fatal("accepted symlink task directory")
	}
}

func TestClientPIDOneAndNonUTF8DefaultName(t *testing.T) {
	directory := privateTempDir(t)
	cwd := "/tmp/project-" + string([]byte{0xff})
	r, err := Register(context.Background(), directory, "", "go", cwd, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	status, err := ReadStatus(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if status.WaitingCount != 1 || status.Waiting[0].ClientPID != 1 || status.Waiting[0].Name == "" || !status.Waiting[0].DetailsAvailable {
		t.Fatalf("invalid display metadata: %+v", status)
	}
}

func TestForeignFilesDoNotBlockJobsAndTemporaryFilesArePruned(t *testing.T) {
	directory := privateTempDir(t)
	tasksDir := filepath.Join(directory, "tasks")
	if err := os.Mkdir(tasksDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"foreign.lock", "foreign.json", ".metadata-abandoned"} {
		if err := os.WriteFile(filepath.Join(tasksDir, name), []byte("not job metadata"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, ".metadata-abandoned"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	r, err := Register(context.Background(), directory, "new job", "go", cwd, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	status, err := ReadStatus(context.Background(), directory)
	if err != nil || status.WaitingCount != 1 {
		t.Fatalf("foreign files affected snapshot: %+v %v", status, err)
	}
	for _, dir := range []string{directory, tasksDir} {
		if _, err := os.Stat(filepath.Join(dir, ".metadata-abandoned")); !os.IsNotExist(err) {
			t.Fatal("temporary metadata remains")
		}
	}
	if _, err := os.Stat(filepath.Join(tasksDir, "foreign.json")); err != nil {
		t.Fatal("foreign file was removed")
	}
}

func TestLegacyReservationHasExplicitlyUnavailableDetails(t *testing.T) {
	directory := privateTempDir(t)
	lease, err := AcquireLease(context.Background(), directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	group, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Reserve(group); err != nil {
		t.Fatal(err)
	}
	defer lease.Clear()
	status, err := ReadStatus(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if !status.SlotOccupied || status.RunningCount != 1 || status.Running[0].DetailsAvailable {
		t.Fatalf("legacy details were invented: %+v", status)
	}
}

func TestPartiallyPublishedReservationCanBeClearedBeforeCommit(t *testing.T) {
	directory := privateTempDir(t)
	cwd, _ := os.Getwd()
	r, err := Register(context.Background(), directory, "startup failure", "go", cwd, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	lease, err := AcquireLease(context.Background(), directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	path := filepath.Join(directory, "tasks", r.Task.ID+".json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	group, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.ReserveTask(group, r); err == nil {
		t.Fatal("fixture did not fail its second metadata write")
	}
	if _, err := os.Stat(filepath.Join(directory, "active.json")); err != nil {
		t.Fatal("reservation was not published before task-file failure")
	}
	if err := lease.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "active.json")); !os.IsNotExist(err) {
		t.Fatal("failed-start reservation remains")
	}
}

func TestReservationWinsOverStaleWaitingCopy(t *testing.T) {
	directory := privateTempDir(t)
	cwd, _ := os.Getwd()
	r, err := Register(context.Background(), directory, "partial publication", "go", cwd, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	waitingCopy := r.Task
	lease, err := AcquireLease(context.Background(), directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	group, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.ReserveTask(group, r); err != nil {
		t.Fatal(err)
	}
	defer lease.Clear()
	// Model a successful reservation write followed by a failed display update.
	if err := atomicJSON(filepath.Join(directory, "tasks"), filepath.Join(directory, "tasks", r.Task.ID+".json"), waitingCopy); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if status.RunningCount != 1 || status.WaitingCount != 0 {
		t.Fatalf("double-counted one job: %+v", status)
	}
}

func TestCoordinationCancellationReleasesAdmissionLock(t *testing.T) {
	directory := privateTempDir(t)
	lock, err := lockState(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	lease, err := AcquireLease(ctx, directory, nil)
	if lease != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation did not return: %v", err)
	}
	_ = lock.Close()
	ctx, cancelNext := context.WithTimeout(context.Background(), time.Second)
	defer cancelNext()
	next, err := AcquireLease(ctx, directory, nil)
	if err != nil {
		t.Fatalf("cancelled acquisition leaked the slot: %v", err)
	}
	next.Close()
}

func TestInvalidByteExecutableGetsBoundedDefaultLabel(t *testing.T) {
	directory := privateTempDir(t)
	r, err := Register(context.Background(), directory, "", "/tmp/"+strings.Repeat(string([]byte{0xff}), 255), "/tmp/project", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.Task.Name) > 512 {
		t.Fatalf("unbounded derived label: %d", len(r.Task.Name))
	}
}
