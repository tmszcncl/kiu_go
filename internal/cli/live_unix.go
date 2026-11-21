//go:build darwin || linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/tmszcncl/kiu_go/internal/pool"
)

const (
	enterLiveScreen = "\x1b[?1049h\x1b[?25l"
	leaveLiveScreen = "\x1b[?25h\x1b[?1049l"
)

func liveStatus(stdin io.Reader, stdout, stderr io.Writer) int {
	input, inputOK := stdin.(*os.File)
	output, outputOK := stdout.(*os.File)
	if !inputOK || !outputOK || os.Getenv("TERM") == "dumb" {
		return statusWithHistory(stdout, stderr)
	}
	var inputMode, outputMode syscall.Termios
	if terminalMode(input.Fd(), getTerminalMode, &inputMode) != nil ||
		terminalMode(output.Fd(), getTerminalMode, &outputMode) != nil {
		return statusWithHistory(stdout, stderr)
	}
	if err := runLiveStatus(input, output, inputMode); err != nil {
		fmt.Fprintln(stderr, "kiu: live status:", err)
		return 1
	}
	return 0
}

func statusWithHistory(stdout, stderr io.Writer) int {
	if code := status(stdout, stderr, false); code != 0 {
		return code
	}
	directory, err := pool.StateDir()
	if err != nil {
		fmt.Fprintln(stderr, "kiu: history:", err)
		return 1
	}
	results, err := pool.ReadHistory(directory)
	if err != nil {
		fmt.Fprintln(stderr, "kiu: history:", err)
		return 1
	}
	_, err = io.WriteString(stdout, formatHistory(results, 0, len(results)+2, true))
	if err != nil {
		fmt.Fprintln(stderr, "kiu: write history:", err)
		return 1
	}
	return 0
}

func runLiveStatus(input, output *os.File, original syscall.Termios) (err error) {
	directory, err := pool.StateDir()
	if err != nil {
		return err
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignals()
	ctx, stop := context.WithCancel(signalCtx)
	defer stop()

	fd := input.Fd()
	mode := original
	mode.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ISIG | syscall.IEXTEN
	// Bound each read so cancellation can stop the reader before restoring input.
	mode.Cc[syscall.VMIN] = 0
	mode.Cc[syscall.VTIME] = 1
	if err := terminalMode(fd, setTerminalMode, &mode); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, terminalMode(fd, restoreTerminalMode, &original))
	}()

	defer func() {
		_, restoreErr := io.WriteString(output, leaveLiveScreen)
		err = errors.Join(err, restoreErr)
	}()
	if _, err := io.WriteString(output, enterLiveScreen); err != nil {
		return err
	}

	inputDone := make(chan error, 1)
	scroll := make(chan int, 64)
	go func() {
		inputDone <- readLiveKeys(ctx, int(fd), scroll)
		stop()
	}()
	defer func() {
		stop()
		err = errors.Join(err, <-inputDone)
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var dockerCPU dockerCPUMonitor
	var offset int
	for ctx.Err() == nil {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		snapshot, readErr := pool.ReadStatus(readCtx, directory)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		var frame string
		if readErr != nil {
			frame = "\n" + pool.DisplayText(readErr.Error()) + "\n"
		} else {
			cpuByGroup := make(map[int]float64)
			if len(snapshot.Running) > 0 {
				task := snapshot.Running[0]
				switch {
				case task.Executable == "docker":
					if cpu, ok := dockerCPU.sample(ctx, task); ok {
						cpuByGroup[task.PGID] = cpu
					}
				case task.PGID > 0:
					cpuCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
					cpuByGroup = readProcessCPU(cpuCtx)
					cancel()
				}
			}
			results, historyErr := pool.ReadHistory(directory)
			frame, offset = formatLiveStatus(snapshot, cpuByGroup, results, historyErr, terminalRows(output.Fd()), offset)
		}
		if ctx.Err() != nil {
			return nil
		}
		if err := writeLiveFrame(output, frame); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		case delta := <-scroll:
			offset += delta
		}
	}
	return nil
}

func readProcessCPU(ctx context.Context) map[int]float64 {
	cmd := exec.CommandContext(ctx, "ps", "-ax", "-o", "pgid=", "-o", "pcpu=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	output, err := cmd.Output()
	if err != nil {
		return map[int]float64{}
	}
	return parseProcessCPU(string(output))
}

func parseProcessCPU(output string) map[int]float64 {
	cpuByGroup := make(map[int]float64)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pgid, pgidErr := strconv.Atoi(fields[0])
		cpu, valid := parseCPUPercent(fields[1])
		if pgidErr == nil && valid && pgid > 0 {
			cpuByGroup[pgid] += cpu
		}
	}
	return cpuByGroup
}

func parseCPUPercent(value string) (float64, bool) {
	cpu, err := strconv.ParseFloat(value, 64)
	return cpu, err == nil && cpu >= 0 && !math.IsInf(cpu, 0)
}

func readLiveKeys(ctx context.Context, fd int, scroll chan<- int) error {
	var escape byte
	var keys [64]byte
	for ctx.Err() == nil {
		count, err := syscall.Read(fd, keys[:])
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if err != nil {
			return err
		}
		for _, key := range keys[:count] {
			if key == 'q' || key == 3 {
				return nil
			}
			delta := 0
			switch {
			case key == 27:
				escape = key
				continue
			case escape == 27 && (key == '[' || key == 'O'):
				escape = key
				continue
			case escape == '[' || escape == 'O':
				switch key {
				case 'A':
					delta = -1
				case 'B':
					delta = 1
				}
			case escape == 0 && key == 'j':
				delta = 1
			case escape == 0 && key == 'k':
				delta = -1
			}
			escape = 0
			if delta != 0 {
				select {
				case scroll <- delta:
				case <-ctx.Done():
					return nil
				}
			}
		}
	}
	return nil
}

func writeLiveFrame(output io.Writer, frame string) error {
	// Explicit carriage returns also work with terminal output processing disabled.
	frame = strings.ReplaceAll(strings.TrimRight(frame, "\n"), "\n", "\r\n")
	_, err := io.WriteString(output, "\x1b[H\x1b[2J\x1b[?7lq / Ctrl+C — quit\r\n"+frame+"\x1b[?7h")
	return err
}

func terminalMode(fd uintptr, request uintptr, mode *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(unsafe.Pointer(mode)))
	if errno != 0 {
		return errno
	}
	return nil
}

func terminalRows(fd uintptr) int {
	var size struct{ rows, cols, x, y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
	if errno != 0 || size.rows == 0 {
		return 24
	}
	return int(size.rows)
}
