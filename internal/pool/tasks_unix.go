//go:build darwin || linux

package pool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Registration holds a separate ownership lock throughout waiting/execution.
// PID existence alone is never used to decide that a waiting record is live.
type Registration struct {
	Task      Task
	directory string
	owner     *os.File
}

func lockState(ctx context.Context, directory string) (*os.File, error) {
	if err := prepareDirectory(directory); err != nil {
		return nil, err
	}
	file, err := openPrivateFile(filepath.Join(directory, "state.lock"), syscall.O_CREAT|syscall.O_RDWR)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if err := removeTemporaryFiles(directory); err != nil {
				file.Close()
				return nil, err
			}
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func atomicJSON(directory, path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".metadata-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func readJSON(path string, target any) error {
	return readLimitedJSON(path, target, 32768)
}

func readLimitedJSON(path string, target any, limit int) error {
	file, err := openPrivateFile(path, syscall.O_RDONLY)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return err
	}
	if len(data) > limit {
		return fmt.Errorf("%s exceeds metadata size limit", path)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func Register(ctx context.Context, directory, name, executable, cwd string, clientPID int) (*Registration, error) {
	if name == "" {
		name = DisplayText(filepath.Base(cwd) + " — " + filepath.Base(executable))
		if len(name) > 512 {
			name = DisplayText(filepath.Base(executable))
		}
		for len(name) > 512 {
			_, size := utf8.DecodeLastRuneInString(name)
			name = name[:len(name)-size]
		}
	}
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	lock, err := lockState(ctx, directory)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	tasksDir := filepath.Join(directory, "tasks")
	if err := prepareDirectory(tasksDir); err != nil {
		return nil, err
	}
	if err := pruneAbandoned(tasksDir); err != nil {
		return nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(random[:])
	owner, err := openPrivateFile(filepath.Join(tasksDir, id+".lock"), syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		owner.Close()
		return nil, err
	}
	r := &Registration{directory: directory, owner: owner, Task: Task{
		SchemaVersion: 1, DetailsAvailable: true, ID: id, State: "waiting", Name: name, Executable: filepath.Base(executable), Cwd: cwd,
		ClientPID: clientPID, SupervisorPID: os.Getpid(), SubmittedAt: time.Now().UTC(),
	}}
	if err := atomicJSON(tasksDir, filepath.Join(tasksDir, id+".json"), r.Task); err != nil {
		owner.Close()
		os.Remove(filepath.Join(tasksDir, id+".lock"))
		return nil, err
	}
	return r, nil
}

func (r *Registration) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := lockState(ctx, r.directory)
	if err != nil {
		r.owner.Close()
		return err
	}
	defer lock.Close()
	defer r.owner.Close()
	base := filepath.Join(r.directory, "tasks", r.Task.ID)
	if err := os.Remove(base + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(base + ".lock"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// Caller holds state.lock. Task IDs are never reused; an unlocked ownership
// inode can be removed without splitting a live owner's identity. Running crash
// metadata is retained independently inside active.json until its group exits.
func pruneAbandoned(tasksDir string) error {
	if err := removeTemporaryFiles(tasksDir); err != nil {
		return err
	}
	entries, err := os.ReadDir(tasksDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".lock")
		if !validID(id) {
			continue // Foreign files are not part of this metadata protocol.
		}
		owner, err := openPrivateFile(filepath.Join(tasksDir, entry.Name()), syscall.O_RDWR)
		if err != nil {
			return err
		}
		err = syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			err = os.Remove(filepath.Join(tasksDir, id+".json"))
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
			if err == nil {
				err = os.Remove(filepath.Join(tasksDir, entry.Name()))
			}
			owner.Close()
			if err != nil {
				return err
			}
		} else {
			owner.Close()
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				return err
			}
		}
	}
	return nil
}

func validateTask(task Task) error {
	if task.SchemaVersion != 1 || !validID(task.ID) || task.SubmittedAt.IsZero() || !filepath.IsAbs(task.Cwd) || task.ClientPID < 1 || task.SupervisorPID <= 1 {
		return errors.New("invalid task metadata")
	}
	if err := ValidateName(task.Name); err != nil {
		return err
	}
	if task.State != "waiting" && task.State != "running" {
		return errors.New("invalid task state")
	}
	if task.State == "running" && (task.PGID <= 1 || task.StartedAt == nil || task.StartedAt.IsZero()) {
		return errors.New("invalid running task")
	}
	return nil
}

// ReadStatus takes one coordinated snapshot. Work may finish immediately after
// it returns. Display order is submission time, not a promise of FIFO admission.
func ReadStatus(ctx context.Context, directory string) (Snapshot, error) {
	result := Snapshot{SchemaVersion: 1, Capacity: 1, Running: []Task{}, Waiting: []Task{}}
	var err error
	result.Host, err = os.Hostname()
	if err != nil {
		return result, err
	}
	lock, err := lockState(ctx, directory)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	var active reservation
	err = readJSON(filepath.Join(directory, "active.json"), &active)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if err == nil {
		if err := validateReservation(active); err != nil {
			return result, err
		}
		boot, err := bootID()
		if err != nil {
			return result, err
		}
		alive := false
		if boot == active.BootID {
			alive, err = GroupAlive(active.PGID)
		}
		if err != nil {
			return result, err
		}
		if alive {
			if active.Task != nil {
				result.Running = append(result.Running, *active.Task)
			} else {
				result.Running = append(result.Running, Task{SchemaVersion: 1, State: "running", Name: "Job details unavailable (older Kiu)", PGID: active.PGID})
			}
		}
	}
	tasksDir := filepath.Join(directory, "tasks")
	if err := prepareDirectory(tasksDir); err != nil {
		return result, err
	}
	if err := pruneAbandoned(tasksDir); err != nil {
		return result, err
	}
	entries, err := os.ReadDir(tasksDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validID(id) {
			continue // Do not interpret unrelated files as job records.
		}
		// The reservation is authoritative if its second display-file write
		// failed. Do not count the same ID as both running and waiting.
		if len(result.Running) != 0 && result.Running[0].ID == id {
			continue
		}
		var task Task
		if err := readJSON(filepath.Join(tasksDir, entry.Name()), &task); err != nil {
			return result, err
		}
		if task.ID != id {
			return result, errors.New("task ID does not match filename")
		}
		if err := validateTask(task); err != nil {
			return result, err
		}
		owner, err := openPrivateFile(filepath.Join(tasksDir, id+".lock"), syscall.O_RDWR)
		if err != nil {
			return result, err
		}
		err = syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			// No supervisor owns this record. The active reservation, if live,
			// already supplies running metadata independently of this file.
			os.Remove(filepath.Join(tasksDir, id+".json"))
			os.Remove(filepath.Join(tasksDir, id+".lock"))
			owner.Close()
			continue
		}
		owner.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return result, err
		}
		if task.State == "waiting" {
			result.Waiting = append(result.Waiting, task)
		}
	}
	// A pre-metadata Kiu can own the lock without an active reservation yet.
	result.SlotOccupied = len(result.Running) != 0
	if len(result.Running) == 0 {
		slot, err := openPrivateFile(filepath.Join(directory, "slot.lock"), syscall.O_CREAT|syscall.O_RDWR)
		if err != nil {
			return result, err
		}
		err = syscall.Flock(int(slot.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		slot.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			result.SlotOccupied = true
		} else if err != nil {
			return result, err
		}
	}
	sort.Slice(result.Waiting, func(i, j int) bool {
		if result.Waiting[i].SubmittedAt.Equal(result.Waiting[j].SubmittedAt) {
			return result.Waiting[i].ID < result.Waiting[j].ID
		}
		return result.Waiting[i].SubmittedAt.Before(result.Waiting[j].SubmittedAt)
	})
	result.RunningCount, result.WaitingCount = len(result.Running), len(result.Waiting)
	return result, nil
}

// All writers hold state.lock, so temporary files seen after acquiring it have
// been abandoned. Only private regular files from our atomic-write prefix qualify.
func removeTemporaryFiles(directory string) error {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".metadata-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || int(stat.Uid) != os.Geteuid() {
			continue
		}
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
