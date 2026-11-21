//go:build darwin || linux

package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tmszcncl/kiu_go/internal/pool"
)

type identity struct{ PID, Supervisor, Group int }

func TestMain(m *testing.M) {
	if internal, code := Internal(os.Args[1:]); internal {
		os.Exit(code)
	}
	if len(os.Args) > 1 && os.Args[1] == "__test_client" {
		os.Exit(RunNamed(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, os.Getenv("KIU_TEST_LABEL")))
	}
	if len(os.Args) > 1 && os.Args[1] == "__test_job" {
		os.Exit(testJob(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func testJob(args []string) int {
	mode, ready, release := args[0], args[1], args[2]
	if mode == "linger" || mode == "tree" {
		child := exec.Command(os.Args[0], "__test_job", "hold", ready+"-descendant", release)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			return 98
		}
	}
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
	group, _ := syscall.Getpgid(0)
	data, _ := json.Marshal(identity{os.Getpid(), os.Getppid(), group})
	if err := os.WriteFile(ready, data, 0600); err != nil {
		return 99
	}
	if mode == "finish" || mode == "linger" {
		return 0
	}
	if mode == "exit42" {
		return 42
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(release); err == nil {
			return 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 97
}

type invocation struct {
	cmd                 *exec.Cmd
	done                chan error
	ready, release, log string
}

func startInvocation(t *testing.T, home, mode string, label ...string) *invocation {
	t.Helper()
	dir := t.TempDir()
	i := &invocation{ready: filepath.Join(dir, "ready"), release: filepath.Join(dir, "release"), log: filepath.Join(dir, "log"), done: make(chan error, 1)}
	log, err := os.Create(i.log)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	i.cmd = exec.Command(exe, "__test_client", exe, "__test_job", mode, i.ready, i.release)
	i.cmd.Dir = t.TempDir() // Independent directories must still share capacity.
	name := ""
	if len(label) != 0 {
		name = label[0]
	}
	i.cmd.Env = append(os.Environ(), "HOME="+home, "XDG_STATE_HOME="+home, "KIU_EXECUTION_CONTEXT=", "KIU_HERDR_STATUS=", "KIU_TEST_LABEL="+name, "KIU_PRIVATE_CANARY=environment-secret-must-not-persist")
	i.cmd.Stdout, i.cmd.Stderr = log, log
	if err := i.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { i.done <- i.cmd.Wait(); _ = log.Close() }()
	t.Cleanup(func() {
		_ = os.WriteFile(i.release, nil, 0600)
		_ = i.cmd.Process.Kill()
	})
	return i
}

func waitReady(t *testing.T, path string) identity {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		var id identity
		if err == nil && json.Unmarshal(data, &id) == nil {
			return id
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job did not become ready: %s", path)
	return identity{}
}

func waitExit(t *testing.T, i *invocation, expected int) {
	t.Helper()
	select {
	case err := <-i.done:
		if code := exitCode(err); code != expected {
			log, _ := os.ReadFile(i.log)
			t.Fatalf("exit %d, want %d: %s", code, expected, log)
		}
	case <-time.After(9 * time.Second):
		log, _ := os.ReadFile(i.log)
		t.Fatalf("invocation did not exit: %s", log)
	}
}

func assertWaiting(t *testing.T, i *invocation) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(i.log)
		if strings.Contains(string(data), "waiting for a slot") {
			if _, err := os.Stat(i.ready); !os.IsNotExist(err) {
				t.Fatal("waiting child was launched")
			}
			return
		}
		select {
		case err := <-i.done:
			t.Fatalf("waiter exited early: %v: %s", err, data)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("invocation did not wait")
}

func releaseJob(t *testing.T, i *invocation) {
	t.Helper()
	if err := os.WriteFile(i.release, nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClientKillKeepsCapacityUntilCleanup(t *testing.T) {
	home := t.TempDir()
	first := startInvocation(t, home, "hold")
	waitReady(t, first.ready)
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, first, 137)
	second := startInvocation(t, home, "finish")
	assertWaiting(t, second)
	releaseJob(t, first)
	waitExit(t, second, 0)
}

func TestSupervisorKillPreservesReservation(t *testing.T) {
	home := t.TempDir()
	first := startInvocation(t, home, "hold")
	id := waitReady(t, first.ready)
	if err := syscall.Kill(id.Supervisor, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitExit(t, first, 137)
	second := startInvocation(t, home, "finish")
	assertWaiting(t, second)
	releaseJob(t, first)
	waitExit(t, second, 0)
}

func TestWaitingCancellationDoesNotLaunch(t *testing.T) {
	for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(strconv.Itoa(int(s)), func(t *testing.T) {
			home := t.TempDir()
			first := startInvocation(t, home, "hold")
			waitReady(t, first.ready)
			second := startInvocation(t, home, "finish")
			assertWaiting(t, second)
			if err := second.cmd.Process.Signal(s); err != nil {
				t.Fatal(err)
			}
			waitExit(t, second, 128+int(s))
			results, err := pool.ReadHistory(testStateDir(home))
			if err != nil || len(results) != 1 || results[0].Outcome != pool.OutcomeCancelled ||
				results[0].StartedAt != nil || results[0].ExitCode != 128+int(s) {
				t.Fatalf("waiting cancellation history = %+v, err = %v", results, err)
			}
			releaseJob(t, first)
			waitExit(t, first, 0)
			if _, err := os.Stat(second.ready); !os.IsNotExist(err) {
				t.Fatal("cancelled waiter launched command")
			}
			third := startInvocation(t, home, "finish")
			waitExit(t, third, 0)
		})
	}
}

func TestCancellationGraceAndSecondSignal(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			home := t.TempDir()
			first := startInvocation(t, home, "hold")
			waitReady(t, first.ready)
			started := time.Now()
			if err := first.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			second := startInvocation(t, home, "finish")
			assertWaiting(t, second)
			if force {
				if err := first.cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			waitExit(t, first, 143)
			if !force && time.Since(started) < gracePeriod {
				t.Fatal("ignored grace period")
			}
			waitExit(t, second, 0)
		})
	}
}

func TestDescendantRetainsCapacityAfterLeaderExit(t *testing.T) {
	home := t.TempDir()
	first := startInvocation(t, home, "linger")
	waitReady(t, first.ready+"-descendant")
	second := startInvocation(t, home, "finish")
	assertWaiting(t, second)
	releaseJob(t, first)
	waitExit(t, first, 0)
	waitExit(t, second, 0)
}

func TestNormalExitCodes(t *testing.T) {
	for _, code := range []int{0, 42} {
		mode := "finish"
		if code == 42 {
			mode = "exit42"
		}
		home := t.TempDir()
		i := startInvocation(t, home, mode, "Test result")
		waitExit(t, i, code)
		results, err := pool.ReadHistory(testStateDir(home))
		if err != nil || len(results) != 1 || results[0].Name != "Test result" ||
			results[0].ExitCode != code || results[0].Outcome != pool.OutcomeExited ||
			results[0].StartedAt == nil || results[0].FinishedAt.Before(*results[0].StartedAt) {
			t.Fatalf("history = %+v, err = %v", results, err)
		}
	}
}

func TestGateEOFNeverExecutesCommand(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	cmd := exec.Command(exe, internalMode, "worker", exe, "__test_job", "finish", ready, filepath.Join(dir, "release"))
	cmd.ExtraFiles = []*os.File{read}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = read.Close()
	_ = write.Close() // The supervisor disappeared before committing startup.
	if code := exitCode(cmd.Wait()); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(ready); !os.IsNotExist(err) {
		t.Fatal("worker executed without gate permission")
	}
}

func TestDirectChildKillReleasesCapacity(t *testing.T) {
	home := t.TempDir()
	first := startInvocation(t, home, "hold")
	id := waitReady(t, first.ready)
	if err := syscall.Kill(id.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitExit(t, first, 137)
	second := startInvocation(t, home, "finish")
	waitExit(t, second, 0)
}

func TestDirectChildCrashGivesDescendantsGrace(t *testing.T) {
	home := t.TempDir()
	first := startInvocation(t, home, "tree")
	id := waitReady(t, first.ready)
	waitReady(t, first.ready+"-descendant")
	if err := syscall.Kill(id.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	second := startInvocation(t, home, "finish")
	assertWaiting(t, second)
	// The descendant ignores TERM and must still own capacity during its grace.
	releaseJob(t, first)
	waitExit(t, first, 137)
	waitExit(t, second, 0)
}

func testStateDir(home string) string {
	directory := filepath.Join(home, "kiu")
	if runtime.GOOS == "darwin" {
		directory = filepath.Join(home, "Library", "Application Support", "kiu", "state")
	}
	return directory
}

func snapshot(t *testing.T, home string) pool.Snapshot {
	t.Helper()
	directory := testStateDir(home)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := pool.ReadStatus(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNamedQueueSnapshotsCancellationCrashAndPrivacy(t *testing.T) {
	home := t.TempDir()
	first := startInvocation(t, home, "hold", "Tidudanka — full CI")
	id := waitReady(t, first.ready)
	second := startInvocation(t, home, "hold", "Calculyo — system tests")
	assertWaiting(t, second)
	third := startInvocation(t, home, "hold", "Flutter — build")
	assertWaiting(t, third)
	status := snapshot(t, home)
	if status.RunningCount != 1 || status.WaitingCount != 2 || status.Running[0].Name != "Tidudanka — full CI" {
		t.Fatalf("unexpected snapshot: %+v", status)
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 5 {
				directory := filepath.Join(home, "kiu")
				if runtime.GOOS == "darwin" {
					directory = filepath.Join(home, "Library", "Application Support", "kiu", "state")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				got, err := pool.ReadStatus(ctx, directory)
				cancel()
				if err != nil {
					t.Errorf("concurrent snapshot: %v", err)
					return
				}
				if got.RunningCount != 1 || got.WaitingCount != 2 {
					t.Errorf("inconsistent concurrent snapshot: %+v", got)
				}
			}
		}()
	}
	readers.Wait()
	data, err := os.ReadFile(second.log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Calculyo — system tests") || !strings.Contains(string(data), "slot occupied by Tidudanka — full CI") || !strings.Contains(string(data), "waiting jobs:") {
		t.Fatalf("missing queue explanation: %s", data)
	}
	directory := filepath.Join(home, "kiu")
	if runtime.GOOS == "darwin" {
		directory = filepath.Join(home, "Library", "Application Support", "kiu", "state")
	}
	if err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), first.ready) || strings.Contains(string(data), "environment-secret-must-not-persist") {
			t.Fatalf("private args/environment persisted in %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := second.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitExit(t, second, 143)
	if got := snapshot(t, home); got.WaitingCount != 1 {
		t.Fatalf("cancelled waiter remains: %+v", got)
	}
	if err := syscall.Kill(id.Supervisor, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitExit(t, first, 137)
	if got := snapshot(t, home); got.RunningCount != 1 || got.Running[0].Name != "Tidudanka — full CI" || got.WaitingCount != 1 {
		t.Fatalf("lost crash reservation metadata: %+v", got)
	}
	releaseJob(t, first)
	waitReady(t, third.ready)
	data, err = os.ReadFile(third.log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "kiu: starting — Flutter — build (waited ") {
		t.Fatalf("missing resumed message: %s", data)
	}
	releaseJob(t, third)
	waitExit(t, third, 0)
	if got := snapshot(t, home); got.RunningCount != 0 || got.WaitingCount != 0 {
		t.Fatalf("completed tasks remain: %+v", got)
	}
	entries, err := os.ReadDir(filepath.Join(directory, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("completed metadata/locks accumulated: %v", entries)
	}
}

func TestFreeInvocationIsQuiet(t *testing.T) {
	i := startInvocation(t, t.TempDir(), "finish", "quiet job")
	waitExit(t, i, 0)
	data, err := os.ReadFile(i.log)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("unexpected free-slot banner: %q", data)
	}
}
