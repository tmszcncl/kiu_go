package pool

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Task deliberately excludes argv and environment values. IDs and timestamps
// describe a snapshot; they do not imply FIFO admission or process ownership.
type Task struct {
	DetailsAvailable bool       `json:"details_available"`
	SchemaVersion    int        `json:"schema_version"`
	ID               string     `json:"id"`
	State            string     `json:"state"`
	Name             string     `json:"name"`
	Executable       string     `json:"executable"`
	Cwd              string     `json:"cwd"`
	ClientPID        int        `json:"client_pid"`
	SupervisorPID    int        `json:"supervisor_pid"`
	ChildPID         int        `json:"child_pid,omitempty"`
	PGID             int        `json:"pgid,omitempty"`
	SubmittedAt      time.Time  `json:"submitted_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
}

type Outcome string

const (
	OutcomeExited    Outcome = "exited"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeSignalled Outcome = "signalled"
	OutcomeError     Outcome = "error"
)

type Result struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Cwd         string     `json:"cwd"`
	SubmittedAt time.Time  `json:"submitted_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  time.Time  `json:"finished_at"`
	ExitCode    int        `json:"exit_code"`
	Outcome     Outcome    `json:"outcome"`
}

type Snapshot struct {
	SchemaVersion int    `json:"schema_version"`
	Host          string `json:"host"`
	Capacity      int    `json:"capacity"`
	SlotOccupied  bool   `json:"slot_occupied"`
	RunningCount  int    `json:"running_count"`
	WaitingCount  int    `json:"waiting_count"`
	Running       []Task `json:"running"`
	Waiting       []Task `json:"waiting"`
}

func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("--name must not be empty or whitespace")
	}
	if len(name) > 512 {
		return errors.New("--name must be at most 512 bytes")
	}
	if !utf8.ValidString(name) {
		return errors.New("--name must be valid UTF-8")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) {
			return errors.New("--name must contain printable text without terminal controls")
		}
	}
	return nil
}

// DisplayText makes labels and paths safe for terminal output, including data
// read from metadata. Invalid UTF-8 in paths is replaced in JSON/display only;
// the child keeps its actual working directory.
func DisplayText(value string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
}
