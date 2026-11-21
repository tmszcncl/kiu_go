//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tmszcncl/kiu_go/internal/pool"
)

func TestLiveStatusCPUColumn(t *testing.T) {
	frame := formatStatusTable(pool.Snapshot{
		Running: []pool.Task{{ID: "12345678", PGID: 42, Name: "Build"}},
		Waiting: []pool.Task{{ID: "98765432", PGID: 42, Name: "Tests"}},
	}, map[int]float64{42: 240})
	for _, line := range []string{
		"ID        STATUS           CPU   NAME",
		"12345678  running unknown  240%  Build",
		"98765432  waiting unknown        Tests",
	} {
		if !strings.Contains(frame, line+"\n") {
			t.Errorf("missing %q in frame:\n%s", line, frame)
		}
	}
}

func TestParseProcessCPUSumsByProcessGroup(t *testing.T) {
	input := "42 99.7\n 42 140.3\n77 0.0\n80 12.5\n42 NaN\n42 +Inf\n42 -1\ninvalid\n"
	want := map[int]float64{42: 240, 77: 0, 80: 12.5}
	if got := parseProcessCPU(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("CPU by group = %v, want %v", got, want)
	}
}

func TestComposeExecService(t *testing.T) {
	for command, want := range map[string]string{
		"docker compose exec -T app bin/ci":                      "app",
		"/usr/local/bin/docker compose exec --no-tty web-1 rake": "web-1",
		"docker compose exec app rake":                           "app",
		"docker compose exec --index 2 app rake":                 "",
		"docker compose -p other exec app rake":                  "",
		"docker --context remote compose exec -T app rake":       "",
		"docker compose exec -T app":                             "",
		"docker compose up":                                      "",
		"docker exec app rake":                                   "",
	} {
		if got := composeExecService(command); got != want {
			t.Errorf("composeExecService(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestLiveQuitKeys(t *testing.T) {
	for name, key := range map[string]byte{"q": 'q', "Ctrl+C": 3} {
		t.Run(name, func(t *testing.T) {
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			if _, err := writer.Write([]byte{'x', key}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := readLiveKeys(ctx, int(input.Fd()), nil); err != nil {
				t.Fatal(err)
			}
			if ctx.Err() != nil {
				t.Fatalf("%s did not close the live view", name)
			}
		})
	}
}

func TestLiveFrameReplacesPreviousScreen(t *testing.T) {
	var output bytes.Buffer
	if err := writeLiveFrame(&output, "Running:  1\nWaiting:  2\n"); err != nil {
		t.Fatal(err)
	}
	want := "\x1b[H\x1b[2J\x1b[?7lq / Ctrl+C — quit\r\nRunning:  1\r\nWaiting:  2\x1b[?7h"
	if output.String() != want {
		t.Fatalf("frame = %q, want %q", &output, want)
	}
}

func TestLiveHistoryFitsAndScrolls(t *testing.T) {
	results := []pool.Result{
		{Name: "Newest", Outcome: pool.OutcomeExited},
		{Name: "Middle", Outcome: pool.OutcomeExited, ExitCode: 42},
		{Name: "Oldest", Outcome: pool.OutcomeCancelled},
	}
	snapshot := pool.Snapshot{Host: "local", Capacity: 1}
	for _, test := range []struct {
		height, offset, wantOffset int
		wantRange, visible, hidden string
	}{
		{4, 0, 0, "1–1 of 3", "Newest", "Oldest"},
		{4, 10, 2, "3–3 of 3", "cancelled  not started  Oldest", "Newest"},
		{5, 1, 1, "2–3 of 3", "failed (exit 42)", "Newest"},
		{8, 2, 0, "History\n", "Newest", ""},
	} {
		frame, offset := formatLiveStatus(snapshot, nil, results, nil, test.height, test.offset)
		if offset != test.wantOffset || strings.Count(frame, "\n") >= test.height ||
			!strings.Contains(frame, test.wantRange) || !strings.Contains(frame, test.visible) ||
			(test.hidden != "" && strings.Contains(frame, test.hidden)) {
			t.Errorf("height %d, offset %d: got offset %d and frame:\n%s", test.height, test.offset, offset, frame)
		}
	}

	snapshot.Running = []pool.Task{{Name: "Active job"}}
	snapshot.Waiting = make([]pool.Task, 20)
	frame, _ := formatLiveStatus(snapshot, nil, results, nil, 8, 0)
	if !strings.Contains(frame, "Active job") || !strings.Contains(frame, "Newest") ||
		!strings.Contains(frame, "more jobs") || strings.Count(frame, "\n") >= 8 {
		t.Fatalf("crowded frame:\n%s", frame)
	}
}

func TestLiveScrollKeys(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	if _, err := writer.WriteString("j\x1b[Ak\x1bOBq"); err != nil {
		t.Fatal(err)
	}
	keys := make(chan int, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := readLiveKeys(ctx, int(input.Fd()), keys); err != nil {
		t.Fatal(err)
	}
	close(keys)
	var got []int
	for key := range keys {
		got = append(got, key)
	}
	if want := []int{1, -1, -1, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scroll keys = %v, want %v", got, want)
	}
}

func TestLiveStatusPreservesQueueLayoutAndHidesEmptySections(t *testing.T) {
	snapshot := pool.Snapshot{
		Host: "local", Capacity: 1, RunningCount: 1,
		Running: []pool.Task{{Name: "Build"}},
	}
	frame, _ := formatLiveStatus(snapshot, map[int]float64{}, nil, nil, 24, 0)
	for _, want := range []string{"Kiu — local\n", "Capacity: 1\nRunning:  1\n\n", "Build"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q in frame:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "Waiting:") || strings.Contains(frame, "History") {
		t.Fatalf("empty section in frame:\n%s", frame)
	}

	snapshot.WaitingCount = 1
	snapshot.Waiting = []pool.Task{{Name: "Queue item"}}
	frame, _ = formatLiveStatus(snapshot, nil, []pool.Result{{Name: "Finished"}}, nil, 24, 0)
	if !strings.Contains(frame, "Waiting:  1\n") || !strings.Contains(frame, "\n\nHistory\n") {
		t.Fatalf("missing waiting count or history in frame:\n%s", frame)
	}
}

func TestLiveHistoryHasSeparateAlignedTable(t *testing.T) {
	snapshot := pool.Snapshot{Host: "local", Capacity: 1, RunningCount: 1,
		Running: []pool.Task{{Name: "Active build"}}}
	results := []pool.Result{
		{Name: "Passed build", Outcome: pool.OutcomeExited},
		{Name: "Failed build", Outcome: pool.OutcomeExited, ExitCode: 42},
	}
	frame, _ := formatLiveStatus(snapshot, nil, results, nil, 24, 0)
	if !strings.Contains(frame, "Active build\n\n\nHistory\nRESULT") {
		t.Fatalf("history should be visually separated:\n%s", frame)
	}
	var starts []int
	for _, line := range strings.Split(frame, "\n") {
		for _, name := range []string{"Passed build", "Failed build"} {
			if column := strings.Index(line, name); column >= 0 {
				starts = append(starts, column)
			}
		}
	}
	if len(starts) != 2 || starts[0] != starts[1] {
		t.Fatalf("history names should align:\n%s", frame)
	}
}
