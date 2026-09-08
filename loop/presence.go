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

// presenceTiming shares the runner's clock and cancellable sleep. Standalone
// answer/presence callers use the same timer implementation with wall time.
type presenceTiming struct {
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

type deliveryOwnership struct {
	path              string
	info              os.FileInfo
	mu                sync.Mutex
	owner             presenceLock
	takenOver         *presenceLock
	takeoverJournaled bool
	cancel            context.CancelFunc
	done              <-chan struct{}
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

func guardPresence(path string) (func(), error) {
	file, err := os.OpenFile(path+".guard", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(file); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	// Keep the guard inode: unlinking it would let processes lock different files.
	return func() { unlockFile(file); _ = file.Close() }, nil
}

func inspectPresence(path string) (*presenceLock, os.FileInfo, error) {
	release, err := guardPresence(path)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	return inspectPresenceGuarded(path)
}

func inspectPresenceGuarded(path string) (*presenceLock, os.FileInfo, error) {
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
		return nil, opened, fmt.Errorf("parse presence lock: %w", err)
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

func (lock presenceLock) writeAtomic(path string) (os.FileInfo, error) {
	data, err := json.Marshal(lock)
	if err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	// A previous owner may have crashed while staging a heartbeat.
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	info, statErr := file.Stat()
	if err := errors.Join(writeErr, syncErr, statErr, file.Close()); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return info, nil
}

func acquireDeliveryOwnership(ctx context.Context, workspace, delivery string, now time.Time, timing ...presenceTiming) (*deliveryOwnership, error) {
	path := filepath.Join(workspace, journal.Dir, delivery+".lock")
	ownership, err := takePresence(ctx, path, delivery, now, timing...)
	if err != nil {
		return nil, err
	}
	if ownership.takenOver != nil {
		recorded, err := journalPresenceTakeover(workspace, delivery, now, *ownership.takenOver, ownership.owner)
		if err != nil {
			return nil, errors.Join(err, ownership.stop())
		}
		ownership.takeoverJournaled = recorded
	}
	return ownership, nil
}

func takePresence(ctx context.Context, path, delivery string, now time.Time, timing ...presenceTiming) (*deliveryOwnership, error) {
	release, err := guardPresence(path)
	if err != nil {
		return nil, err
	}
	defer release()
	owner, info, err := inspectPresenceGuarded(path)
	if errors.Is(err, os.ErrNotExist) {
		return acquirePresenceGuarded(ctx, path, now, timing...)
	}
	// Only parse errors return an inode; never recover symlinks or I/O failures.
	if err != nil && (info == nil || now.Sub(info.ModTime()) <= presenceFresh) {
		return nil, fmt.Errorf("loop: inspect presence lock: %w", err)
	}
	if owner != nil && now.Sub(owner.RefreshedAt) <= presenceFresh {
		return nil, fmt.Errorf("delivery %s is owned by pid %d since %s\nstop it or wait for waiting_input", delivery, owner.PID, owner.StartedAt.Format(time.RFC3339))
	}
	if err := removeStalePresence(path, owner, info, now); err != nil {
		return nil, fmt.Errorf("loop: remove stale presence lock: %w", err)
	}
	ownership, err := acquirePresenceGuarded(ctx, path, now, timing...)
	if err != nil {
		return nil, err
	}
	ownership.takenOver = owner
	return ownership, nil
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
	if len(records) == 0 {
		return false, errors.New("loop: cannot take over an empty journal")
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

// removeStalePresence runs with the sibling guard held by takePresence.
func removeStalePresence(path string, expected *presenceLock, expectedInfo os.FileInfo, now time.Time) error {
	current, currentInfo, err := inspectPresenceGuarded(path)
	if err != nil && currentInfo == nil {
		return err
	}
	if !os.SameFile(expectedInfo, currentInfo) {
		return os.ErrExist
	}
	if current == nil {
		if expected != nil || now.Sub(currentInfo.ModTime()) <= presenceFresh {
			return os.ErrExist
		}
	} else if expected == nil || !samePresenceOwner(*expected, *current) || now.Sub(current.RefreshedAt) <= presenceFresh {
		return os.ErrExist
	}
	return os.Remove(path)
}

func samePresenceOwner(left, right presenceLock) bool {
	return left.PID == right.PID && left.StartedAt.Equal(right.StartedAt)
}

func acquirePresence(ctx context.Context, path string, now time.Time, timing ...presenceTiming) (*deliveryOwnership, error) {
	release, err := guardPresence(path)
	if err != nil {
		return nil, err
	}
	defer release()
	return acquirePresenceGuarded(ctx, path, now, timing...)
}

func acquirePresenceGuarded(ctx context.Context, path string, now time.Time, timing ...presenceTiming) (*deliveryOwnership, error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("loop: presence host: %w", err)
	}
	now = now.UTC()
	if _, err := os.Lstat(path); err == nil {
		return nil, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock := presenceLock{PID: os.Getpid(), Host: host, StartedAt: now, RefreshedAt: now}
	info, err := lock.writeAtomic(path)
	if err != nil {
		return nil, fmt.Errorf("loop: presence lock: %w", err)
	}
	clock := presenceTiming{now: func() time.Time { return time.Now().UTC() }, sleep: (&Runner{}).sleep}
	if len(timing) > 0 {
		clock = timing[0]
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	ownership := &deliveryOwnership{path: path, info: info, owner: lock, cancel: cancel, done: heartbeatCtx.Done(), finished: make(chan error, 1)}
	go func() {
		defer cancel()
		for {
			if err := clock.sleep(heartbeatCtx, presenceRefresh); err != nil {
				if heartbeatCtx.Err() != nil {
					err = nil
				}
				ownership.finished <- err
				return
			}
			if heartbeatCtx.Err() != nil {
				ownership.finished <- nil
				return
			}
			lock.RefreshedAt = clock.now().UTC()
			if err := ownership.refresh(lock); err != nil {
				ownership.finished <- fmt.Errorf("loop: refresh presence: %w", err)
				return
			}
		}
	}()
	return ownership, nil
}

func (ownership *deliveryOwnership) refresh(lock presenceLock) error {
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	release, err := guardPresence(ownership.path)
	if err != nil {
		return err
	}
	defer release()
	owned, err := ownership.ownsPath()
	if err != nil || !owned {
		return errors.Join(err, errors.New("ownership was replaced"))
	}
	info, err := lock.writeAtomic(ownership.path)
	if err != nil {
		return err
	}
	ownership.info = info
	return nil
}

// ownsPath runs with the sibling guard and ownership mutex held.
func (ownership *deliveryOwnership) ownsPath() (bool, error) {
	current, pathInfo, err := inspectPresenceGuarded(ownership.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(ownership.info, pathInfo) && samePresenceOwner(ownership.owner, *current), nil
}

func (ownership *deliveryOwnership) stop() error {
	ownership.stopOnce.Do(func() {
		ownership.cancel()
		refreshErr := <-ownership.finished
		ownership.mu.Lock()
		defer ownership.mu.Unlock()
		release, err := guardPresence(ownership.path)
		if err != nil {
			ownership.stopErr = errors.Join(refreshErr, err)
			return
		}
		defer release()
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
		ownership.stopErr = errors.Join(refreshErr, inspectErr, removeErr)
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
