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
