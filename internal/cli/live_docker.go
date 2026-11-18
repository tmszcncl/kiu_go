//go:build darwin || linux

package cli

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tmszcncl/kiu_go/internal/pool"
)

type dockerCPUSample struct {
	taskID string
	cpu    float64
	valid  bool
}

// Docker stats needs its own sampling interval. Keep it off the redraw loop.
type dockerCPUMonitor struct {
	pending <-chan dockerCPUSample
	latest  dockerCPUSample
}

func (m *dockerCPUMonitor) sample(ctx context.Context, task pool.Task) (float64, bool) {
	select {
	case result := <-m.pending:
		m.latest = result
		m.pending = nil
	default:
	}
	if m.pending == nil {
		result := make(chan dockerCPUSample, 1)
		m.pending = result
		go func() {
			sampleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			cpu, valid := readDockerCPU(sampleCtx, task)
			result <- dockerCPUSample{task.ID, cpu, valid}
		}()
	}
	return m.latest.cpu, m.latest.valid && m.latest.taskID == task.ID
}

func readDockerCPU(ctx context.Context, task pool.Task) (float64, bool) {
	if task.ChildPID <= 0 || !filepath.IsAbs(task.Cwd) {
		return 0, false
	}
	command, err := exec.CommandContext(ctx, "ps", "-ww", "-p", strconv.Itoa(task.ChildPID), "-o", "args=").Output()
	if err != nil {
		return 0, false
	}
	service := composeExecService(string(command))
	if service == "" {
		return 0, false
	}
	containers, err := exec.CommandContext(ctx, "docker", "ps",
		"--filter", "label=com.docker.compose.project.working_dir="+task.Cwd,
		"--filter", "label=com.docker.compose.service="+service,
		"--format", "{{.ID}}").Output()
	if err != nil {
		return 0, false
	}
	ids := strings.Fields(string(containers))
	if len(ids) != 1 || !dockerContainerID.MatchString(ids[0]) {
		return 0, false
	}
	usage, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.CPUPerc}}", ids[0]).Output()
	if err != nil {
		return 0, false
	}
	value, ok := strings.CutSuffix(strings.TrimSpace(string(usage)), "%")
	if !ok {
		return 0, false
	}
	return parseCPUPercent(value)
}

var (
	composeServiceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	dockerContainerID  = regexp.MustCompile(`^[a-f0-9]{12,64}$`)
)

// ps exposes a flattened command line. Recognize only the default Compose
// invocation and flags that do not affect container selection.
func composeExecService(command string) string {
	args := strings.Fields(command)
	if len(args) < 5 || filepath.Base(args[0]) != "docker" || args[1] != "compose" || args[2] != "exec" {
		return ""
	}
	args = args[3:]
	for len(args) > 0 && (args[0] == "-T" || args[0] == "--no-tty") {
		args = args[1:]
	}
	if len(args) < 2 || !composeServiceName.MatchString(args[0]) {
		return ""
	}
	return args[0]
}
