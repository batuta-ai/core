//go:build linux || darwin

package loop

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func backgroundSleepFixture(t *testing.T) fixture {
	t.Helper()
	f := setup(t)
	script := strings.Replace(fakeExecutor, "mkdir -p out\ncase", "mkdir -p out\nif [ \"$n\" = 1 ]; then\n  nohup sleep 30 >/dev/null 2>&1 &\n  echo $! > \"$state/sleep-pid\"\nfi\ncase", 1)
	if script == fakeExecutor {
		t.Fatal("fake executor insertion point missing")
	}
	if err := os.WriteFile(f.fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		payload, err := os.ReadFile(filepath.Join(f.state, "sleep-pid"))
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
		if err != nil {
			return
		}
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	})
	return f
}

func sleepPID(t *testing.T, f fixture) int {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(f.state, "sleep-pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func processIsRunning(t *testing.T, pid int) bool {
	t.Helper()
	if unix.Kill(pid, 0) != nil {
		return false
	}
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return false
		}
		state := strings.TrimSpace(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		return state != "" && state[0] != 'Z'
	}
	return true
}

func TestLoopStopsWorktreeProcesses(t *testing.T) {
	t.Parallel()
	f := backgroundSleepFixture(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatal(err)
	}
	root := r.git.Path(r.worktreeName("task_1", 1))
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	pid := sleepPID(t, f)
	if processIsRunning(t, pid) {
		t.Fatalf("sleep PID %d survived worktree removal", pid)
	}
	want := fmt.Sprintf("task_1 stopped pid %d (sleep) in %s with SIG", pid, root)
	trail, err := os.ReadFile(r.trailPath("task_1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), want) || !strings.Contains(string(trail), want) {
		t.Fatalf("missing %q in output or trail\noutput:\n%s\ntrail:\n%s", want, out.String(), trail)
	}
}

func TestLoopKeepWorktreesLeavesProcesses(t *testing.T) {
	t.Parallel()
	f := backgroundSleepFixture(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.KeepWorktrees = true
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	pid := sleepPID(t, f)
	if !processIsRunning(t, pid) {
		t.Fatalf("sleep PID %d stopped with --keep-worktrees\n%s", pid, out.String())
	}
	if strings.Contains(out.String(), fmt.Sprintf("stopped pid %d", pid)) {
		t.Fatalf("reported retained sleep stopped:\n%s", out.String())
	}
}
