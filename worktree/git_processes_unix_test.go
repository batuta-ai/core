//go:build linux || darwin

package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func worktreeSleep(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.Dir = path
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd
}

func sleepRunning(t *testing.T, cmd *exec.Cmd) bool {
	t.Helper()
	_, _, state, _, err := platformProcessInspector(cmd.Process.Pid)
	return err == nil && state != "Z"
}

func TestProcessMethodsRequireRegisteredWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, _ := initRepo(t)
	path := filepath.Join(RepositoryHome(p.Root), "unregistered")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := worktreeSleep(t, path)
	calls := 0
	p.Processes = providerScanner(func(context.Context, string) ([]Process, error) {
		calls++
		return nil, nil
	})
	if got, err := p.ProcessesIn(ctx, path); err != nil || len(got) != 0 {
		t.Fatalf("ProcessesIn() = %v, %v", got, err)
	}
	if got, err := p.StopInside(ctx, path); err != nil || len(got) != 0 {
		t.Fatalf("StopInside() = %v, %v", got, err)
	}
	_ = p.Remove(ctx, path, "")
	if calls != 0 || !sleepRunning(t, cmd) {
		t.Fatalf("scanner calls = %d, sleep running = %v", calls, sleepRunning(t, cmd))
	}
}

func TestRemoveStopsProcesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, base := initRepo(t)
	path, err := p.Add(ctx, "demo-task-1-e1", "batuta/demo/task-1-e1", base)
	if err != nil {
		t.Fatal(err)
	}
	cmd := worktreeSleep(t, path)
	p.Stopper = providerStopper(func(ctx context.Context, dir string, procs []Process, grace time.Duration) ([]StoppedProcess, error) {
		if dir != path {
			t.Fatalf("stop directory = %q, want %q", dir, path)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("worktree missing before stop: %v", err)
		}
		return StopProcesses(ctx, dir, procs, grace)
	})
	callbacks := 0
	p.OnProcessesStopped = func(gotPath string, stopped []StoppedProcess, err error) {
		callbacks++
		if gotPath != path || err != nil || len(stopped) != 1 || stopped[0].PID != cmd.Process.Pid || stopped[0].Signal == "" {
			t.Fatalf("callback = %q, %v, %v", gotPath, stopped, err)
		}
	}
	if err := p.Remove(ctx, path, "batuta/demo/task-1-e1"); err != nil {
		t.Fatal(err)
	}
	if callbacks != 1 || sleepRunning(t, cmd) {
		t.Fatalf("callbacks = %d, sleep running = %v", callbacks, sleepRunning(t, cmd))
	}
}

func TestRemoveAllowedHomes(t *testing.T) {
	for _, tc := range []struct{ name string }{{"cache"}, {"symlink"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p, base := initRepo(t)
			real := filepath.Join(tempDir(t), "worktrees")
			if tc.name == "cache" {
				isolateCacheDir(t)
				var err error
				real, err = CacheHome(p.Root)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(real, 0o755); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(tempDir(t), "link")
				if err := os.Symlink(real, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				p.Home = link
			}
			if tc.name == "cache" && p.Home != "" {
				t.Fatalf("Home = %q, want empty", p.Home)
			}
			path := addManaged(t, p, base, real, "demo-task-1-e1", "batuta/demo/task-1-e1")
			cmd := worktreeSleep(t, path)
			if err := p.Remove(ctx, path, "batuta/demo/task-1-e1"); err != nil {
				t.Fatal(err)
			}
			if sleepRunning(t, cmd) {
				t.Fatal("sleep remains alive")
			}
		})
	}
}

func TestAddAtReplacementStopsProcesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, base := initRepo(t)
	path, err := p.Add(ctx, "demo-task-1-e1", "batuta/demo/task-1-e1", base)
	if err != nil {
		t.Fatal(err)
	}
	cmd := worktreeSleep(t, path)
	stopped := false
	p.OnProcessesStopped = func(gotPath string, processes []StoppedProcess, err error) {
		if err != nil || gotPath != path || len(processes) != 1 || processes[0].PID != cmd.Process.Pid {
			t.Fatalf("callback = %q, %v, %v", gotPath, processes, err)
		}
		stopped = true
	}
	if _, err := p.AddAt(ctx, path, "batuta/demo/task-1-e1", base, []string{RepositoryHome(p.Root)}); err != nil {
		t.Fatal(err)
	}
	if !stopped || sleepRunning(t, cmd) {
		t.Fatalf("stopped callback = %v, sleep running = %v", stopped, sleepRunning(t, cmd))
	}
	if _, err := os.Stat(filepath.Join(path, "README.md")); err != nil {
		t.Fatalf("replacement worktree missing: %v", err)
	}
}

type providerScanner func(context.Context, string) ([]Process, error)

func (f providerScanner) Inside(ctx context.Context, dir string) ([]Process, error) {
	return f(ctx, dir)
}

type providerStopper func(context.Context, string, []Process, time.Duration) ([]StoppedProcess, error)

func (f providerStopper) Stop(ctx context.Context, dir string, procs []Process, grace time.Duration) ([]StoppedProcess, error) {
	return f(ctx, dir, procs, grace)
}

func TestProcessMethodsRegisteredWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, base := initRepo(t)
	path, err := p.Add(ctx, "demo-task-1-e1", "batuta/demo/task-1-e1", base)
	if err != nil {
		t.Fatal(err)
	}
	proc := Process{PID: 999999, Command: "sleep", Cwd: path, Start: time.Unix(123, 0)}
	p.Processes = providerScanner(func(_ context.Context, dir string) ([]Process, error) {
		if dir != path {
			t.Fatalf("scan directory = %q, want %q", dir, path)
		}
		return []Process{proc}, nil
	})
	stopCalls := 0
	p.Stopper = providerStopper(func(_ context.Context, dir string, procs []Process, grace time.Duration) ([]StoppedProcess, error) {
		stopCalls++
		if dir != path || len(procs) != 1 || procs[0] != proc || grace != 3*time.Second {
			t.Fatalf("stop = %q, %v, %v", dir, procs, grace)
		}
		return []StoppedProcess{{Process: proc, Signal: "TERM"}}, nil
	})
	got, err := p.ProcessesIn(ctx, path)
	if err != nil || len(got) != 1 || got[0] != proc || stopCalls != 0 {
		t.Fatalf("ProcessesIn() = %v, %v; stop calls = %d", got, err, stopCalls)
	}
	stopped, err := p.StopInside(ctx, path)
	if err != nil || len(stopped) != 1 || stopped[0].PID != proc.PID || stopCalls != 1 {
		t.Fatalf("StopInside() = %v, %v; stop calls = %d", stopped, err, stopCalls)
	}
}

func TestRemoveNoProcessesNoCallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, base := initRepo(t)
	path, err := p.Add(ctx, "demo-task-1-e1", "batuta/demo/task-1-e1", base)
	if err != nil {
		t.Fatal(err)
	}
	p.Processes = providerScanner(func(context.Context, string) ([]Process, error) { return nil, nil })
	p.OnProcessesStopped = func(string, []StoppedProcess, error) { t.Fatal("callback with no processes") }
	if err := p.Remove(ctx, path, "batuta/demo/task-1-e1"); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveScansOnlyManagedPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, base := initRepo(t)
	outside := tempDir(t)
	path := addManaged(t, p, base, outside, "demo-task-1-e1", "batuta/demo/task-1-e1")
	calls := 0
	p.Processes = providerScanner(func(context.Context, string) ([]Process, error) {
		calls++
		return nil, nil
	})
	if err := p.Remove(ctx, path, "batuta/demo/task-1-e1"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("scanner called %d times", calls)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

func TestRemoveProcessErrorKeepsRemoval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		partial bool
	}{
		{name: "scan"}, {name: "partial scan", partial: true}, {name: "stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			p, base := initRepo(t)
			path, err := p.Add(ctx, "demo-task-1-e1", "batuta/demo/task-1-e1", base)
			if err != nil {
				t.Fatal(err)
			}
			wantErr := errors.New(tc.name + " error")
			proc := Process{PID: 999999, Command: "sleep", Cwd: path, Start: time.Unix(123, 0)}
			p.Processes = providerScanner(func(context.Context, string) ([]Process, error) {
				if tc.name == "scan" {
					return nil, wantErr
				}
				if tc.partial {
					return []Process{proc}, &PartialScanError{Warning: tc.name}
				}
				return []Process{proc}, nil
			})
			stopCalls := 0
			p.Stopper = providerStopper(func(_ context.Context, _ string, procs []Process, _ time.Duration) ([]StoppedProcess, error) {
				stopCalls++
				if len(procs) != 1 || procs[0] != proc {
					t.Fatalf("stopper processes = %v", procs)
				}
				if tc.name == "stop" {
					return nil, wantErr
				}
				return []StoppedProcess{{Process: proc, Signal: "TERM"}}, nil
			})
			callbacks := 0
			p.OnProcessesStopped = func(gotPath string, stopped []StoppedProcess, gotErr error) {
				callbacks++
				var partial *PartialScanError
				matchesError := errors.Is(gotErr, wantErr)
				if tc.partial {
					matchesError = errors.As(gotErr, &partial)
				}
				if gotPath != path || !matchesError {
					t.Fatalf("callback = %q, %v, %v", gotPath, stopped, gotErr)
				}
			}
			if err := p.Remove(ctx, path, "batuta/demo/task-1-e1"); err != nil {
				t.Fatal(err)
			}
			wantStops := 1
			if tc.name == "scan" {
				wantStops = 0
			}
			if callbacks != 1 || stopCalls != wantStops {
				t.Fatalf("callbacks = %d, stop calls = %d", callbacks, stopCalls)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("worktree still exists: %v", err)
			}
		})
	}
}
