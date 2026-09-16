package loop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/publication"
)

type supervisionRunSink func(context.Context, SupervisionNotification) error

func (f supervisionRunSink) Notify(ctx context.Context, n SupervisionNotification) error {
	return f(ctx, n)
}

type supervisionRunBackend func(context.Context, executor.Execution) (executor.Result, error)

func (f supervisionRunBackend) Execute(ctx context.Context, e executor.Execution) (executor.Result, error) {
	return f(ctx, e)
}

func supervisionRunReview(t *testing.T, root string, launches *int, verdict string) *SupervisionReviewOptions {
	t.Helper()
	return &SupervisionReviewOptions{Executable: "fake-batuta", Runner: commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		if len(cmd.Args) == 1 && cmd.Args[0] == "capabilities" {
			return publication.CommandResult{Stdout: []byte(`{"commands":["review"]}`)}, nil
		}
		if len(cmd.Args) == 2 && cmd.Args[1] == "-h" {
			return publication.CommandResult{Stderr: []byte(" -base string\n -spec string\n -full\n -out string\n")}, nil
		}
		if err := supervisionGateNoOwner(SupervisionOptions{Workspace: root}); err != nil {
			return publication.CommandResult{}, err
		}
		*launches++
		if err := os.MkdirAll(cmd.Args[7], 0700); err != nil {
			return publication.CommandResult{}, err
		}
		return publication.CommandResult{}, writeSupervisionReviewEvidenceError(cmd, verdict, true)
	})}
}

func TestSupervisionRunObservesBeforeReviewAndResumesArchive(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	launches := 0
	started := make(chan struct{})
	var once sync.Once
	opts.Supervisor = &SuperviseOptions{Interval: 100 * time.Millisecond, Review: supervisionRunReview(t, f.root, &launches, "SHIP"), Sink: supervisionRunSink(func(ctx context.Context, n SupervisionNotification) error {
		if n.Event.Kind == KindStarted {
			once.Do(func() { close(started) })
		}
		return nil
	})}
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	backend := r.backend
	r.backend = supervisionRunBackend(func(ctx context.Context, e executor.Execution) (executor.Result, error) {
		select {
		case <-started:
		case <-ctx.Done():
			return executor.Result{}, ctx.Err()
		}
		return backend.Execute(ctx, e)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := r.Run(ctx)
	if err != nil || state != StateDone || launches != 1 {
		t.Fatalf("run: %s, %v, launches=%d\n%s", state, err, launches, &out)
	}
	observer := SupervisionOptions{Workspace: f.root, Delivery: r.Delivery()}
	normalized, err := normalizeSupervisionOptions(observer)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.root, ".batuta", "runs", "supervision", r.Delivery()+".json"); normalized.CursorPath != want {
		t.Fatalf("default cursor = %s, want %s", normalized.CursorPath, want)
	}
	for path, mode := range map[string]os.FileMode{normalized.CursorPath: 0600, filepath.Dir(normalized.CursorPath): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("private cursor %s: %v, %v", path, info, err)
		}
	}
	before := supervisionObserve(t, normalized)
	opts.Resume = r.Delivery()
	resumed, err := Resume(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	state, err = resumed.Run(ctx)
	after := supervisionObserve(t, normalized)
	if err != nil || state != StateDone || launches != 1 || before.Review.ID != after.Review.ID || before.Cursor != after.Cursor {
		t.Fatalf("resume: %s, %v, launches=%d, before=%+v after=%+v", state, err, launches, before, after)
	}
}

func TestSupervisionRunBoundedStops(t *testing.T) {
	for _, scenario := range []string{"ask", "always-broken", "max-waves"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options(scenario, &out)
			want := StateBlocked
			if scenario == "ask" {
				want = StateWaitingInput
			}
			if scenario == "max-waves" {
				opts.MaxWaves = 1
				want = ""
			}
			launches := 0
			opts.Supervisor = &SuperviseOptions{Review: supervisionRunReview(t, f.root, &launches, "SHIP")}
			r, err := New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			state, err := r.Run(context.Background())
			if scenario == "max-waves" {
				if !errors.Is(err, ErrStopped) {
					t.Fatalf("stop: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if state != want || launches != 0 || r.ownership != nil {
				t.Fatalf("state=%s want=%s launches=%d owner=%v", state, want, launches, r.ownership)
			}
		})
	}
}

func TestSupervisionRunRoadmapReviewsEveryPhase(t *testing.T) {
	for _, verdict := range []string{"SHIP", "FIX_BEFORE_SHIP"} {
		t.Run(verdict, func(t *testing.T) {
			f := setupRoadmap(t)
			opts := f.options("default", new(bytes.Buffer))
			launches := 0
			opts.Supervisor = &SuperviseOptions{Review: supervisionRunReview(t, f.root, &launches, verdict)}
			state, err := RunRoadmap(context.Background(), opts)
			want, count := StateDone, 2
			if verdict != "SHIP" {
				want, count = StateReviewBlocked, 1
			}
			if err != nil || state != want || launches != count {
				t.Fatalf("roadmap: %s, %v launches=%d", state, err, launches)
			}
		})
	}
}
