package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
			once.Do(func() {
				observer := SupervisionOptions{Workspace: f.root, Delivery: n.Event.Delivery}
				records, err := readSupervisionRecords(observer)
				if err != nil || len(records) == 0 || records[0].Kind != KindOpened {
					t.Errorf("observation preceded durable opening: %v", err)
				}
				if err := supervisionGateNoOwner(observer); err == nil {
					t.Error("running observation lost runner ownership")
				}
				close(started)
			})
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

func TestSupervisionRunJoinsCanceledActivity(t *testing.T) {
	for _, scenario := range []string{"cancellation", "observer-error", "joined-observer-error"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			opts := f.options("default", new(bytes.Buffer))
			opts.Parallel = 1
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			started, workerJoined, observerJoined := make(chan struct{}), make(chan struct{}), make(chan struct{})
			observerFailure := errors.New("observer failed")
			launches := 0
			opts.Supervisor = &SuperviseOptions{Review: supervisionRunReview(t, f.root, &launches, "SHIP"), Sleep: func(ctx context.Context, _ time.Duration) error {
				defer close(observerJoined)
				select {
				case <-started:
				case <-ctx.Done():
					return ctx.Err()
				}
				if scenario == "joined-observer-error" {
					return errors.Join(context.Canceled, observerFailure)
				}
				if scenario == "observer-error" {
					return observerFailure
				}
				cancel()
				<-ctx.Done()
				return ctx.Err()
			}}
			r, err := New(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			r.backend = supervisionRunBackend(func(ctx context.Context, _ executor.Execution) (executor.Result, error) {
				defer close(workerJoined)
				close(started)
				<-ctx.Done()
				return executor.Result{}, ctx.Err()
			})
			state, err := r.Run(ctx)
			wantErr := error(context.Canceled)
			if scenario != "cancellation" {
				wantErr = observerFailure
			}
			if !errors.Is(err, wantErr) || state != StateCanceled || r.ownership != nil || launches != 0 {
				t.Fatalf("run: %s, %v, owner=%v, launches=%d", state, err, r.ownership, launches)
			}
			if scenario == "joined-observer-error" && !errors.Is(err, context.Canceled) {
				t.Fatalf("joined cancellation lost: %v", err)
			}
			for _, joined := range []chan struct{}{workerJoined, observerJoined} {
				select {
				case <-joined:
				default:
					t.Fatal("Run returned before activity joined")
				}
			}
			if err := supervisionGateNoOwner(SupervisionOptions{Workspace: f.root}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSupervisionRunAnswerContinuation(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic=%t", automatic), func(t *testing.T) {
			f := setup(t)
			// Isolate the answered task from the fixture's dependent third task.
			planPath := ".batuta/plans/greetings.md"
			plan := testPlan[:strings.Index(testPlan, "- [ ] 2.")] + "\n## Decisions and context\nOne approved task.\n"
			if err := os.WriteFile(filepath.Join(f.root, planPath), []byte(plan), 0600); err != nil {
				t.Fatal(err)
			}
			f.run(t, "add", planPath)
			f.run(t, "commit", "-qm", "test: one waiting task")
			opts := f.options("ask", new(bytes.Buffer))
			launches := 0
			opts.Supervisor = &SuperviseOptions{Interval: 100 * time.Millisecond, Review: supervisionRunReview(t, f.root, &launches, "SHIP")}
			r, err := New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput || launches != 0 {
				t.Fatalf("waiting run: %s, %v, launches=%d", state, err, launches)
			}
			observer := SupervisionOptions{Workspace: f.root, Delivery: r.Delivery()}
			before := supervisionObserve(t, observer)
			counts := kinds(readJournal(t, f, r.Delivery()))
			if automatic {
				plan, err := os.ReadFile(filepath.Join(f.root, planPath))
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range before.Pending {
					if event.Kind == KindQuestion {
						opts.Supervisor.Policy = &SupervisionPolicy{Delivery: r.Delivery(), TaskID: event.TaskID, Execution: event.Execution,
							QuestionID: event.QuestionID, QuestionDigest: event.Evidence.Digest, Action: SupervisionContinueApprovedTask,
							Ownership: "approved_task", MaxAttempts: 2, PlanEvidence: SupervisionEvidence{Path: planPath, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(plan))}}
					}
				}
				if opts.Supervisor.Policy == nil {
					t.Fatal("missing question")
				}
				execution := opts
				execution.Plan = ""
				execution.Transport = &executor.TransportBackend{Mode: "cli"}
				opts.Supervisor.Execution = &execution
			} else if delivery, err := Answer(f.root, "1", "hello there"); err != nil || delivery != r.Delivery() {
				t.Fatalf("answer: %s, %v", delivery, err)
			}
			opts.Resume = r.Delivery()
			resumed, err := Resume(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := resumed.Run(context.Background()); err != nil || state != StateDone || launches != 1 {
				t.Fatalf("answered run: %s, %v, launches=%d", state, err, launches)
			}
			after := supervisionObserve(t, observer)
			got := kinds(readJournal(t, f, r.Delivery()))
			if got[KindAnswer] != 1 || got[KindStarted] != counts[KindStarted]+1 || after.Cursor <= before.Cursor || after.Review == nil || after.Review.Attempts != 1 {
				t.Fatalf("answer duplicated execution or lost cursor: counts=%v cursor=%d -> %d review=%+v", got, before.Cursor, after.Cursor, after.Review)
			}
			resumed, err = Resume(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := resumed.Run(context.Background()); err != nil || state != StateDone || launches != 1 {
				t.Fatalf("repeated resume: %s, %v, launches=%d", state, err, launches)
			}
			if got := kinds(readJournal(t, f, r.Delivery())); got[KindAnswer] != 1 || got[KindStarted] != counts[KindStarted]+1 {
				t.Fatalf("repeated worker/answer: %v", got)
			}
		})
	}
}

func TestSupervisionRunReviewCancellationRemainsUncertain(t *testing.T) {
	f := setup(t)
	opts := f.options("default", new(bytes.Buffer))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launches := 0
	review := supervisionRunReview(t, f.root, &launches, "SHIP")
	original := review.Runner
	joined := make(chan struct{})
	review.Runner = commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		if len(cmd.Args) < 3 {
			return original.Run(ctx, cmd)
		}
		defer close(joined)
		launches++
		cancel()
		<-ctx.Done()
		return publication.CommandResult{}, ctx.Err()
	})
	opts.Supervisor = &SuperviseOptions{Review: review}
	r, err := New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled review: %v", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("review not joined")
	}
	observer := SupervisionOptions{Workspace: f.root, Delivery: r.Delivery()}
	before := supervisionObserve(t, observer)
	if before.Review == nil || before.Review.State != "uncertain" || before.Review.Attempts != 1 {
		t.Fatalf("uncertain review lost: %+v", before.Review)
	}
	opts.Resume = r.Delivery()
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateReviewBlocked || launches != 1 {
		t.Fatalf("uncertain resume: %s, %v, launches=%d", state, err, launches)
	}
	after := supervisionObserve(t, observer)
	if after.Review.ID != before.Review.ID || after.Review.Attempts != 1 || after.Review.State != "uncertain" {
		t.Fatalf("uncertain identity changed: %+v", after.Review)
	}
}

func TestSupervisionRunRoadmapDiscoversArchivedPendingReview(t *testing.T) {
	f := setupRoadmap(t)
	opts := f.options("default", new(bytes.Buffer))
	opts.Supervision = true
	if state, err := RunRoadmap(context.Background(), opts); err != nil || state != StateReviewBlocked {
		t.Fatalf("unreviewed roadmap: %s, %v", state, err)
	}
	launches := 0
	opts.Supervisor = &SuperviseOptions{Review: supervisionRunReview(t, f.root, &launches, "SHIP")}
	if state, err := RunRoadmap(context.Background(), opts); err != nil || state != StateDone || launches != 2 {
		t.Fatalf("recovered roadmap: %s, %v, launches=%d", state, err, launches)
	}
}

func TestSupervisionRunInvalidConfigurationReleasesResume(t *testing.T) {
	for _, scenario := range []string{"delivery", "interval", "cursor"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			opts := f.options("ask", new(bytes.Buffer))
			opts.Supervisor = &SuperviseOptions{}
			r, err := New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
				t.Fatalf("waiting run: %s, %v", state, err)
			}
			before := len(readJournal(t, f, r.Delivery()))
			switch scenario {
			case "delivery":
				opts.Supervisor.Observer.Delivery = "another-delivery"
			case "interval":
				opts.Supervisor.Interval = time.Nanosecond
			case "cursor":
				opts.Supervisor.Observer.CursorPath = filepath.Join(f.root, ".batuta", "journal", "cursor.json")
			}
			opts.Resume = r.Delivery()
			resumed, err := Resume(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := resumed.Run(context.Background()); err == nil || resumed.ownership != nil {
				t.Fatalf("invalid configuration: err=%v owner=%v", err, resumed.ownership)
			}
			if err := supervisionGateNoOwner(SupervisionOptions{Workspace: f.root}); err != nil {
				t.Fatal(err)
			}
			if len(readJournal(t, f, r.Delivery())) != before {
				t.Fatal("invalid configuration started work")
			}
		})
	}
}

func TestSupervisionRunLegacyDeliveryStillRequiresConfiguredReview(t *testing.T) {
	f := setup(t)
	opts := f.options("default", new(bytes.Buffer))
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	launches := 0
	// An existing legacy delivery keeps its opening identity when supervised.
	r.opts.Supervisor = &SuperviseOptions{Review: supervisionRunReview(t, f.root, &launches, "FIX_BEFORE_SHIP")}
	state, err := r.Run(context.Background())
	if err != nil || state != StateReviewBlocked || launches != 1 {
		t.Fatalf("legacy supervised review: %s, %v, launches=%d", state, err, launches)
	}
}
