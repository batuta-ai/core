//go:build linux || darwin

package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingSignaller struct{ calls []string }

type recordingSignal struct{ owner *recordingSignaller }

func (s *recordingSignaller) Open(int) (processSignal, error) {
	return recordingSignal{s}, nil
}

func (s recordingSignal) Signal(name string) error {
	s.owner.calls = append(s.owner.calls, name)
	return nil
}

func (recordingSignal) Close() error { return nil }

func testStopEnv(dir string, signaller processSignaller, inspect processInspector) stopEnv {
	return stopEnv{
		home:      filepath.Join(filepath.Dir(dir), "home"),
		callerPID: 10,
		parentPID: func(pid int) (int, error) { return 1, nil },
		inspect:   inspect,
		signaller: signaller,
	}
}

func TestStopProcessesRecheck(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(123, 0)
	proc := Process{PID: 50, Command: "sleep", Cwd: dir, Start: start}
	for _, tc := range []struct {
		name, command, cwd string
		start              time.Time
	}{
		{"command", "other", dir, start},
		{"working directory", "sleep", filepath.Dir(dir), start},
		{"start time", "sleep", dir, start.Add(time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			signaller := &recordingSignaller{}
			env := testStopEnv(dir, signaller, func(int) (string, string, string, time.Time, error) {
				return tc.command, tc.cwd, "S", tc.start, nil
			})
			got, err := stopWith(context.Background(), env, dir, []Process{proc}, time.Millisecond)
			if err != nil || len(got) != 1 || got[0].Signal != "" || got[0].Reason == "" || len(signaller.calls) != 0 {
				t.Fatalf("stopWith() = %v, %v; signals = %v", got, err, signaller.calls)
			}
		})
	}
}

func TestStopProcessesGuards(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	proc := Process{PID: 50, Command: "sleep", Cwd: dir, Start: time.Unix(123, 0)}
	for _, tc := range []struct {
		name string
		dir  string
		pid  int
		fail bool
	}{
		{"unsafe", "/", 50, false},
		{"caller", dir, 10, false},
		{"ancestor", dir, 20, false},
		{"failed parent lookup", dir, 50, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			signaller := &recordingSignaller{}
			env := testStopEnv(dir, signaller, func(int) (string, string, string, time.Time, error) {
				return proc.Command, proc.Cwd, "S", proc.Start, nil
			})
			env.parentPID = func(pid int) (int, error) {
				if tc.fail {
					return 0, errors.New("lookup failed")
				}
				if pid == 10 {
					return 20, nil
				}
				return 1, nil
			}
			candidate := proc
			candidate.PID = tc.pid
			got, err := stopWith(context.Background(), env, tc.dir, []Process{candidate}, time.Millisecond)
			if tc.name == "unsafe" || tc.fail {
				if err == nil || len(got) != 0 {
					t.Fatalf("stopWith() = %v, %v; want fail closed", got, err)
				}
			} else if err != nil || len(got) != 1 || got[0].Signal != "" || got[0].Reason == "" {
				t.Fatalf("stopWith() = %v, %v; want skipped", got, err)
			}
			if len(signaller.calls) != 0 {
				t.Fatalf("unexpected signals: %v", signaller.calls)
			}
		})
	}
}

func TestStopProcessesUnconfirmed(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(123, 0)
	proc := Process{PID: 50, Command: "sleep", Cwd: dir, Start: start}
	for _, tc := range []struct {
		name, command, cwd, reason string
	}{
		{"command", "other", dir, "process identity changed"},
		{"working directory", "sleep", filepath.Dir(dir), "process left worktree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			signaller := &recordingSignaller{}
			env := testStopEnv(dir, signaller, func(int) (string, string, string, time.Time, error) {
				if len(signaller.calls) == 0 {
					return "sleep", dir, "S", start, nil
				}
				return tc.command, tc.cwd, "S", start, nil
			})
			got, err := stopWith(context.Background(), env, dir, []Process{proc}, time.Second)
			if err != nil || len(got) != 1 || got[0].Signal != "TERM" || got[0].Reason != tc.reason || got[0].Confirmed() {
				t.Fatalf("stopWith() = %v, %v; want unconfirmed TERM with %q", got, err, tc.reason)
			}
			if strings.Join(signaller.calls, ",") != "TERM" {
				t.Fatalf("signals = %v; want TERM only", signaller.calls)
			}
		})
	}
}

func TestStopProcessesStillRunningAfterKill(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(123, 0)
	proc := Process{PID: 50, Command: "sleep", Cwd: dir, Start: start}
	signaller := &recordingSignaller{}
	env := testStopEnv(dir, signaller, func(int) (string, string, string, time.Time, error) {
		return "sleep", dir, "S", start, nil
	})
	got, err := stopWith(context.Background(), env, dir, []Process{proc}, time.Millisecond)
	if err == nil || len(got) != 1 || got[0].Signal != "KILL" || got[0].Confirmed() {
		t.Fatalf("stopWith() = %v, %v; want unconfirmed KILL with an error", got, err)
	}
}

func startStopChild(t *testing.T, dir string, ignored bool) (*exec.Cmd, Process) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	marker := filepath.Join(dir, "ready")
	if ignored {
		cmd = exec.Command("sh", "-c", `trap '' TERM; touch "$1"; exec sleep 30`, "sh", marker)
	}
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Register the reap before any wait that can fail, so the child never outlives the test.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if ignored {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("child did not become ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	procs, err := ProcessesInside(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, proc := range procs {
		if proc.PID == cmd.Process.Pid {
			return cmd, proc
		}
	}
	t.Fatalf("child PID %d absent from %v", cmd.Process.Pid, procs)
	return nil, Process{}
}

func TestStopProcessesTerm(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, proc := startStopChild(t, dir, false)
	got, err := StopProcesses(context.Background(), dir, []Process{proc}, time.Minute)
	if err != nil || len(got) != 1 || got[0].Signal != "TERM" || !got[0].Confirmed() {
		t.Fatalf("StopProcesses() = %v, %v", got, err)
	}
}

func TestStopProcessesEscalates(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, proc := startStopChild(t, dir, true)
	got, err := StopProcesses(context.Background(), dir, []Process{proc}, 50*time.Millisecond)
	if err != nil || len(got) != 1 || got[0].Signal != "KILL" || !got[0].Confirmed() {
		t.Fatalf("StopProcesses() = %v, %v", got, err)
	}
}

func TestStopProcessesZombie(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, proc := startStopChild(t, dir, false)
	got, err := StopProcesses(context.Background(), dir, []Process{proc}, time.Minute)
	if err != nil || len(got) != 1 || got[0].Signal != "TERM" {
		t.Fatalf("StopProcesses() = %v, %v", got, err)
	}
	_, _, state, _, inspectErr := platformProcessInspector(cmd.Process.Pid)
	if inspectErr != nil || !strings.EqualFold(state, "Z") {
		t.Fatalf("unreaped child state = %q, %v; want Z", state, inspectErr)
	}
	_ = cmd.Wait()
}
