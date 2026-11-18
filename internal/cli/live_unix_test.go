//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

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
