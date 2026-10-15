package loop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/worktree"
)

type loopProcessScanner func(context.Context, string) ([]worktree.Process, error)

func (f loopProcessScanner) Inside(ctx context.Context, dir string) ([]worktree.Process, error) {
	return f(ctx, dir)
}

type loopProcessStopper func(context.Context, string, []worktree.Process, time.Duration) ([]worktree.StoppedProcess, error)

func (f loopProcessStopper) Stop(ctx context.Context, dir string, procs []worktree.Process, grace time.Duration) ([]worktree.StoppedProcess, error) {
	return f(ctx, dir, procs, grace)
}

func TestLoopSkippedProcessNotReportedStopped(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.Processes = loopProcessScanner(func(_ context.Context, dir string) ([]worktree.Process, error) {
		if filepath.Base(dir) != "greetings-task-1-e1" {
			return nil, nil
		}
		return []worktree.Process{
			{PID: 900001, Command: "sleep", Cwd: dir, Start: time.Unix(1, 0)},
			{PID: 900002, Command: "editor", Cwd: dir, Start: time.Unix(1, 0)},
		}, nil
	})
	opts.Stopper = loopProcessStopper(func(_ context.Context, _ string, procs []worktree.Process, _ time.Duration) ([]worktree.StoppedProcess, error) {
		return []worktree.StoppedProcess{
			{Process: procs[0], Signal: "TERM"},
			{Process: procs[1], Reason: "identity changed"},
		}, nil
	})
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	trail, err := os.ReadFile(r.trailPath("task_1"))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"output": out.String(), "trail": string(trail)} {
		if !strings.Contains(content, "task_1 stopped pid 900001 (sleep)") || !strings.Contains(content, "SIGTERM") {
			t.Errorf("%s missing stopped entry:\n%s", name, content)
		}
		if !strings.Contains(content, "warning:") || !strings.Contains(content, "900002") || !strings.Contains(content, "identity changed") {
			t.Errorf("%s missing skipped warning:\n%s", name, content)
		}
		if strings.Contains(content, "stopped pid 900002") {
			t.Errorf("%s claims skipped PID stopped:\n%s", name, content)
		}
	}
}

func TestLoopProcessScanErrorKeepsRemoval(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.Processes = loopProcessScanner(func(context.Context, string) ([]worktree.Process, error) {
		return nil, errors.New("injected process scan failure")
	})
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	if got := strings.Count(out.String(), "warning: process cleanup: injected process scan failure"); got != 1 {
		t.Fatalf("scan warning count = %d, want 1:\n%s", got, out.String())
	}
	if trees := f.worktrees(t); len(trees) != 0 {
		t.Fatalf("worktrees left after scan error: %v", trees)
	}
}
