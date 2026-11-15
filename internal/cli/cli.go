package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/tmszcncl/kiu_go/internal/process"
)

const help = `Kiu — Keep your tasks in line.

Usage:
  kiu <command> [arguments...]
  kiu run -- <command> [arguments...]
  kiu version
  kiu help

Commands run directly, without an implicit shell. Arguments after the command
belong to that command. Use an explicit shell for pipelines and other shell syntax.

Development status: one shared slot with process-group supervision.
Foreground commands on macOS and Linux only; detached work is outside the contract.
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
	return process.Run(args, stdin, stdout, stderr)
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintln(stderr, "kiu: "+message)
	return 2
}
