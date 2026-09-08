package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	path              string
	file              *os.File
	owner             presenceLock
	takenOver         *presenceLock
	takeoverJournaled bool
	done              chan struct{}
	finished          chan error
	stopOnce          sync.Once
	stopErr           error
}

func liveDeliveryOwner(workspace, delivery string, now time.Time) (*presenceLock, error) {
	path := filepath.Join(workspace, journal.Dir, delivery+".lock")
	lock, _, err := inspectPresence(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loop: inspect presence lock: %w", err)
	}
	if now.Sub(lock.RefreshedAt) > presenceFresh {
		return nil, nil
	}
	return lock, nil
}

func inspectPresence(path string) (*presenceLock, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("presence lock is a symlink")
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errors.New("presence lock is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("presence lock is a symlink")
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return nil, nil, errors.New("presence lock changed while it was inspected")
	}
	payload, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}
	var lock presenceLock
	if err := json.Unmarshal(payload, &lock); err != nil {
		return nil, nil, fmt.Errorf("parse presence lock: %w", err)
	}
	return &lock, opened, nil
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
	var takenOver *presenceLock
	for {
		ownership, err := acquirePresence(ctx, path, now)
		if err == nil {
			ownership.takenOver = takenOver
			if takenOver != nil {
				recorded, recordErr := journalPresenceTakeover(workspace, delivery, now, *takenOver, ownership.owner)
				if recordErr != nil {
					return nil, errors.Join(recordErr, ownership.stop())
				}
				ownership.takeoverJournaled = recorded
			}
			return ownership, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		owner, info, inspectErr := inspectPresence(path)
		if inspectErr != nil {
			return nil, fmt.Errorf("loop: inspect presence lock: %w", inspectErr)
		}
		if now.Sub(owner.RefreshedAt) <= presenceFresh {
			return nil, fmt.Errorf("delivery %s is owned by pid %d since %s\nstop it or wait for waiting_input", delivery, owner.PID, owner.StartedAt.Format(time.RFC3339))
		}
		if err := removeStalePresence(path, *owner, info); err != nil {
			if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("loop: remove stale presence lock: %w", err)
		}
		takenOver = owner
	}
}

type presenceTakeoverDetail struct {
	PreviousPID       int       `json:"previous_pid"`
	PreviousStartedAt time.Time `json:"previous_started_at"`
	PID               int       `json:"pid"`
	StartedAt         time.Time `json:"started_at"`
}

func newPresenceTakeoverDetail(previous, current presenceLock) presenceTakeoverDetail {
	return presenceTakeoverDetail{
		PreviousPID: previous.PID, PreviousStartedAt: previous.StartedAt,
		PID: current.PID, StartedAt: current.StartedAt,
	}
}

func journalPresenceTakeover(workspace, delivery string, now time.Time, previous, current presenceLock) (bool, error) {
	store, err := journal.Open(workspace)
	if err != nil {
		return false, err
	}
	records, err := store.Read(delivery)
	if errors.Is(err, journal.ErrUnknownDelivery) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	detail, err := json.Marshal(newPresenceTakeoverDetail(previous, current))
	if err != nil {
		return false, err
	}
	_, err = store.Append(delivery, journal.Record{
		Kind: KindPresenceTakenOver, Detail: detail, Graph: records[len(records)-1].Graph, At: now,
	})
	return err == nil, err
}

func (r *Runner) recordPendingPresenceTakeover() error {
	if r.ownership == nil || r.ownership.takenOver == nil || r.ownership.takeoverJournaled {
		return nil
	}
	if err := r.record(KindPresenceTakenOver, "", newPresenceTakeoverDetail(*r.ownership.takenOver, r.ownership.owner)); err != nil {
		return err
	}
	r.ownership.takeoverJournaled = true
	return nil
}

func removeStalePresence(path string, expected presenceLock, expectedInfo os.FileInfo) error {
	current, currentInfo, err := inspectPresence(path)
	if err != nil {
		return err
	}
	if !os.SameFile(expectedInfo, currentInfo) || !samePresenceOwner(expected, *current) {
		return os.ErrExist
	}
	return os.Remove(path)
}

func samePresenceOwner(left, right presenceLock) bool {
	return left.PID == right.PID && left.StartedAt.Equal(right.StartedAt)
}

func acquirePresence(ctx context.Context, path string, now time.Time) (*deliveryOwnership, error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("loop: presence host: %w", err)
	}
	now = now.UTC()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	lock := presenceLock{PID: os.Getpid(), Host: host, StartedAt: now, RefreshedAt: now}
	if err := lock.writeFile(file); err != nil {
		return nil, fmt.Errorf("loop: presence lock: %w", errors.Join(err, file.Close(), os.Remove(path)))
	}
	ownership := &deliveryOwnership{path: path, file: file, owner: lock, done: make(chan struct{}), finished: make(chan error, 1)}
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
	return nil
}

func (ownership *deliveryOwnership) ownsPath() (bool, error) {
	ownedInfo, err := ownership.file.Stat()
	if err != nil {
		return false, err
	}
	current, pathInfo, err := inspectPresence(ownership.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(ownedInfo, pathInfo) && samePresenceOwner(ownership.owner, *current), nil
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
