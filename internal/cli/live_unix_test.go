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
			if err := readLiveKeys(ctx, int(input.Fd())); err != nil {
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
	want := "\x1b[H\x1b[2Jq / Ctrl+C — quit\r\nRunning:  1\r\nWaiting:  2"
	if output.String() != want {
		t.Fatalf("frame = %q, want %q", &output, want)
	}
}
