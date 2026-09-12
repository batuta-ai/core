package loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

func supervisionReviewFixture(t *testing.T) (SupervisionOptions, *journal.Store, string) {
	t.Helper()
	store, opts := supervisionFixture(t)
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = opts.Workspace
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s, %v", args, b, e)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.test")
	git("config", "commit.gpgsign", "false")
	spec := "# Plan — Delivery\n\n**Goal:** Test.\n**Status:** approved\n\n## Tasks\n- [ ] 1. Add source — backend/low\n      Scope: source.txt\n      Accept: source exists → test -f source.txt\n"
	path := ".batuta/plans/delivery.md"
	if e := os.MkdirAll(filepath.Join(opts.Workspace, ".batuta/plans"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(opts.Workspace, path), []byte(spec), 0600); e != nil {
		t.Fatal(e)
	}
	git("add", path)
	git("commit", "-qm", "plan")
	base := git("rev-parse", "HEAD")
	plan, e := routing.ParsePlan("delivery", []byte(spec))
	if e != nil {
		t.Fatal(e)
	}
	opened, _ := json.Marshal(openedDetail{Slug: "delivery", PlanPath: path, PlanDigest: plan.Set.Digest, Head: base})
	supervisionAppend(t, store, opts, KindOpened, string(opened))
	if e := os.WriteFile(filepath.Join(opts.Workspace, "source.txt"), []byte("delivered\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Join(opts.Workspace, ".batuta/plans/done"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(filepath.Join(opts.Workspace, path), filepath.Join(opts.Workspace, ".batuta/plans/done/delivery.md")); e != nil {
		t.Fatal(e)
	}
	git("add", "source.txt", ".batuta/plans")
	git("commit", "-qm", "delivery")
	final := git("rev-parse", "HEAD")
	detail, _ := json.Marshal(terminalDetail{State: StateDone, FinalCommit: final})
	supervisionAppend(t, store, opts, KindTerminal, string(detail))
	return opts, store, spec
}

func fakeSupervisionReview(t *testing.T, opts SupervisionOptions, spec string, launches *int) SupervisionReviewOptions {
	t.Helper()
	records, err := readSupervisionRecords(opts)
	if err != nil {
		t.Fatal(err)
	}
	base := supervisionReviewCandidate(opts.Delivery, records).Base
	return SupervisionReviewOptions{Executable: "fake-batuta", Runner: commandRunnerFunc(func(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
		if len(c.Args) == 1 && c.Args[0] == "capabilities" {
			return publication.CommandResult{Stdout: []byte(`{"commands":["review"]}`)}, nil
		}
		if len(c.Args) == 2 && c.Args[1] == "-h" {
			return publication.CommandResult{Stderr: []byte("  -base string\n  -spec string\n  -full\n  -out string\n")}, nil
		}
		(*launches)++
		if c.Directory == opts.Workspace || len(c.Args) != 8 || c.Args[0] != "review" || c.Args[2] != base || c.Args[5] != "--full" {
			t.Fatalf("review command: %+v", c)
		}
		data, e := os.ReadFile(c.Args[4])
		if e != nil || string(data) != spec {
			t.Fatalf("spec: %q, %v", data, e)
		}
		data, e = os.ReadFile(filepath.Join(c.Directory, "source.txt"))
		if e != nil || string(data) != "delivered\n" {
			t.Fatalf("snapshot: %q, %v", data, e)
		}
		if e := os.MkdirAll(c.Args[7], 0700); e != nil {
			t.Fatal(e)
		}
		for _, name := range []string{"manifest.json", "findings.json", "review.md"} {
			if e := os.WriteFile(filepath.Join(c.Args[7], name), []byte("evidence"), 0600); e != nil {
				t.Fatal(e)
			}
		}
		return publication.CommandResult{}, nil
	})}
}

func TestSupervisionReviewDeduplicatesAcrossCursors(t *testing.T) {
	opts, _, spec := supervisionReviewFixture(t)
	observation := supervisionObserve(t, opts)
	if observation.Review == nil || observation.Review.ID == "" {
		t.Fatalf("no review: %+v", observation)
	}
	launches := 0
	engine := fakeSupervisionReview(t, opts, spec, &launches)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, e := RunSupervisionReview(context.Background(), opts, engine)
			if e != nil || job.State != "reported" {
				t.Errorf("job: %+v, %v", job, e)
			}
		}()
	}
	wg.Wait()
	opts.CursorPath = filepath.Join(opts.Workspace, "another-cursor.json")
	job, e := RunSupervisionReview(context.Background(), opts, engine)
	if e != nil || job.ID != observation.Review.ID || launches != 1 {
		t.Fatalf("restart: %+v, launches=%d, %v", job, launches, e)
	}
}

func TestSupervisionReviewUncertainLaunchDoesNotRelaunch(t *testing.T) {
	opts, _, spec := supervisionReviewFixture(t)
	candidate := supervisionObserve(t, opts).Review
	dir := supervisionReviewDirectory(opts, *candidate)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	candidate.State = "launching"
	if e := writeSupervisionJSON(filepath.Join(dir, "job.json"), candidate); e != nil {
		t.Fatal(e)
	}
	launches := 0
	for i := 0; i < 2; i++ {
		job, e := RunSupervisionReview(context.Background(), opts, fakeSupervisionReview(t, opts, spec, &launches))
		if e != nil || job.State != "uncertain" {
			t.Fatalf("job: %+v, %v", job, e)
		}
	}
	if launches != 0 {
		t.Fatal("re-launched uncertain process")
	}
}

func TestSupervisionReviewSnapshotAndEngineFailures(t *testing.T) {
	for _, scenario := range []string{"later source", "snapshot mutation", "active runner", "runner during review", "missing engine", "missing flag", "missing snapshot", "wrong digest", "canceled", "missing artifacts"} {
		t.Run(scenario, func(t *testing.T) {
			opts, store, spec := supervisionReviewFixture(t)
			launches := 0
			engine := fakeSupervisionReview(t, opts, spec, &launches)
			original := engine.Runner
			want := "reported"
			switch scenario {
			case "later source":
				c := exec.Command("git", "commit", "--allow-empty", "-qm", "later")
				c.Dir = opts.Workspace
				if b, e := c.CombinedOutput(); e != nil {
					t.Fatalf("%s: %v", b, e)
				}
				if e := os.WriteFile(filepath.Join(opts.Workspace, "source.txt"), []byte("later\n"), 0600); e != nil {
					t.Fatal(e)
				}
			case "active runner":
				if _, e := (presenceLock{RefreshedAt: opts.Now()}).writeAtomic(filepath.Join(opts.Workspace, journal.Dir, "other.lock")); e != nil {
					t.Fatal(e)
				}
				want = "pending"
			case "missing snapshot", "wrong digest":
				records, e := store.Read(opts.Delivery)
				if e != nil {
					t.Fatal(e)
				}
				var opened openedDetail
				_ = json.Unmarshal(records[0].Detail, &opened)
				var terminal terminalDetail
				_ = json.Unmarshal(records[1].Detail, &terminal)
				if scenario == "missing snapshot" {
					terminal.FinalCommit = strings.Repeat("a", 40)
				} else {
					opened.PlanDigest = strings.Repeat("b", 64)
				}
				if e := os.Remove(store.Path(opts.Delivery)); e != nil {
					t.Fatal(e)
				}
				data, _ := json.Marshal(opened)
				supervisionAppend(t, store, opts, KindOpened, string(data))
				data, _ = json.Marshal(terminal)
				supervisionAppend(t, store, opts, KindTerminal, string(data))
				want = "failed"
			default:
				engine.Runner = commandRunnerFunc(func(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
					if scenario == "missing engine" && c.Args[0] == "capabilities" {
						return publication.CommandResult{Stdout: []byte(`{"commands":[]}`)}, nil
					}
					if scenario == "missing flag" && len(c.Args) == 2 {
						return publication.CommandResult{Stderr: []byte("-base -spec -out")}, nil
					}
					if len(c.Args) > 2 {
						if scenario == "missing artifacts" {
							return publication.CommandResult{}, nil
						}
						result, e := original.Run(ctx, c)
						if scenario == "snapshot mutation" {
							if e := os.WriteFile(filepath.Join(c.Directory, "source.txt"), []byte("changed"), 0600); e != nil {
								t.Fatal(e)
							}
						}
						if scenario == "runner during review" {
							if _, e := (presenceLock{RefreshedAt: opts.Now()}).writeAtomic(filepath.Join(opts.Workspace, journal.Dir, "other.lock")); e != nil {
								t.Fatal(e)
							}
						}
						if scenario == "canceled" {
							return result, context.Canceled
						}
						return result, e
					}
					return original.Run(ctx, c)
				})
				want = "failed"
			}
			job, e := RunSupervisionReview(context.Background(), opts, engine)
			if e != nil || job.State != want {
				t.Fatalf("job: %+v, want %s, %v", job, want, e)
			}
		})
	}
}

func TestSupervisionReviewCandidatesRequireCompletedIdentity(t *testing.T) {
	for _, state := range []string{StateDone, StateBlocked, StateCanceled, StateAbandoned, StateWaitingInput} {
		t.Run(state, func(t *testing.T) {
			store, opts := supervisionFixture(t)
			supervisionAppend(t, store, opts, KindOpened, `{}`)
			detail, _ := json.Marshal(terminalDetail{State: state})
			supervisionAppend(t, store, opts, KindTerminal, string(detail))
			got := supervisionObserve(t, opts)
			if state == StateDone {
				if got.Review == nil || got.Review.State != "pending" || got.Review.ID != "" || got.Review.Reason == "" {
					t.Fatalf("legacy identity: %+v", got)
				}
			} else if got.Review != nil {
				t.Fatalf("noncompleted review: %+v", got)
			}
		})
	}
	for _, detail := range []string{`{"state":"done","cleanup_pending":true}`, `{"state":"done","bookkeeping_pending":true}`, `{"state":"done","pending_ref_deletions":[{"ref":"refs/batuta/parked/x"}]}`} {
		store, opts := supervisionFixture(t)
		supervisionAppend(t, store, opts, KindOpened, `{}`)
		supervisionAppend(t, store, opts, KindTerminal, detail)
		if got := supervisionObserve(t, opts); got.Review != nil {
			t.Fatalf("recovery became candidate: %+v", got)
		}
	}
}

func TestSupervisionReviewForegroundLifecycle(t *testing.T) {
	opts, _, spec := supervisionReviewFixture(t)
	launches := 0
	engine := fakeSupervisionReview(t, opts, spec, &launches)
	lock := filepath.Join(opts.Workspace, journal.Dir, "other.lock")
	if _, err := (presenceLock{RefreshedAt: opts.Now()}).writeAtomic(lock); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	sleeps := 0
	err := Supervise(context.Background(), SuperviseOptions{Observer: opts, Review: &engine, Interval: 100 * time.Millisecond, Output: &output, Sleep: func(context.Context, time.Duration) error {
		sleeps++
		if sleeps > 1 {
			return errors.New("supervisor did not finish review")
		}
		return os.Remove(lock)
	}})
	if err != nil || launches != 1 || sleeps != 1 || !strings.Contains(output.String(), `"state":"reported"`) || !strings.Contains(output.String(), `"artifacts":`) {
		t.Fatalf("foreground: launches=%d, sleeps=%d, err=%v, output=%s", launches, sleeps, err, &output)
	}
}

func TestSupervisionReviewPersistsIntentBeforeEngine(t *testing.T) {
	opts, _, spec := supervisionReviewFixture(t)
	launches := 0
	engine := fakeSupervisionReview(t, opts, spec, &launches)
	original := engine.Runner
	crash := errors.New("lost supervisor")
	engine.Runner = commandRunnerFunc(func(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
		if len(c.Args) > 2 {
			job := supervisionObserve(t, opts).Review
			data, err := os.ReadFile(filepath.Join(supervisionReviewDirectory(opts, *job), "job.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, job); err != nil || job.State != "launching" || job.Attempts != 1 || job.LaunchedAt != opts.Now() {
				t.Fatalf("launch intent: %+v, %v", job, err)
			}
			panic(crash)
		}
		return original.Run(ctx, c)
	})
	func() {
		defer func() {
			if p := recover(); p != crash {
				t.Fatalf("crash = %v", p)
			}
		}()
		_, _ = RunSupervisionReview(context.Background(), opts, engine)
	}()
	job, err := RunSupervisionReview(context.Background(), opts, fakeSupervisionReview(t, opts, spec, &launches))
	if err != nil || job.State != "uncertain" || job.Attempts != 1 || launches != 0 {
		t.Fatalf("recovery: %+v, launches=%d, %v", job, launches, err)
	}
}

func TestSupervisionReviewRejectsChangedReportedSnapshot(t *testing.T) {
	for _, changed := range []string{"source", "spec", "artifact"} {
		t.Run(changed, func(t *testing.T) {
			opts, _, spec := supervisionReviewFixture(t)
			launches := 0
			engine := fakeSupervisionReview(t, opts, spec, &launches)
			job, err := RunSupervisionReview(context.Background(), opts, engine)
			if err != nil || job.State != "reported" {
				t.Fatalf("review: %+v, %v", job, err)
			}
			path := filepath.Join(job.Snapshot, "source.txt")
			if changed == "spec" {
				path = job.Spec
			}
			if changed == "artifact" {
				path = filepath.Join(job.Artifacts, "findings.json")
			}
			if err := os.WriteFile(path, []byte("changed after report"), 0600); err != nil {
				t.Fatal(err)
			}
			job, err = RunSupervisionReview(context.Background(), opts, engine)
			if err != nil || job.State != "failed" || launches != 1 {
				t.Fatalf("stale evidence: %+v, launches=%d, %v", job, launches, err)
			}
		})
	}
}
