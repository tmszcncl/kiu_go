package process

import (
	"encoding/base64"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"
)

// statusReporter is opt-in: ordinary runs and redirected logs stay unchanged.
// Each report replaces the root record of a dedicated hc terminal, never a
// parent agent's record. Only the executable name is exposed, not command args.
type statusReporter struct {
	output io.Writer
	name   string
}

func (r statusReporter) report(state, message string) {
	if r.output == nil {
		return
	}
	title := "kiu: " + message + " — " + r.name
	// A peek must be useful even when the command itself is quiet or writes its
	// output to a file. OSC reports alone are intentionally invisible in a terminal.
	fmt.Fprintln(r.output, title)
	encoded := base64.StdEncoding.EncodeToString([]byte(title))
	// OSC 2 supplies the visible row title; OSC 7501 supplies the state/message.
	// Waiting is working, not blocked: no user action is required.
	fmt.Fprintf(r.output, "\x1b]2;%s\x1b\\\x1b]7501;state=%s:app=kiu:msg=%s\x1b\\", title, state, encoded)
}

func displayExecutable(executable string) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, filepath.Base(executable))
	runes := []rune(name)
	if len(runes) > 64 {
		name = string(runes[:64])
	}
	return name
}
