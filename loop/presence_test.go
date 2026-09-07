package loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/core/journal"
)

func writePresenceFixture(t *testing.T, root, delivery string, at time.Time) string {
	t.Helper()
	path := filepath.Join(root, journal.Dir, delivery+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"pid":1,"host":"remote","started_at":"2026-09-07T12:00:00Z","refreshed_at":"2026-09-07T12:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPresenceStates(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	if state, loops := Presence(root, "shown", now); state != "none" || loops != 0 {
		t.Fatalf("missing directory: %s, %d", state, loops)
	}
	writePresenceFixture(t, root, "shown", now.Add(-15*time.Second))
	writePresenceFixture(t, root, "other", now.Add(-5*time.Second))
	writePresenceFixture(t, root, "old", now.Add(-16*time.Second))
	if err := os.WriteFile(filepath.Join(root, journal.Dir, "ignored.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, journal.Dir, "directory.lock"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		delivery, state string
		loops           int
		at              time.Time
	}{
		{"shown", "running", 2, now}, {"old", "stale", 2, now}, {"missing", "none", 2, now},
		{"shown", "stale", 1, now.Add(time.Nanosecond)}, {"other", "stale", 0, now.Add(16 * time.Second)},
	} {
		if state, loops := Presence(root, tc.delivery, tc.at); state != tc.state || loops != tc.loops {
			t.Errorf("%s at %v: %s, %d; want %s, %d", tc.delivery, tc.at, state, loops, tc.state, tc.loops)
		}
	}
}

func readPresenceLock(t *testing.T, path string) presenceLock {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lock presenceLock
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	return lock
}

func TestLoopWritesPresenceLockHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "delivery.lock")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stop, err := startPresence(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		first := readPresenceLock(t, path)
		host, err := os.Hostname()
		if err != nil {
			t.Fatal(err)
		}
		if first.PID != os.Getpid() || first.Host != host || !first.StartedAt.Equal(time.Now()) || !first.RefreshedAt.Equal(first.StartedAt) {
			t.Fatalf("initial lock: %+v", first)
		}
		time.Sleep(4 * time.Second)
		synctest.Wait()
		if got := readPresenceLock(t, path); got != first {
			t.Fatalf("early refresh: %+v", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		refreshed := readPresenceLock(t, path)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !refreshed.RefreshedAt.Equal(first.RefreshedAt.Add(5*time.Second)) || !refreshed.StartedAt.Equal(first.StartedAt) || !info.ModTime().Equal(refreshed.RefreshedAt) {
			t.Fatalf("heartbeat: %+v, mtime %v", refreshed, info.ModTime())
		}
		cancel()
		synctest.Wait()
		time.Sleep(20 * time.Second)
		if got := readPresenceLock(t, path); got != refreshed {
			t.Fatalf("refresh after cancellation: %+v", got)
		}
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Second)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("lock remains: %v", err)
		}
	})
}

func TestLoopRemovesPresenceLockOnEndWithoutCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "delivery.lock")
		stop, err := startPresence(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Second)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("lock recreated after run ended: %v", err)
		}
	})
}
