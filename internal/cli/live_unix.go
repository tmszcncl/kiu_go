//go:build darwin || linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
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
		return status(stdout, stderr, false)
	}
	var inputMode, outputMode syscall.Termios
	if terminalMode(input.Fd(), getTerminalMode, &inputMode) != nil ||
		terminalMode(output.Fd(), getTerminalMode, &outputMode) != nil {
		return status(stdout, stderr, false)
	}
	if err := runLiveStatus(input, output, inputMode); err != nil {
		fmt.Fprintln(stderr, "kiu: live status:", err)
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
	go func() {
		inputDone <- readLiveKeys(ctx, int(fd))
		stop()
	}()
	defer func() {
		stop()
		err = errors.Join(err, <-inputDone)
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		snapshot, readErr := pool.ReadStatus(readCtx, directory)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		frame := formatStatus(snapshot)
		if readErr != nil {
			frame = "\n" + pool.DisplayText(readErr.Error()) + "\n"
		}
		if err := writeLiveFrame(output, frame); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	return nil
}

func readLiveKeys(ctx context.Context, fd int) error {
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
		}
	}
	return nil
}

func writeLiveFrame(output io.Writer, frame string) error {
	// Explicit carriage returns also work with terminal output processing disabled.
	frame = strings.ReplaceAll(strings.TrimRight(frame, "\n"), "\n", "\r\n")
	_, err := io.WriteString(output, "\x1b[H\x1b[2Jq / Ctrl+C — quit\r\n"+frame)
	return err
}

func terminalMode(fd uintptr, request uintptr, mode *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(unsafe.Pointer(mode)))
	if errno != 0 {
		return errno
	}
	return nil
}
