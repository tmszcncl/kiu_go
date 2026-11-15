package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/tmszcncl/kiu_go/internal/pool"
)

const help = `Kiu — Keep your tasks in line.

Usage:
  kiu <command> [arguments...]
  kiu run -- <command> [arguments...]
  kiu version
  kiu help

Commands run directly, without an implicit shell. Arguments after the command
belong to that command. Use an explicit shell for pipelines and other shell syntax.

Development status: one shared slot, with automatic waiting. Active cancellation
and crash-safe descendant handling are unfinished; this is a prototype.
`

// Run executes one CLI invocation and returns its exit status.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, help)
		return 0
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) != 1 {
			return usageError(stderr, "help takes no arguments")
		}
		fmt.Fprint(stdout, help)
		return 0
	case "version", "--version":
		if len(args) != 1 {
			return usageError(stderr, "version takes no arguments")
		}
		fmt.Fprintf(stdout, "kiu %s\n", version)
		return 0
	case "config", "status":
		return usageError(stderr, args[0]+" is not implemented yet; use 'kiu run -- "+args[0]+"' to run an executable with this name")
	case "run":
		if len(args) < 3 || args[1] != "--" {
			return usageError(stderr, "expected: kiu run -- <command> [arguments...]")
		}
		args = args[2:]
	default:
		if len(args[0]) > 0 && args[0][0] == '-' {
			return usageError(stderr, "unknown option: "+args[0])
		}
	}
	if os.Getenv("KIU_EXECUTION_CONTEXT") != "" {
		return usageError(stderr, "nested Kiu execution would deadlock; run the inner command without kiu")
	}
	directory, err := pool.StateDir()
	if err != nil {
		fmt.Fprintln(stderr, "kiu:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	release, err := pool.Acquire(ctx, directory, func() {
		fmt.Fprintln(stderr, "kiu: waiting for a slot (capacity: 1)")
	})
	cancelled := ctx.Err() != nil
	stop()
	if err != nil || cancelled {
		if release != nil {
			release()
		}
		if cancelled {
			return 130
		}
		fmt.Fprintln(stderr, "kiu: acquire slot:", err)
		return 1
	}
	defer release()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "KIU_EXECUTION_CONTEXT=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return childExitCode(exitErr)
		}
		fmt.Fprintf(stderr, "kiu: cannot start %q: %v\n", args[0], err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	return 0
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintln(stderr, "kiu: "+message)
	return 2
}
