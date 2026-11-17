package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tmszcncl/kiu_go/internal/pool"
	"github.com/tmszcncl/kiu_go/internal/process"
)

func TestMain(m *testing.M) {
	if internal, code := process.Internal(os.Args[1:]); internal {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// The test executable doubles as a child, avoiding dependencies on shell tools.
func TestChild(t *testing.T) {
	if os.Getenv("KIU_TEST_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg != "--" || i+1 >= len(os.Args) {
			continue
		}
		switch os.Args[i+1] {
		case "inspect":
			cwd, _ := os.Getwd()
			input, _ := io.ReadAll(os.Stdin)
			_ = json.NewEncoder(os.Stdout).Encode([]any{os.Args[i+2:], cwd, os.Getenv("KIU_TEST_VALUE"), string(input)})
			fmt.Fprint(os.Stderr, "child stderr")
			os.Exit(0)
		case "exit":
			os.Exit(42)
		case "nested":
			os.Exit(Run([]string{"must-not-launch"}, nil, os.Stdout, os.Stderr, "test"))
		case "status":
			os.Exit(Run([]string{"status", "--json"}, nil, os.Stdout, os.Stderr, "test"))
		}
	}
	os.Exit(99)
}

func TestExecution(t *testing.T) {
	t.Setenv("KIU_EXECUTION_CONTEXT", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("KIU_TEST_CHILD", "1")
	t.Setenv("KIU_TEST_VALUE", "inherited value")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"with spaces", "; echo injected", "$HOME", "--name", "-race", ""}
	args := append([]string{executable, "-test.run=^TestChild$", "--", "inspect"}, wantArgs...)
	var stdout, stderr bytes.Buffer
	if code := Run(args, strings.NewReader("input"), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	var got []json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var childArgs []string
	if err := json.Unmarshal(got[0], &childArgs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(childArgs, wantArgs) {
		t.Fatalf("arguments: %#v", childArgs)
	}
	cwd, _ := os.Getwd()
	for i, want := range []string{cwd, "inherited value", "input"} {
		var value string
		if err := json.Unmarshal(got[i+1], &value); err != nil || value != want {
			t.Fatalf("field %d: %q, want %q (%v)", i, value, want, err)
		}
	}
	if stderr.String() != "child stderr" {
		t.Fatalf("stderr: %q", &stderr)
	}
	if code := Run([]string{"run", "--", executable, "-test.run=^TestChild$", "--", "exit"}, nil, io.Discard, io.Discard, "test"); code != 42 {
		t.Fatalf("child exit: %d", code)
	}
	stderr.Reset()
	if code := Run([]string{executable, "-test.run=^TestChild$", "--", "nested"}, nil, io.Discard, &stderr, "test"); code != 2 || !strings.Contains(stderr.String(), "nested Kiu execution") {
		t.Fatalf("nested execution: exit %d, stderr %s", code, &stderr)
	}
}

func TestCLI(t *testing.T) {
	t.Setenv("KIU_EXECUTION_CONTEXT", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 0}, {[]string{"help"}, 0}, {[]string{"version"}, 0},
		{[]string{"run"}, 2}, {[]string{"run", "--"}, 2},
		{[]string{"--unknown"}, 2}, {[]string{"status"}, 0},
		{[]string{"status", "--json"}, 0}, {[]string{"status", "extra"}, 2},
		{[]string{"--name"}, 2}, {[]string{"--name", ""}, 2},
		{[]string{"--name= "}, 2}, {[]string{"--name", "bad\x1b", "echo"}, 2},
		{[]string{"--name", "job", "status"}, 2}, {[]string{"--name", "job", "--name", "other", "echo"}, 2},
		{[]string{"config"}, 2}, {[]string{"version", "extra"}, 2},
		{[]string{"kiu-definitely-missing-executable-871265"}, 127},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			if code := Run(tc.args, nil, io.Discard, io.Discard, "test"); code != tc.code {
				t.Fatalf("exit %d, want %d", code, tc.code)
			}
		})
	}
	var output bytes.Buffer
	Run([]string{"version"}, nil, &output, io.Discard, "0.0.1")
	if output.String() != "kiu 0.0.1\n" {
		t.Fatal(output.String())
	}
}

func TestCannotExecute(t *testing.T) {
	t.Setenv("KIU_EXECUTION_CONTEXT", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(path, []byte("not a program"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{path}, nil, io.Discard, io.Discard, "test"); code != 126 {
		t.Fatalf("exit %d, want 126", code)
	}
}

func TestNamedFormsKeepChildArgumentsAndQuietFreeSlot(t *testing.T) {
	t.Setenv("KIU_EXECUTION_CONTEXT", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("KIU_TEST_CHILD", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range [][]string{{"--name", "apka cośtam"}, {"--name", "apka cośtam", "run", "--"}, {"--name=apka cośtam"}} {
		args := append(append([]string{}, prefix...), executable, "-test.run=^TestChild$", "--", "inspect", "--name", "child option")
		var stdout, stderr bytes.Buffer
		if code := Run(args, nil, &stdout, &stderr, "test"); code != 0 {
			t.Fatalf("exit %d: %s", code, &stderr)
		}
		if stderr.String() != "child stderr" {
			t.Fatalf("free slot must not add a banner: %q", &stderr)
		}
		var result []json.RawMessage
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		var childArgs []string
		if err := json.Unmarshal(result[0], &childArgs); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(childArgs, []string{"--name", "child option"}) {
			t.Fatalf("child flags changed: %#v", childArgs)
		}
	}
}

func TestEmptyStatusJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"status", "--json"}, nil, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	var result struct {
		SchemaVersion int               `json:"schema_version"`
		Capacity      int               `json:"capacity"`
		Running       []json.RawMessage `json:"running"`
		Waiting       []json.RawMessage `json:"waiting"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.Capacity != 1 || result.Running == nil || result.Waiting == nil || len(result.Running)+len(result.Waiting) != 0 {
		t.Fatalf("invalid empty snapshot: %s", &stdout)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %s", &stderr)
	}
}

func TestNamedExplicitRunExecutesReservedName(t *testing.T) {
	t.Setenv("KIU_EXECUTION_CONTEXT", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("KIU_TEST_CHILD", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Symlink(executable, filepath.Join(directory, "status")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stderr bytes.Buffer
	code := Run([]string{"--name", "named status executable", "run", "--", "status", "-test.run=^TestChild$", "--", "exit"}, nil, io.Discard, &stderr, "test")
	if code != 42 {
		t.Fatalf("reserved child name parsed as Kiu command: exit %d: %s", code, &stderr)
	}
}

func TestWrappedCommandCanQueryNamedStatusJSON(t *testing.T) {
	t.Setenv("KIU_EXECUTION_CONTEXT", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("KIU_TEST_CHILD", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--name", "CLI status fixture", executable, "-test.run=^TestChild$", "--", "status", "private-argument-canary"}, nil, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	var status pool.Snapshot
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.RunningCount != 1 || status.WaitingCount != 0 || status.Running[0].Name != "CLI status fixture" || status.Running[0].StartedAt == nil || status.Running[0].ChildPID <= 1 {
		t.Fatalf("incorrect named CLI snapshot: %s", &stdout)
	}
	if strings.Contains(stdout.String(), "private-argument-canary") || stderr.Len() != 0 {
		t.Fatalf("privacy/output regression: stdout=%s stderr=%s", &stdout, &stderr)
	}
}

func TestStatusOmitsEmptySections(t *testing.T) {
	for _, tc := range []struct {
		name             string
		running, waiting bool
	}{
		{"idle", false, false},
		{"running only", true, false},
		{"waiting only", false, true},
		{"running and waiting", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := pool.Snapshot{Host: "test-host", Capacity: 1}
			if tc.running {
				snapshot.Running = []pool.Task{{Name: "active-test", State: "running"}}
			}
			if tc.waiting {
				snapshot.Waiting = []pool.Task{{Name: "queued-test", State: "waiting"}}
			}
			snapshot.RunningCount = len(snapshot.Running)
			snapshot.WaitingCount = len(snapshot.Waiting)
			snapshot.SlotOccupied = tc.running
			got := formatStatus(snapshot)
			if strings.Contains(got, "\nRUNNING\n") != tc.running || strings.Contains(got, "active-test") != tc.running {
				t.Fatalf("incorrect running section: %s", got)
			}
			if strings.Contains(got, "\nWAITING\n") != tc.waiting || strings.Contains(got, "queued-test") != tc.waiting {
				t.Fatalf("incorrect waiting section: %s", got)
			}
			if strings.Contains(got, "(none)") || !strings.Contains(got, fmt.Sprintf("Running: %d\nWaiting: %d\n", snapshot.RunningCount, snapshot.WaitingCount)) {
				t.Fatalf("incorrect summary: %s", got)
			}
		})
	}
}
