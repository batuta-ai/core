package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/batuta-ai/core/journal"
)

const presenceRefresh = 5 * time.Second
const presenceFresh = 15 * time.Second

type presenceLock struct {
	PID         int       `json:"pid"`
	Host        string    `json:"host"`
	StartedAt   time.Time `json:"started_at"`
	RefreshedAt time.Time `json:"refreshed_at"`
}

type deliveryOwnership struct {
	path     string
	file     *os.File
	done     chan struct{}
	finished chan error
	stopOnce sync.Once
	stopErr  error
}

func liveDeliveryOwner(workspace, delivery string, now time.Time) (*presenceLock, error) {
	path := filepath.Join(workspace, journal.Dir, delivery+".lock")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loop: inspect presence lock: %w", err)
	}
	if now.Sub(info.ModTime()) > presenceFresh {
		return nil, nil
	}
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loop: read presence lock: %w", err)
	}
	var lock presenceLock
	if err := json.Unmarshal(payload, &lock); err != nil {
		return nil, fmt.Errorf("loop: parse presence lock: %w", err)
	}
	return &lock, nil
}

// Presence observes lock freshness only; it never probes processes or hosts.
func Presence(workspace, delivery string, now time.Time) (state string, loops int) {
	state = "none"
	for name, stamp := range presenceFiles(workspace) {
		fresh := now.Sub(stamp.modTime) <= presenceFresh
		if fresh {
			loops++
		}
		if name == delivery+".lock" {
			state = "stale"
			if fresh {
				state = "running"
			}
		}
	}
	return state, loops
}

func presenceFiles(workspace string) map[string]watchFileStamp {
	locks := make(map[string]watchFileStamp)
	entries, err := os.ReadDir(filepath.Join(workspace, journal.Dir))
	if err != nil {
		return locks
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lock") || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			// A loop may remove its lock during the directory scan.
			continue
		}
		locks[entry.Name()] = watchFileStamp{size: info.Size(), modTime: info.ModTime()}
	}
	return locks
}

func (lock presenceLock) writeFile(file *os.File) error {
	data, err := json.Marshal(lock)
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return nil
}

func acquireDeliveryOwnership(ctx context.Context, workspace, delivery string, now time.Time) (*deliveryOwnership, error) {
	path := filepath.Join(workspace, journal.Dir, delivery+".lock")
	for {
		ownership, err := acquirePresence(ctx, path, now)
		if err == nil {
			return ownership, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		owner, inspectErr := liveDeliveryOwner(workspace, delivery, now)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if owner != nil {
			return nil, fmt.Errorf("delivery %s is owned by pid %d since %s\nstop it or wait for waiting_input", delivery, owner.PID, owner.StartedAt.Format(time.RFC3339))
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("loop: remove stale presence lock: %w", err)
		}
	}
}

func acquirePresence(ctx context.Context, path string, now time.Time) (*deliveryOwnership, error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("loop: presence host: %w", err)
	}
	now = now.UTC()
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return nil, err
	}
	temporary := file.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	lock := presenceLock{PID: os.Getpid(), Host: host, StartedAt: now, RefreshedAt: now}
	if err := lock.writeFile(file); err != nil {
		return nil, fmt.Errorf("loop: presence lock: %w", errors.Join(err, file.Close()))
	}
	if err := os.Chtimes(temporary, now, now); err != nil {
		return nil, fmt.Errorf("loop: presence timestamp: %w", errors.Join(err, file.Close()))
	}
	// A hard link publishes the complete lock without an observable empty-file window.
	if err := os.Link(temporary, path); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := os.Remove(temporary); err != nil {
		return nil, fmt.Errorf("loop: publish presence lock: %w", errors.Join(err, file.Close(), os.Remove(path)))
	}
	removeTemporary = false
	ownership := &deliveryOwnership{path: path, file: file, done: make(chan struct{}), finished: make(chan error, 1)}
	ticker := time.NewTicker(presenceRefresh)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				ownership.finished <- nil
				return
			case <-ownership.done:
				ownership.finished <- nil
				return
			case at := <-ticker.C:
				lock.RefreshedAt = at.UTC()
				if err := ownership.refresh(lock); err != nil {
					ownership.finished <- fmt.Errorf("loop: refresh presence: %w", err)
					return
				}
			}
		}
	}()
	return ownership, nil
}

func (ownership *deliveryOwnership) refresh(lock presenceLock) error {
	owned, err := ownership.ownsPath()
	if err != nil || !owned {
		return errors.Join(err, errors.New("ownership was replaced"))
	}
	if err := lock.writeFile(ownership.file); err != nil {
		return err
	}
	return os.Chtimes(ownership.path, lock.RefreshedAt, lock.RefreshedAt)
}

func (ownership *deliveryOwnership) ownsPath() (bool, error) {
	ownedInfo, err := ownership.file.Stat()
	if err != nil {
		return false, err
	}
	pathInfo, err := os.Stat(ownership.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(ownedInfo, pathInfo), nil
}

func (ownership *deliveryOwnership) stop() error {
	ownership.stopOnce.Do(func() {
		close(ownership.done)
		refreshErr := <-ownership.finished
		owned, inspectErr := ownership.ownsPath()
		var removeErr error
		if owned {
			removeErr = os.Remove(ownership.path)
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
		}
		if removeErr != nil {
			removeErr = fmt.Errorf("loop: remove presence: %w", removeErr)
		}
		ownership.stopErr = errors.Join(refreshErr, inspectErr, removeErr, ownership.file.Close())
	})
	return ownership.stopErr
}

func startPresence(ctx context.Context, path string) (func() error, error) {
	ownership, err := acquirePresence(ctx, path, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return ownership.stop, nil
}
