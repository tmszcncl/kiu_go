//go:build darwin || linux

// Package pool coordinates commands using local operating-system file locks.
package pool

import (
	"context"
	"errors"
	"fmt"
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
	Task          *Task  `json:"task,omitempty"`
}

func (l *Lease) Close() { _ = l.file.Close() }

// Clear is only safe after the managed process group has ceased to exist.
// The caller still owns the lock, so no admission can race with this removal.
func (l *Lease) Clear() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := lockState(ctx, l.directory)
	if err != nil {
		return err
	}
	defer lock.Close()
	err = os.Remove(filepath.Join(l.directory, "active.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Reserve must complete before the gated worker is allowed to execute user code.
func (l *Lease) Reserve(pgid int) error { return l.reserve(pgid, nil) }

func (l *Lease) ReserveTask(pgid int, registration *Registration) error {
	return l.reserve(pgid, registration)
}

func (l *Lease) reserve(pgid int, registration *Registration) error {
	if pgid <= 1 {
		return errors.New("invalid process group")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := lockState(ctx, l.directory)
	if err != nil {
		return err
	}
	defer lock.Close()
	boot, err := bootID()
	if err != nil {
		return fmt.Errorf("read boot identity: %w", err)
	}
	record := reservation{SchemaVersion: 1, PGID: pgid, BootID: boot}
	if registration != nil {
		task := registration.Task
		now := time.Now().UTC()
		task.State, task.PGID, task.ChildPID, task.StartedAt = "running", pgid, pgid, &now
		record.Task = &task
	}
	if err := validateReservation(record); err != nil {
		return err
	}
	// The reservation is committed first. If updating the display record fails,
	// the gated worker still cannot start, and capacity remains fail-closed.
	if err := atomicJSON(l.directory, filepath.Join(l.directory, "active.json"), record); err != nil {
		return err
	}
	if registration != nil {
		if err := atomicJSON(filepath.Join(l.directory, "tasks"), filepath.Join(l.directory, "tasks", registration.Task.ID+".json"), record.Task); err != nil {
			return err
		}
		registration.Task = *record.Task
	}
	return nil
}

func validateReservation(record reservation) error {
	if record.SchemaVersion != 1 || record.PGID <= 1 || record.BootID == "" {
		return errors.New("invalid active reservation; refusing unsafe admission")
	}
	if record.Task != nil {
		if err := validateTask(*record.Task); err != nil {
			return err
		}
		if record.Task.State != "running" || record.Task.PGID != record.PGID {
			return errors.New("inconsistent active task")
		}
	}
	return nil
}

func reservationAlive(directory string) (bool, error) {
	var record reservation
	err := readJSON(filepath.Join(directory, "active.json"), &record)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if err := validateReservation(record); err != nil {
		return true, err
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
	if err := prepareDirectory(directory); err != nil {
		return nil, err
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
			stateLock, checkErr := lockState(ctx, directory)
			if checkErr != nil {
				file.Close()
				return nil, checkErr
			}
			alive, checkErr := reservationAlive(directory)
			stateLock.Close()
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

func prepareDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("state directory %s must be a private directory (mode 0700)", directory)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("state directory %s must belong to the current user", directory)
	}

	return nil
}
