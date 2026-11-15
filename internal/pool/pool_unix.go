//go:build darwin || linux

// Package pool coordinates commands using local operating-system file locks.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func StateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "kiu", "state"), nil
	}
	if root := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(root) {
		return filepath.Join(root, "kiu"), nil
	}
	return filepath.Join(home, ".local", "state", "kiu"), nil
}

// Acquire holds the single shared slot until the returned function is called.
// Lock files must never be deleted: replacing their inode creates separate pools.
func Acquire(ctx context.Context, directory string, waiting func()) (func(), error) {
	lease, err := AcquireLease(ctx, directory, waiting)
	if err != nil {
		return nil, err
	}
	return lease.Close, nil
}

// Lease owns the slot lock. Its reservation survives a supervisor crash.
type Lease struct {
	file      *os.File
	directory string
}

type reservation struct {
	SchemaVersion int    `json:"schema_version"`
	PGID          int    `json:"pgid"`
	BootID        string `json:"boot_id"`
}

func (l *Lease) Close() { _ = l.file.Close() }

// Clear is only safe after the managed process group has ceased to exist.
// The caller still owns the lock, so no admission can race with this removal.
func (l *Lease) Clear() error {
	err := os.Remove(filepath.Join(l.directory, "active.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Reserve must complete before the gated worker is allowed to execute user code.
func (l *Lease) Reserve(pgid int) error {
	if pgid <= 1 {
		return errors.New("invalid process group")
	}
	boot, err := bootID()
	if err != nil {
		return fmt.Errorf("read boot identity: %w", err)
	}
	if boot == "" {
		return errors.New("empty boot identity")
	}
	data, err := json.Marshal(reservation{SchemaVersion: 1, PGID: pgid, BootID: boot})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(l.directory, ".reservation-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	// Directory fsync is unnecessary for the process-lifetime contract: a host
	// power loss also stops the managed processes. This is not a persistent queue.
	return os.Rename(temporary.Name(), filepath.Join(l.directory, "active.json"))
}

func reservationAlive(directory string) (bool, error) {
	file, err := openPrivateFile(filepath.Join(directory, "active.json"), syscall.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return true, err
	}
	if len(data) > 4096 {
		return true, errors.New("active reservation exceeds 4096 bytes")
	}
	var record reservation
	if err := json.Unmarshal(data, &record); err != nil {
		return true, err
	}
	if record.SchemaVersion != 1 || record.PGID <= 1 || record.BootID == "" {
		return true, errors.New("invalid active reservation; refusing unsafe admission")
	}
	boot, err := bootID()
	if err != nil {
		return true, err
	}
	alive := false
	if record.BootID == boot {
		alive, err = GroupAlive(record.PGID)
	}
	if err != nil || alive {
		return alive, err
	}
	if err := os.Remove(filepath.Join(directory, "active.json")); err != nil {
		return true, err
	}
	return false, nil
}

func AcquireLease(ctx context.Context, directory string, waiting func()) (*Lease, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("state directory %s must be a private directory (mode 0700)", directory)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("state directory %s must belong to the current user", directory)
	}
	file, err := openPrivateFile(filepath.Join(directory, "slot.lock"), syscall.O_CREAT|syscall.O_RDWR)
	if err != nil {
		return nil, err
	}

	announced := false
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			alive, checkErr := reservationAlive(directory)
			if checkErr != nil {
				file.Close()
				return nil, fmt.Errorf("check %s: %w", filepath.Join(directory, "active.json"), checkErr)
			}
			if !alive {
				return &Lease{file: file, directory: directory}, nil
			}
			if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
				file.Close()
				return nil, err
			}
			err = syscall.EWOULDBLOCK
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			file.Close()
			return nil, fmt.Errorf("lock slot: %w", err)
		}
		if !announced && waiting != nil {
			waiting()
			announced = true
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// O_NONBLOCK prevents a corrupt FIFO from hanging admission before validation.
func openPrivateFile(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || int(stat.Uid) != os.Geteuid() {
		file.Close()
		return nil, fmt.Errorf("%s must be a private regular file owned by the current user (mode 0600)", path)
	}
	return file, nil
}
