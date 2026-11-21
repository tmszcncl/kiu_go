//go:build darwin || linux

package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tmszcncl/kiu_go/internal/pool"
)

func formatLiveStatus(snapshot pool.Snapshot, cpu map[int]float64, results []pool.Result, historyErr error, height, offset int) (string, int) {
	var frame strings.Builder
	if height <= 1 {
		return "", 0
	}
	rows := height - 1 // The controls use one line.
	if rows >= 10 {
		rows -= writeQueueSummary(&frame, snapshot)
	} else {
		fmt.Fprintf(&frame, "Kiu — %s\n", pool.DisplayText(snapshot.Host))
		rows--
	}
	historyRows := 0
	if len(results) > 0 || historyErr != nil {
		historyRows = 2
	}
	tasks := len(snapshot.Running) + len(snapshot.Waiting)
	if tasks > 0 && rows >= historyRows+3 {
		if rows >= historyRows+5 || tasks <= rows-historyRows-3 {
			frame.WriteByte('\n')
			rows--
		}
		capacity := rows - historyRows - 2
		showOmitted := tasks > capacity && capacity > 1
		if showOmitted {
			capacity--
		}
		shown := snapshot
		shown.Running = shown.Running[:min(capacity, len(shown.Running))]
		shown.Waiting = shown.Waiting[:min(capacity-len(shown.Running), len(shown.Waiting))]
		writeTaskTable(&frame, shown, cpu)
		rows -= 2 + len(shown.Running) + len(shown.Waiting)
		if showOmitted {
			fmt.Fprintf(&frame, "… %d more jobs · kiu status\n", tasks-capacity)
			rows--
		}
	} else if tasks == 0 && snapshot.SlotOccupied && rows > historyRows {
		frame.WriteString("Job active; details unavailable.\n")
		rows--
	}
	if historyRows == 0 {
		return frame.String(), 0
	}
	if rows == 0 {
		return frame.String(), 0
	}
	if historyErr != nil {
		fmt.Fprintf(&frame, "History unavailable · %s\n", pool.DisplayText(historyErr.Error()))
		return frame.String(), 0
	}

	columns := rows >= 5 && height >= 12
	if columns {
		frame.WriteString("\n\n")
		rows -= 2
	}
	pageSize := max(0, rows-historyHeaderRows(columns))
	offset = min(max(0, offset), max(0, len(results)-pageSize))
	if pageSize == 0 {
		offset = 0
	}
	frame.WriteString(formatHistory(results, offset, rows, columns))
	return frame.String(), offset
}

func historyHeaderRows(columns bool) int {
	if columns {
		return 2
	}
	return 1
}

func formatHistory(results []pool.Result, offset, rows int, columns bool) string {
	var output strings.Builder
	end := min(len(results), offset+rows-historyHeaderRows(columns))
	switch {
	case len(results) == 0:
		return ""
	case end <= offset:
		fmt.Fprintf(&output, "History — %d\n", len(results))
	default:
		if end-offset == len(results) {
			output.WriteString("History\n")
		} else {
			fmt.Fprintf(&output, "History — %d–%d of %d  ·  ↑/↓ or j/k\n", offset+1, end, len(results))
		}
		table := tabwriter.NewWriter(&output, 0, 4, 2, ' ', 0)
		if columns {
			fmt.Fprintln(table, "RESULT\tDURATION\tNAME")
		}
		for _, result := range results[offset:end] {
			outcome, duration := resultDetails(result)
			fmt.Fprintf(table, "%s\t%s\t%s\n", outcome, duration, pool.DisplayText(result.Name))
		}
		table.Flush()
	}
	return output.String()
}

func resultDetails(result pool.Result) (outcome, duration string) {
	switch result.Outcome {
	case pool.OutcomeExited:
		if result.ExitCode == 0 {
			outcome = "passed"
		} else {
			outcome = fmt.Sprintf("failed (exit %d)", result.ExitCode)
		}
	case pool.OutcomeCancelled:
		outcome = "cancelled"
	case pool.OutcomeSignalled:
		outcome = fmt.Sprintf("terminated (exit %d)", result.ExitCode)
	default:
		outcome = fmt.Sprintf("Kiu error (exit %d)", result.ExitCode)
	}
	duration = "not started"
	if result.StartedAt != nil {
		duration = max(0, result.FinishedAt.Sub(*result.StartedAt)).Round(time.Second).String()
	}
	return outcome, duration
}
