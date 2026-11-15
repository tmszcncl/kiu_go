//go:build darwin || linux

// Package process supervises foreground jobs without a permanent daemon.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tmszcncl/kiu_go/internal/pool"
)

const internalMode = "__kiu_internal_v1"
const gracePeriod = 5 * time.Second

// Internal dispatches same-binary helpers before normal CLI parsing. A pipe on
// descriptor 3 is required; invoking the hidden command alone cannot start work.
func Internal(args []string) (bool, int) {
	if len(args) == 0 || args[0] != internalMode {
		return false, 0
	}
	if len(args) < 3 {
		return true, 2
	}
	control := os.NewFile(3, "kiu-control")
	if control == nil {
		return true, 2
	}
	defer control.Close()
	info, err := control.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return true, 2
	}
	switch args[1] {
	case "supervise":
		if len(args) < 4 {
			return true, 2
		}
		return true, supervise(args[3:], control, args[2])
	case "worker":
		return true, worker(args[2:], control)
	default:
		return true, 2
	}
}

// Run starts a supervisor in a separate process group. The control pipe is also
// a liveness channel: SIGKILL of the client closes it and triggers cancellation.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return RunNamed(args, stdin, stdout, stderr, "")
}

func RunNamed(args []string, stdin io.Reader, stdout, stderr io.Writer, name string) int {
	executable, err := os.Executable()
	if err != nil {
		return failure(stderr, err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		return failure(stderr, err)
	}
	defer read.Close()
	defer write.Close()
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	cmd := exec.Command(executable, append([]string{internalMode, "supervise", name}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.ExtraFiles = []*os.File{read}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return failure(stderr, err)
	}
	_ = read.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	forwarded := 0
	for {
		select {
		case err := <-done:
			return exitCode(err)
		case received := <-signals:
			// Bound writes even if a stopped supervisor cannot drain its pipe.
			if forwarded < 2 {
				_, _ = write.Write([]byte{byte(received.(syscall.Signal))})
				forwarded++
			}
		}
	}
}

func supervise(args []string, control *os.File, name string) (code int) {
	// Diagnostics must not stop the background supervisor under stty tostop.
	signal.Ignore(syscall.SIGTTOU)
	reporter := statusReporter{name: displayExecutable(args[0])}
	if os.Getenv("KIU_HERDR_STATUS") == "1" && os.Getenv("HC_TERMINAL_ID") != "" {
		if info, err := os.Stderr.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			reporter.output = os.Stderr
		}
	}
	defer func() {
		if code == 0 {
			reporter.report("done", "Finished")
		} else {
			reporter.report("error", fmt.Sprintf("Exited %d", code))
		}
	}()
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	events := make(chan syscall.Signal, 16)
	go func() {
		var buffer [1]byte
		for {
			_, err := io.ReadFull(control, buffer[:])
			if err != nil {
				events <- syscall.SIGTERM
				return
			}
			s := syscall.Signal(buffer[0])
			if s != syscall.SIGINT && s != syscall.SIGTERM && s != syscall.SIGHUP && s != syscall.SIGQUIT {
				s = syscall.SIGTERM
			}
			events <- s
		}
	}()
	go func() {
		for s := range signals {
			events <- s.(syscall.Signal)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acquired := make(chan struct{}, 1)
	firstSignal := make(chan syscall.Signal, 1)
	watcherDone := make(chan struct{})
	// Only this goroutine reads cancellation until acquisition has completed.
	go func() {
		defer close(watcherDone)
		select {
		case s := <-events:
			firstSignal <- s
			cancel()
		case <-acquired:
		}
	}()
	directory, err := pool.StateDir()
	if err != nil {
		return failure(os.Stderr, err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return failure(os.Stderr, err)
	}
	registration, err := pool.Register(ctx, directory, name, args[0], cwd, os.Getppid())
	if err != nil {
		acquired <- struct{}{}
		<-watcherDone
		if ctx.Err() != nil {
			return 128 + int(<-firstSignal)
		}
		return failure(os.Stderr, err)
	}
	defer func() {
		if err := registration.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "kiu: clean task record:", err)
			if code == 0 {
				code = 1
			}
		}
	}()
	reporter.name = pool.DisplayText(registration.Task.Name)
	reporter.report("working", "Starting")
	waited := false
	lease, err := pool.AcquireLease(ctx, directory, func() {
		waited = true
		fmt.Fprintf(os.Stderr, "kiu: waiting for a slot — %s\n", reporter.name)
		snapshot, err := pool.ReadStatus(ctx, directory)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintln(os.Stderr, "kiu: slot details unavailable:", err)
			reporter.report("working", "Waiting for slot")
			return
		}
		message := "Waiting for slot"
		if len(snapshot.Running) == 0 && snapshot.SlotOccupied {
			fmt.Fprintln(os.Stderr, "kiu: slot occupied; job details unavailable (older Kiu or startup transition)")
		}
		for _, running := range snapshot.Running {
			occupied := pool.DisplayText(running.Name)
			if running.StartedAt != nil {
				fmt.Fprintf(os.Stderr, "kiu: slot occupied by %s (running for %s)\n", occupied, elapsedSince(*running.StartedAt))
			} else {
				fmt.Fprintf(os.Stderr, "kiu: slot occupied by %s\n", occupied)
			}
			message += "; running: " + occupied
		}
		fmt.Fprintf(os.Stderr, "kiu: waiting jobs: %d (including this job)\n", snapshot.WaitingCount)
		reporter.report("working", message)
	})
	acquired <- struct{}{}
	<-watcherDone
	if err != nil {
		if ctx.Err() != nil {
			return 128 + int(<-firstSignal)
		}
		return failure(os.Stderr, err)
	}
	defer lease.Close()
	if ctx.Err() != nil {
		return 128 + int(<-firstSignal)
	}
	executable, err := os.Executable()
	if err != nil {
		return failure(os.Stderr, err)
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return failure(os.Stderr, err)
	}
	defer gateRead.Close()
	defer gateWrite.Close()
	cmd := exec.Command(executable, append([]string{internalMode, "worker"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{gateRead}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return failure(os.Stderr, err)
	}
	_ = gateRead.Close()
	pgid := cmd.Process.Pid
	if err := lease.ReserveTask(pgid, registration); err != nil {
		_ = gateWrite.Close() // EOF: worker exits without executing the command.
		_ = cmd.Wait()
		// No commit byte was sent, so this worker could not execute user code
		// or start descendants. Remove any partially published reservation.
		if cleanupErr := lease.Clear(); cleanupErr != nil {
			fmt.Fprintln(os.Stderr, "kiu: clean failed reservation:", cleanupErr)
		}
		return failure(os.Stderr, err)
	}
	// The durable reservation is visible before any user code can execute.
	select {
	case s := <-events:
		_ = gateWrite.Close()
		_ = cmd.Wait()
		if err := lease.Clear(); err != nil {
			return failure(os.Stderr, err)
		}
		return 128 + int(s)
	default:
	}
	if _, err := gateWrite.Write([]byte{1}); err != nil {
		_ = gateWrite.Close()
		waitErr := cmd.Wait()
		if alive, checkErr := pool.GroupAlive(pgid); checkErr == nil && !alive {
			if cleanupErr := lease.Clear(); cleanupErr != nil {
				fmt.Fprintln(os.Stderr, "kiu: clean failed gate reservation:", cleanupErr)
			}
		}
		fmt.Fprintln(os.Stderr, "kiu: commit worker startup:", err)
		if waitErr != nil {
			return exitCode(waitErr)
		}
		return 1
	}
	_ = gateWrite.Close()
	if waited {
		fmt.Fprintf(os.Stderr, "kiu: starting — %s (waited %s)\n", reporter.name, elapsedSince(registration.Task.SubmittedAt))
	}
	reporter.report("working", "Running")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var childErr error
	childDone := false
	announcedDescendants := false
	var cancellation syscall.Signal
	var deadline <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case s := <-events:
			if cancellation == 0 {
				reporter.report("working", "Stopping")
				cancellation = s
				_ = syscall.Kill(-pgid, s)
				timer = time.NewTimer(gracePeriod)
				deadline = timer.C
			} else {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		case <-deadline:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			deadline = nil
		case childErr = <-done:
			childDone = true
			done = nil
			// A directly signalled command may leave descendants behind. Apply
			// the same grace policy to them as for a cancelled client.
			var exited *exec.ExitError
			if cancellation == 0 && errors.As(childErr, &exited) {
				if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					cancellation = status.Signal()
					// Crashes (SEGV/ABRT/etc.) must not be propagated as
					// crashes to unrelated descendants. Preserve the exit
					// status, but give descendants a normal TERM grace.
					_ = syscall.Kill(-pgid, syscall.SIGTERM)
					timer = time.NewTimer(gracePeriod)
					deadline = timer.C
				}
			}
		case <-ticker.C:
		}
		if childDone {
			alive, err := pool.GroupAlive(pgid)
			if err != nil {
				return failure(os.Stderr, err)
			} // Reservation remains fail-closed.
			if !alive {
				if err := lease.Clear(); err != nil {
					return failure(os.Stderr, err)
				}
				if cancellation != 0 {
					return 128 + int(cancellation)
				}
				return exitCode(childErr)
			}
			if !announcedDescendants {
				reporter.report("working", "Waiting for remaining processes")
				fmt.Fprintln(os.Stderr, "kiu: waiting for remaining processes in the job's group")
				announcedDescendants = true
			}
		}
	}
}

func worker(args []string, gate *os.File) int {
	var token [1]byte
	if _, err := io.ReadFull(gate, token[:]); err != nil || token[0] != 1 {
		return 1
	}
	_ = gate.Close()
	// Terminal job control is outside this release's contract. An unexpected
	// terminal read must fail with EIO instead of silently stopping on SIGTTIN.
	// Piped/file stdin is unaffected. Go preserves ignored signals across Exec.
	signal.Ignore(syscall.SIGTTIN, syscall.SIGTTOU)
	executable, err := exec.LookPath(args[0])
	if err == nil {
		// syscall.Exec does not deduplicate environment keys like exec.Cmd does.
		// An inherited empty marker must not shadow the marker we add here.
		environment := os.Environ()
		filtered := environment[:0]
		for _, value := range environment {
			if !strings.HasPrefix(value, "KIU_EXECUTION_CONTEXT=") {
				filtered = append(filtered, value)
			}
		}
		err = syscall.Exec(executable, args, append(filtered, "KIU_EXECUTION_CONTEXT=1"))
	}
	fmt.Fprintf(os.Stderr, "kiu: cannot start %q: %v\n", args[0], err)
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return 127
	}
	return 126
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var child *exec.ExitError
	if errors.As(err, &child) {
		if status, ok := child.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return child.ExitCode()
	}
	return 1
}

func failure(stderr io.Writer, err error) int { fmt.Fprintln(stderr, "kiu:", err); return 1 }

func elapsedSince(start time.Time) time.Duration {
	elapsed := time.Since(start)
	if elapsed < 0 {
		return 0
	}
	return elapsed.Truncate(time.Second)
}
