package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tmszcncl/kiu_go/internal/pool"
	"github.com/tmszcncl/kiu_go/internal/process"
)

const help = `Kiu — Keep your tasks in line.

Usage:
  kiu                         Open the live queue and recent results (text outside a terminal)
  kiu <command> [arguments...]
  kiu run -- <command> [arguments...]
  kiu --name <label> <command> [arguments...]
  kiu --name <label> run -- <command> [arguments...]
  kiu status [--json]
  kiu version
  kiu help

Commands run directly, without an implicit shell. Arguments after the command
belong to that command. Use an explicit shell for pipelines and other shell syntax.
Kiu options such as --name must precede the command (or run --).

Development status: one shared slot with process-group supervision.
Noninteractive commands on macOS and Linux only; detached work is outside the contract.
`

// Run executes one CLI invocation and returns its exit status.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	if len(args) == 0 {
		return liveStatus(stdin, stdout, stderr)
	}
	name, named := "", false
	for len(args) > 0 && (args[0] == "--name" || strings.HasPrefix(args[0], "--name=")) {
		if named {
			return usageError(stderr, "--name may only be specified once")
		}
		named = true
		if args[0] == "--name" {
			if len(args) < 2 {
				return usageError(stderr, "--name requires a label")
			}
			name, args = args[1], args[2:]
		} else {
			name, args = strings.TrimPrefix(args[0], "--name="), args[1:]
		}
		if err := pool.ValidateName(name); err != nil {
			return usageError(stderr, err.Error())
		}
	}
	if len(args) == 0 {
		return usageError(stderr, "expected a command after --name")
	}
	if named {
		switch args[0] {
		case "help", "--help", "-h", "version", "--version", "status", "config":
			return usageError(stderr, "--name applies to commands, not administrative subcommands; use run -- to execute a reserved name")
		}
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
	case "status":
		if len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
			return usageError(stderr, "expected: kiu status [--json]")
		}
		return status(stdout, stderr, len(args) == 2)
	case "config":
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
	return process.RunNamed(args, stdin, stdout, stderr, name)
}

func status(stdout, stderr io.Writer, asJSON bool) int {
	directory, err := pool.StateDir()
	if err != nil {
		fmt.Fprintln(stderr, "kiu:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := pool.ReadStatus(ctx, directory)
	if err != nil {
		fmt.Fprintln(stderr, "kiu: status:", err)
		return 1
	}
	if asJSON {
		err = json.NewEncoder(stdout).Encode(snapshot)
	} else {
		_, err = io.WriteString(stdout, formatStatus(snapshot))
	}
	if err != nil {
		fmt.Fprintln(stderr, "kiu: write status:", err)
		return 1
	}
	return 0
}

func formatStatus(snapshot pool.Snapshot) string {
	return formatStatusTable(snapshot, nil)
}

// writeQueueSummary returns the number of lines written for the live view's layout.
func writeQueueSummary(output io.Writer, snapshot pool.Snapshot) int {
	fmt.Fprintf(output, "\nKiu — %s\n────────────────────────────────────────\n\n", pool.DisplayText(snapshot.Host))
	rows := 4
	if snapshot.RunningCount > 0 {
		fmt.Fprintf(output, "Running: %d/%d\n", snapshot.RunningCount, snapshot.Capacity)
		rows++
	}
	if snapshot.WaitingCount > 0 {
		fmt.Fprintf(output, "Waiting: %d\n", snapshot.WaitingCount)
		rows++
	}
	return rows
}

// A non-nil CPU map enables the live view's CPU column.
func formatStatusTable(snapshot pool.Snapshot, cpuByGroup map[int]float64) string {
	var output strings.Builder
	writeQueueSummary(&output, snapshot)
	if snapshot.RunningCount > 0 || snapshot.WaitingCount > 0 {
		output.WriteByte('\n')
	}
	if len(snapshot.Running)+len(snapshot.Waiting) == 0 {
		if snapshot.SlotOccupied {
			output.WriteString("Job active; details unavailable.\n\n")
		}
		return output.String()
	}
	writeTaskTable(&output, snapshot, cpuByGroup)
	output.WriteByte('\n')
	return output.String()
}

func writeTaskTable(output io.Writer, snapshot pool.Snapshot, cpuByGroup map[int]float64) {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if cpuByGroup == nil {
		fmt.Fprintln(table, "ID\tSTATUS\tNAME")
		fmt.Fprintln(table, "────────\t─────────────\t────")
	} else {
		fmt.Fprintln(table, "ID\tSTATUS\tCPU\tNAME")
		fmt.Fprintln(table, "────────\t─────────────\t───\t────")
	}
	for _, group := range []struct {
		state string
		tasks []pool.Task
	}{{"running", snapshot.Running}, {"waiting", snapshot.Waiting}} {
		for _, task := range group.tasks {
			id, elapsed := "-", "unknown"
			if len(task.ID) >= 8 {
				id = task.ID[:8]
			}
			start := task.SubmittedAt
			if group.state == "running" && task.StartedAt != nil {
				start = *task.StartedAt
			}
			if !start.IsZero() {
				duration := time.Since(start)
				if duration < 0 {
					duration = 0
				}
				elapsed = duration.Truncate(time.Second).String()
			}
			fmt.Fprintf(table, "%s\t%s %s\t", id, group.state, elapsed)
			if cpuByGroup != nil {
				if cpu, ok := cpuByGroup[task.PGID]; ok && group.state == "running" {
					fmt.Fprintf(table, "%.0f%%", cpu)
				}
				fmt.Fprint(table, "\t")
			}
			fmt.Fprintln(table, strings.TrimSpace(pool.DisplayText(task.Name)))
		}
	}
	table.Flush()
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintln(stderr, "kiu: "+message)
	return 2
}
