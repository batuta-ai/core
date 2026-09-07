package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func (lock presenceLock) write(path string) error {
	data, err := json.Marshal(lock)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return os.Chtimes(path, lock.RefreshedAt, lock.RefreshedAt)
}

func startPresence(ctx context.Context, path string) (func() error, error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("loop: presence host: %w", err)
	}
	now := time.Now().UTC()
	lock := presenceLock{PID: os.Getpid(), Host: host, StartedAt: now, RefreshedAt: now}
	if err := lock.write(path); err != nil {
		cleanupErr := os.Remove(path)
		if os.IsNotExist(cleanupErr) {
			cleanupErr = nil
		}
		return nil, fmt.Errorf("loop: presence lock: %w", errors.Join(err, cleanupErr))
	}
	done := make(chan struct{})
	finished := make(chan error, 1)
	ticker := time.NewTicker(presenceRefresh)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				finished <- nil
				return
			case <-done:
				finished <- nil
				return
			case at := <-ticker.C:
				lock.RefreshedAt = at.UTC()
				if err := lock.write(path); err != nil {
					finished <- fmt.Errorf("loop: refresh presence: %w", err)
					return
				}
			}
		}
	}()
	return func() error {
		close(done)
		// Join before removing so an in-flight heartbeat cannot recreate the lock.
		refreshErr := <-finished
		removeErr := os.Remove(path)
		if os.IsNotExist(removeErr) {
			removeErr = nil
		}
		if removeErr != nil {
			removeErr = fmt.Errorf("loop: remove presence: %w", removeErr)
		}
		return errors.Join(refreshErr, removeErr)
	}, nil
}
