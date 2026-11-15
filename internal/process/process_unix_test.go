//go:build darwin || linux

package process

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type identity struct{ PID, Supervisor, Group int }

func TestMain(m *testing.M) {
	if internal, code := Internal(os.Args[1:]); internal {
		os.Exit(code)
	}
	if len(os.Args) > 1 && os.Args[1] == "__test_client" {
		os.Exit(Run(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
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

func startInvocation(t *testing.T, home, mode string) *invocation {
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
	i.cmd.Env = append(os.Environ(), "HOME="+home, "XDG_STATE_HOME="+home, "KIU_EXECUTION_CONTEXT=")
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
		i := startInvocation(t, t.TempDir(), mode)
		waitExit(t, i, code)
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
