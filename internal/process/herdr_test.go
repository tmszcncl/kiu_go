package process

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestWaitingStatusDoesNotRequestUserAction(t *testing.T) {
	var output bytes.Buffer
	r := statusReporter{output: &output, name: "go"}
	r.report("working", "Waiting for slot")
	report := output.String()
	if !strings.HasPrefix(report, "Kiu: Waiting for slot — go\n") {
		t.Fatalf("peek must show a readable status: %q", report)
	}
	if !strings.Contains(report, "7501;state=working:app=kiu:msg=") || strings.Contains(report, "blocked") {
		t.Fatalf("waiting must remain working: %q", report)
	}
	encoded := strings.Split(strings.Split(report, ":msg=")[1], "\x1b")[0]
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != "Kiu: Waiting for slot — go" {
		t.Fatalf("status message: %q (%v)", decoded, err)
	}
}

func TestExecutableDisplayCannotInjectTerminalControls(t *testing.T) {
	name := displayExecutable("/tmp/evil\x1b\x07\n\r\u202e-command")
	if strings.ContainsAny(name, "\x1b\x07\n\r\u202e") {
		t.Fatalf("unsafe name: %q", name)
	}
	if got := displayExecutable(strings.Repeat("界", 100)); len([]rune(got)) != 64 {
		t.Fatalf("unbounded label: %q", got)
	}
}

func TestDisabledReporterIsSilent(t *testing.T) {
	r := statusReporter{name: "go"}
	r.report("working", "Waiting for slot")
	r.report("done", "Finished")
}

func TestNamedHerdrWaitExplainsOccupantWithoutBlocking(t *testing.T) {
	var output bytes.Buffer
	r := statusReporter{output: &output, name: "Calculyo — system tests"}
	r.report("working", "Waiting for slot; running: Tidudanka — full CI")
	got := output.String()
	if !strings.Contains(got, "Calculyo — system tests") || !strings.Contains(got, "Tidudanka — full CI") || !strings.Contains(got, "state=working:app=kiu") || strings.Contains(got, "state=blocked") {
		t.Fatalf("incorrect named wait status: %q", got)
	}
}
