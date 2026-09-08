package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

func answerRecords(t *testing.T, store *journal.Store, delivery string) []journal.Record {
	t.Helper()
	records, err := store.Read(delivery)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func copyAnswerDelivery(t *testing.T, store *journal.Store, delivery string, records []journal.Record) {
	t.Helper()
	for _, record := range records {
		if _, err := store.Append(delivery, record); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnswerBindsToDelivery(t *testing.T) {
	m := resumableAnswerWatch(t)
	synctest.Test(t, func(t *testing.T) {
		// Advance the virtual clock so the public API closes the fixture's human pause.
		time.Sleep(m.currentTime.Sub(time.Now()))
		before := answerRecords(t, m.store, m.delivery)
		copyAnswerDelivery(t, m.store, "aaa-other", before)
		var graph routing.DeliveryGraph
		if err := json.Unmarshal(before[len(before)-1].Graph, &graph); err != nil {
			t.Fatal(err)
		}
		task := graphTask(&graph, m.panel.Detail.Task)
		questionID := task.Attempts[len(task.Attempts)-1].Question.RequestID
		for _, wrong := range []struct{ delivery, question string }{{"missing", questionID}, {m.delivery, "stale"}, {"", questionID}, {m.delivery, ""}} {
			if _, err := AnswerDelivery(m.workspace, wrong.delivery, task.TaskID, wrong.question, "use JSON"); err == nil {
				t.Fatalf("accepted invalid binding: %+v", wrong)
			}
			if len(answerRecords(t, m.store, m.delivery)) != len(before) {
				t.Fatal("invalid binding recorded an answer")
			}
		}
		delivery, err := AnswerDelivery(m.workspace, m.delivery, task.TaskID, questionID, "use JSON")
		if err != nil || delivery != m.delivery {
			t.Fatalf("bound answer: %q, %v", delivery, err)
		}
		after := answerRecords(t, m.store, m.delivery)
		if len(after) != len(before)+1 || after[len(after)-1].Kind != KindAnswer {
			t.Fatal("bound answer not recorded")
		}
		if len(answerRecords(t, m.store, "aaa-other")) != len(before) {
			t.Fatal("other delivery changed")
		}
		if _, err := AnswerDelivery(m.workspace, m.delivery, task.TaskID, questionID, "use JSON"); err == nil {
			t.Fatal("duplicate answer accepted")
		}
	})
}

func TestTerminalSummaryMatchesRetainedRefs(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("continuation-retry-untracked", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run = %s, %v\n%s", state, err, &out)
	}
	records := readJournal(t, f, r.Delivery())
	var detail struct{ Summary Summary }
	if err := json.Unmarshal(records[len(records)-1].Detail, &detail); err != nil {
		t.Fatal(err)
	}
	refs, err := r.git.Parked(context.Background(), r.plan.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(detail.Summary.Parked, refs) {
		t.Fatalf("terminal refs = %v; retained = %v", detail.Summary.Parked, refs)
	}
	if len(snapshotRecords(t, f, r.Delivery())) == 0 {
		t.Fatal("test requires snapshots before cleanup")
	}
}

func TestFinishSurvivesWorktreeRemovalFailure(t *testing.T) {
	for _, retry := range []string{"resume", "abandon"} {
		t.Run(retry, func(t *testing.T) {
			f := setup(t)
			plan := "# Plan — Greetings\n\n**Goal:** Greeting.\n**Created:** 2026-09-06 · **Status:** approved\n\n## Tasks\n- [ ] 1. Add greeting one — backend/low\n      Scope: out/1.txt\n      Accept: greeting exists → test -f out/1.txt\n"
			if err := os.WriteFile(filepath.Join(f.root, ".batuta", "plans", "greetings.md"), []byte(plan), 0o644); err != nil {
				t.Fatal(err)
			}
			f.run(t, "add", "-A")
			f.run(t, "commit", "-qm", "test: one task")
			var out bytes.Buffer
			opts := f.options("default", &out)
			opts.KeepWorktrees, opts.MaxWaves = true, 1
			r, err := New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Run(context.Background()); !errors.Is(err, ErrStopped) {
				t.Fatalf("stop: %v", err)
			}
			wt := r.worktrees[attemptKey("task_1", 1)]
			f.run(t, "worktree", "lock", wt.Root)
			r.opts.KeepWorktrees, r.opts.MaxWaves = false, 0
			out.Reset()
			if state, err := r.Run(context.Background()); err == nil || state != StateDone {
				t.Fatalf("finish = %s, %v", state, err)
			}
			records := readJournal(t, f, r.Delivery())
			if !strings.Contains(string(records[len(records)-1].Detail), "cleanup_pending") {
				t.Fatalf("cleanup failure not recorded: %s", records[len(records)-1].Detail)
			}
			if terminalState(records) != "" {
				t.Fatal("cleanup failure closed recovery")
			}
			if !strings.Contains(out.String(), "delivery "+r.Delivery()+": done") || !strings.Contains(out.String(), wt.Root) {
				t.Fatalf("missing recovery summary:\n%s", &out)
			}
			archived := f.run(t, "show", "HEAD:.batuta/plans/done/greetings.md")
			if !strings.Contains(archived, "- [x] 1.") {
				t.Fatalf("plan not ticked: %s", archived)
			}
			work := f.run(t, "show", "HEAD:WORK.md")
			before := f.run(t, "rev-parse", "HEAD")
			f.run(t, "worktree", "unlock", wt.Root)
			opts.Resume, opts.KeepWorktrees, opts.MaxWaves = r.Delivery(), false, 0
			if retry == "resume" {
				resumed, err := Resume(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
					t.Fatalf("retry = %s, %v", state, err)
				}
			} else if _, err := Abandon(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			if got := f.worktrees(t); len(got) != 0 {
				t.Fatalf("retained after retry: %v", got)
			}
			if terminalState(readJournal(t, f, r.Delivery())) != StateDone {
				t.Fatal("retry did not finalize original result")
			}
			if f.run(t, "rev-parse", "HEAD") != before || f.run(t, "show", "HEAD:WORK.md") != work {
				t.Fatal("cleanup retry repeated bookkeeping")
			}
		})
	}
}

func finishedTasksForBookkeeping(t *testing.T) (fixture, *Runner, *bytes.Buffer) {
	t.Helper()
	f := setup(t)
	plan := "# Plan — Greetings\n\n**Goal:** Greeting.\n**Created:** 2026-09-06 · **Status:** approved\n\n## Tasks\n- [ ] 1. Add greeting one — backend/low\n      Scope: out/1.txt\n      Accept: greeting exists → test -f out/1.txt\n"
	if err := os.WriteFile(filepath.Join(f.root, ".batuta", "plans", "greetings.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", "-A")
	f.run(t, "commit", "-qm", "test: one task")
	out := new(bytes.Buffer)
	opts := f.options("default", out)
	opts.MaxWaves, opts.KeepWorktrees = 1, true
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("stop: %v", err)
	}
	r.opts.MaxWaves, r.opts.KeepWorktrees = 0, false
	out.Reset()
	return f, r, out
}

func TestFinishRecordsTerminalAfterBookkeeping(t *testing.T) {
	f, r, out := finishedTasksForBookkeeping(t)
	r.git.Runner = commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		if cmd.Directory == f.root && len(cmd.Args) > 0 && cmd.Args[0] == "commit" {
			records := readJournal(t, f, r.Delivery())
			if pendingFinalization(records) == nil {
				t.Error("no recovery checkpoint before bookkeeping commit")
			}
			if terminalState(records) != "" {
				t.Error("delivery ended before bookkeeping")
			}
			return publication.CommandResult{ExitCode: 1}, errors.New("injected bookkeeping failure")
		}
		return (publication.ExecRunner{}).Run(ctx, cmd)
	})
	if state, err := r.Run(context.Background()); err == nil || state != StateDone {
		t.Fatalf("finish = %s, %v", state, err)
	}
	records := readJournal(t, f, r.Delivery())
	if records[len(records)-1].Kind == KindTerminal || !strings.Contains(string(records[len(records)-1].Detail), "injected bookkeeping failure") {
		t.Fatalf("bookkeeping failure not journaled: %s %s", records[len(records)-1].Kind, records[len(records)-1].Detail)
	}
	if !strings.Contains(out.String(), "batuta loop --resume "+r.Delivery()) {
		t.Fatalf("missing recovery command: %s", out)
	}
}

func TestResumeRepeatsFailedBookkeeping(t *testing.T) {
	for _, failure := range []string{"move", "stage", "commit", "after-commit", "before-terminal"} {
		for _, retry := range []string{"resume", "abandon"} {
			t.Run(failure+"/"+retry, func(t *testing.T) {
				f, r, out := finishedTasksForBookkeeping(t)
				done := filepath.Join(f.root, ".batuta", "plans", "done")
				if failure == "move" {
					if err := os.WriteFile(done, []byte("block archive directory"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				interrupted := errors.New("interrupted after bookkeeping commit")
				r.git.Runner = commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
					if cmd.Directory == f.root && len(cmd.Args) > 0 {
						if (failure == "stage" && cmd.Args[0] == "add") || (failure == "commit" && cmd.Args[0] == "commit") {
							return publication.CommandResult{ExitCode: 1}, errors.New("injected bookkeeping failure")
						}
						if failure == "after-commit" && cmd.Args[0] == "commit" {
							result, err := (publication.ExecRunner{}).Run(ctx, cmd)
							if err != nil {
								return result, err
							}
							panic(interrupted)
						}
					}
					return (publication.ExecRunner{}).Run(ctx, cmd)
				})
				func() {
					defer func() {
						if got := recover(); got != nil && got != interrupted {
							panic(got)
						}
					}()
					state, err := r.Run(context.Background())
					if state != StateDone || (failure != "before-terminal" && err == nil) {
						t.Fatalf("finish = %s, %v", state, err)
					}
				}()
				if failure == "move" {
					if err := os.Remove(done); err != nil {
						t.Fatal(err)
					}
				}
				if failure == "before-terminal" {
					// Simulate losing only the final terminal append after bookkeeping completed.
					path := filepath.Join(f.root, journal.Dir, r.Delivery()+".jsonl")
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
					if err := os.WriteFile(path, append(bytes.Join(lines[:len(lines)-1], []byte("\n")), '\n'), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				before := readJournal(t, f, r.Delivery())
				head := f.run(t, "rev-parse", "HEAD")
				opts := f.options("default", out)
				opts.Resume = r.Delivery()
				opts.Now = func() time.Time { return time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC) }
				if retry == "resume" {
					resumed, err := Resume(context.Background(), opts)
					if err != nil {
						t.Fatalf("resume: %v", err)
					}
					if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
						t.Fatalf("retry = %s, %v", state, err)
					}
				} else if state, err := Abandon(context.Background(), opts); err != nil || state != StateDone {
					t.Fatalf("abandon retry = %s, %v", state, err)
				}
				after := readJournal(t, f, r.Delivery())
				if terminalState(after) != StateDone {
					t.Fatal("retry did not finalize")
				}
				for _, rec := range after[len(before):] {
					if rec.Kind == KindStarted {
						t.Fatal("retry re-ran tasks")
					}
				}
				work := f.run(t, "show", "HEAD:WORK.md")
				if strings.Count(work, "Add greeting one →") != 1 {
					t.Fatalf("duplicate WORK entry: %s", work)
				}
				if !strings.Contains(f.run(t, "show", "HEAD:.batuta/plans/done/greetings.md"), "- [x] 1.") {
					t.Fatal("plan was not archived and committed")
				}
				if got := f.run(t, "status", "--porcelain"); got != "" {
					t.Fatalf("bookkeeping left changes: %s", got)
				}
				if (failure == "after-commit" || failure == "before-terminal") && f.run(t, "rev-parse", "HEAD") != head {
					t.Fatal("retry duplicated successful bookkeeping commit")
				}
			})
		}
	}
}

func TestBookkeepingDoesNotHideAnotherDelivery(t *testing.T) {
	f, r, _ := finishedTasksForBookkeeping(t)
	r.mu.Lock()
	summary := r.summaryLocked()
	r.mu.Unlock()
	if err := r.writeWork(summary, StateDone); err != nil {
		t.Fatal(err)
	}
	r.delivery = "greetings-20260906-040001"
	if err := r.writeWork(summary, StateDone); err != nil {
		t.Fatal(err)
	}
	work, err := os.ReadFile(filepath.Join(f.root, "WORK.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(work), "Add greeting one →") != 2 {
		t.Fatalf("new delivery was suppressed: %s", work)
	}
}

func TestParkedDeletionFailureIsJournaled(t *testing.T) {
	f, r, out := finishedTasksForBookkeeping(t)
	ref := "refs/batuta/parked/greetings/task_1-e1"
	f.run(t, "update-ref", ref, "HEAD")
	deleted := false
	r.git.Runner = commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		if len(cmd.Args) > 1 && cmd.Args[0] == "update-ref" && cmd.Args[1] == "-d" {
			deleted = true
			records := readJournal(t, f, r.Delivery())
			if kinds(records)[KindTerminal] == 0 {
				t.Error("deleted parked ref before terminal record")
			}
			return publication.CommandResult{ExitCode: 1}, errors.New("injected parked deletion failure")
		}
		return (publication.ExecRunner{}).Run(ctx, cmd)
	})
	if state, err := r.Run(context.Background()); err == nil || state != StateDone {
		t.Fatalf("finish = %s, %v", state, err)
	}
	records := readJournal(t, f, r.Delivery())
	if !deleted || kinds(records)[KindTerminal] == 0 || records[len(records)-1].Kind == KindTerminal || !strings.Contains(string(records[len(records)-1].Detail), "injected parked deletion failure") {
		t.Fatalf("deletion failure not journaled after terminal: %+v", records[len(records)-1])
	}
	if !strings.Contains(out.String(), "injected parked deletion failure") || !strings.Contains(out.String(), ref) {
		t.Fatalf("missing cleanup report: %s", out)
	}
	opts := f.options("default", out)
	opts.Resume = r.Delivery()
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("retry = %s, %v", state, err)
	}
	if got := f.run(t, "for-each-ref", "--format=%(refname)", ref); got != "" {
		t.Fatalf("ref retained: %s", got)
	}
}

func TestBookkeepingRecoveryRefusesForeignStagedPaths(t *testing.T) {
	f, r, out := finishedTasksForBookkeeping(t)
	r.git.Runner = commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		if len(cmd.Args) > 0 && cmd.Args[0] == "commit" {
			return publication.CommandResult{ExitCode: 1}, errors.New("injected bookkeeping failure")
		}
		return (publication.ExecRunner{}).Run(ctx, cmd)
	})
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("expected bookkeeping failure")
	}
	foreign := "foreign\twork\n.txt"
	if err := os.WriteFile(filepath.Join(f.root, foreign), []byte("user work"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", "--", foreign)
	head := f.run(t, "rev-parse", "HEAD")
	index := f.run(t, "diff", "--cached", "--binary")
	opts := f.options("default", out)
	opts.Resume = r.Delivery()
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "staged path outside bookkeeping") {
		t.Fatalf("retry error = %v", err)
	}
	if f.run(t, "rev-parse", "HEAD") != head || f.run(t, "diff", "--cached", "--binary") != index {
		t.Fatal("retry changed user index or HEAD")
	}
	f.run(t, "reset", "-q", "HEAD", "--", foreign)
	resumed, err = Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("retry = %s, %v", state, err)
	}
	if got := f.run(t, "ls-files", "--", foreign); got != "" {
		t.Fatal("committed foreign path")
	}
}
