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
)

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
		}
	}
	os.Exit(99)
}

func TestExecution(t *testing.T) {
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
}

func TestCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 0}, {[]string{"help"}, 0}, {[]string{"version"}, 0},
		{[]string{"run"}, 2}, {[]string{"run", "--"}, 2},
		{[]string{"--unknown"}, 2}, {[]string{"status"}, 2},
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
