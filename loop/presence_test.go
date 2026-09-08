package loop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/core/journal"
)

func TestPresenceLockExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.lock")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	first, err := acquirePresence(context.Background(), path, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.stop() })
	if _, err := acquirePresence(context.Background(), path, now); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second acquire error = %v, want os.ErrExist", err)
	}
}

func TestPresenceOwnerDoesNotTouchReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.lock")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	ownership, err := acquirePresence(context.Background(), path, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement := presenceLock{PID: 99, Host: "replacement", StartedAt: now.Add(time.Second), RefreshedAt: now.Add(time.Second)}
	payload, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	refreshed := ownership.owner
	refreshed.RefreshedAt = now.Add(presenceRefresh)
	if err := ownership.refresh(refreshed); err == nil || !strings.Contains(err.Error(), "ownership was replaced") {
		t.Fatalf("refresh error = %v", err)
	}
	if err := ownership.stop(); err != nil {
		t.Fatal(err)
	}
	if got := readPresenceLock(t, path); got != replacement {
		t.Fatalf("replacement changed: %+v", got)
	}
}

func TestPresenceRefusesLiveOwner(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	writePresenceFixture(t, root, "delivery", now)
	_, err := acquireDeliveryOwnership(context.Background(), root, "delivery", now)
	if err == nil || !strings.Contains(err.Error(), "delivery delivery is owned by pid 1 since 2026-09-08T12:00:00Z") {
		t.Fatalf("acquire error = %v", err)
	}
}

func TestPresenceTakesOverStaleLock(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("delivery", journal.Record{Kind: KindOpened, Detail: json.RawMessage(`{}`), Graph: json.RawMessage(`{}`), At: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	writePresenceFixture(t, root, "delivery", now.Add(-presenceFresh-time.Second))
	ownership, err := acquireDeliveryOwnership(context.Background(), root, "delivery", now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownership.stop() })
	lock := readPresenceLock(t, filepath.Join(root, journal.Dir, "delivery.lock"))
	if lock.PID != os.Getpid() || !lock.StartedAt.Equal(now) {
		t.Fatalf("replacement lock = %+v", lock)
	}
	if ownership.takenOver == nil || ownership.takenOver.PID != 1 {
		t.Fatalf("taken-over owner = %+v", ownership.takenOver)
	}
	records, err := store.Read("delivery")
	if err != nil {
		t.Fatal(err)
	}
	if last := records[len(records)-1]; last.Kind != KindPresenceTakenOver || !last.At.Equal(now) || string(last.Graph) != `{}` {
		t.Fatalf("takeover record = %+v", last)
	}
}

func TestPresenceRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, journal.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "delivery.lock")); err != nil {
		t.Fatal(err)
	}
	_, err := acquireDeliveryOwnership(context.Background(), root, "delivery", time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("acquire error = %v", err)
	}
	payload, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "untouched" {
		t.Fatalf("symlink target changed to %q", payload)
	}
}

func writePresenceFixture(t *testing.T, root, delivery string, at time.Time) string {
	t.Helper()
	path := filepath.Join(root, journal.Dir, delivery+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(presenceLock{PID: 1, Host: "remote", StartedAt: at, RefreshedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
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
		if !refreshed.RefreshedAt.Equal(first.RefreshedAt.Add(5*time.Second)) || !refreshed.StartedAt.Equal(first.StartedAt) {
			t.Fatalf("heartbeat: %+v", refreshed)
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

func TestPresenceHeartbeatIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.lock")
	now := time.Now().UTC()
	ownership, err := acquirePresence(context.Background(), path, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ownership.stop(); err != nil {
			t.Error(err)
		}
	})
	// A reader that opened the old inode must retain a complete old payload.
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	first := readPresenceLock(t, path)
	refreshed := first
	refreshed.RefreshedAt = now.Add(time.Second)
	if err := ownership.refresh(refreshed); err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var old presenceLock
	if err := json.Unmarshal(payload, &old); err != nil {
		t.Fatal(err)
	}
	if old != first {
		t.Fatalf("refresh overwrote an open reader's inode: %+v", old)
	}
	if got := readPresenceLock(t, path); got != refreshed {
		t.Fatalf("new heartbeat = %+v", got)
	}
	// Simulate a crash after truncating the staging file, before rename.
	if err := os.WriteFile(path+".tmp", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _, err := inspectPresence(path); err != nil || *got != refreshed {
		t.Fatalf("interrupted staging write affected lock: %+v, %v", got, err)
	}
	if err := ownership.refresh(refreshed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging file remains: %v", err)
	}
}

func TestPresenceRecoversMalformedStaleLock(t *testing.T) {
	for _, payload := range []string{"", `{"pid":`} {
		for _, stale := range []bool{false, true} {
			t.Run(fmt.Sprintf("payload=%q/stale=%v", payload, stale), func(t *testing.T) {
				root := t.TempDir()
				now := time.Now().UTC()
				at := now
				if stale {
					at = now.Add(-presenceFresh - time.Second)
				}
				path := writePresenceFixture(t, root, "delivery", at)
				if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, at, at); err != nil {
					t.Fatal(err)
				}
				ownership, err := acquireDeliveryOwnership(context.Background(), root, "delivery", now)
				if !stale {
					if err == nil {
						_ = ownership.stop()
						t.Fatal("accepted fresh malformed lock")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := ownership.stop(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestConcurrentStaleTakeoversLeaveOneOwner(t *testing.T) {
	if root := os.Getenv("BATUTA_PRESENCE_TEST_ROOT"); root != "" {
		fmt.Println("ready")
		ownership, err := acquireDeliveryOwnership(context.Background(), root, "delivery", time.Now().UTC())
		if err != nil {
			if !strings.Contains(err.Error(), "owned by pid") {
				t.Fatal(err)
			}
			fmt.Println("refused")
			return
		}
		fmt.Println("owned")
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
		if err := ownership.stop(); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := t.TempDir()
	path := writePresenceFixture(t, root, "delivery", time.Now().UTC().Add(-presenceFresh-time.Second))
	guard, err := os.OpenFile(path+".guard", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := lockExclusive(guard); err != nil {
		t.Fatal(err)
	}
	defer unlockFile(guard)
	type child struct {
		cmd    *exec.Cmd
		output *bufio.Reader
		input  io.WriteCloser
	}
	children := make([]child, 0, 2)
	for range 2 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentStaleTakeoversLeaveOneOwner$")
		cmd.Env = append(os.Environ(), "BATUTA_PRESENCE_TEST_ROOT="+root)
		cmd.Stderr = os.Stderr
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill() })
		reader := bufio.NewReader(output)
		if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
			t.Fatalf("child ready = %q, %v", line, err)
		}
		children = append(children, child{cmd, reader, input})
	}
	// Both processes start with the same stale lock behind the parent's guard.
	unlockFile(guard)
	owners, refused := 0, 0
	for _, child := range children {
		line, err := child.output.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		switch line {
		case "owned\n":
			owners++
		case "refused\n":
			refused++
		default:
			t.Fatalf("child result = %q", line)
		}
	}
	if owners != 1 || refused != 1 {
		t.Fatalf("owners = %d, refused = %d", owners, refused)
	}
	for _, child := range children {
		_ = child.input.Close()
	}
	for _, child := range children {
		if err := child.cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}
