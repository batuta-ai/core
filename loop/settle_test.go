package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func TestResumeDispatchShutdownBeforeSubmission(t *testing.T) {
	for _, boundary := range []journal.Kind{KindDispatchResult, KindFailure} {
		t.Run(string(boundary), func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			opts.KeepWorktrees = true
			opts.Transport = loopACPBeforeSubmission(t, func() error { return errors.New("shutdown unresolved") })
			r := prepareACPAttempt(t, f, opts)
			defer r.Release()
			if _, err := r.runPreparingWaves(context.Background()); err != nil {
				t.Fatal(err)
			}
			records := readJournal(t, f, r.delivery)
			end := 0
			var dispatch dispatchDetail
			for i, record := range records {
				if record.Kind == KindDispatchResult {
					if err := json.Unmarshal(record.Detail, &dispatch); err != nil {
						t.Fatal(err)
					}
				}
				if record.Kind == boundary {
					end = i + 1
					break
				}
			}
			if end == 0 || dispatch.Submission != executor.SubmissionNotSubmitted || !dispatch.ReconciliationRequired {
				t.Fatalf("missing %s shutdown evidence: %+v", boundary, dispatch)
			}
			records = records[:end]
			delivery := "shutdown-" + strings.ReplaceAll(string(boundary), "_", "-")
			copyAnswerDelivery(t, r.store, delivery, records)
			resumeOpts := f.options("default", &out)
			resumeOpts.Resume = delivery
			resumed, err := Resume(context.Background(), resumeOpts)
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Release()
			task, _ := resumed.graph.Task("task_1")
			if task.State != routing.GraphTaskBlocked || len(task.Attempts) != 1 || task.BlockerCode != blockerSubmissionUncertain {
				t.Fatalf("shutdown reconciliation lost at %s: %+v", boundary, task)
			}
			if state, err := resumed.Run(context.Background()); err != nil || state != StateBlocked {
				t.Fatalf("resumed Run = %s, %v\n%s", state, err, out.String())
			}
			if body, err := os.ReadFile(filepath.Join(task.Attempts[0].WorktreeRoot, "shared.txt")); err != nil || string(body) != "unverified startup work" {
				t.Fatalf("startup work lost: %q / %v", body, err)
			}
			replayed := answerRecords(t, resumed.store, delivery)
			if len(replayed) < len(records) {
				t.Fatal("recovery discarded dispatch history")
			}
			for i, original := range records {
				if !bytes.Equal(replayed[i].Detail, original.Detail) || !bytes.Equal(replayed[i].Graph, original.Graph) || replayed[i].Kind != original.Kind {
					t.Fatalf("recovery rewrote original record %d", i)
				}
			}
			counts := kinds(replayed)
			if counts[KindStarted] != 1 || counts[KindDispatchResult] != 1 || counts[KindCandidate] != 0 || counts[KindGates] != 0 || counts[KindSettled] != 0 {
				t.Fatalf("shutdown recovery consumed work: %v", counts)
			}
		})
	}
}

func TestResumeDispatchCrashBoundaries(t *testing.T) {
	for _, boundary := range []string{"before_intent", "intent", "prompt", "result", "gates", "old_cli"} {
		t.Run(boundary, func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			opts.KeepWorktrees = true
			var r *Runner
			var promptRecords []journal.Record
			opts.Transport = loopACPTransport(t, func(e executor.Execution) (string, string) {
				promptRecords = readJournal(t, f, r.delivery)
				if err := os.MkdirAll(filepath.Join(e.Request.Cwd, "out"), 0755); err != nil {
					t.Error(err)
				}
				if err := os.WriteFile(filepath.Join(e.Request.Cwd, "out", "1.txt"), []byte("ok\n"), 0644); err != nil {
					t.Error(err)
				}
				return "completed", "end_turn"
			})
			r = prepareACPAttempt(t, f, opts)
			if _, err := r.runPreparingWaves(context.Background()); err != nil {
				t.Fatal(err)
			}
			records := readJournal(t, f, r.delivery)
			kind := journal.Kind("dispatch_intent")
			switch boundary {
			case "before_intent", "old_cli":
				kind = KindStarted
			case "result":
				kind = "dispatch_result"
			case "gates":
				kind = KindGates
			}
			if boundary == "prompt" {
				records = promptRecords
			} else {
				end := 0
				for i, record := range records {
					if record.Kind == kind {
						end = i + 1
						break
					}
				}
				if end == 0 {
					t.Fatalf("no %s boundary", kind)
				}
				records = records[:end]
			}
			if boundary == "old_cli" {
				var detail map[string]json.RawMessage
				if err := json.Unmarshal(records[len(records)-1].Detail, &detail); err != nil {
					t.Fatal(err)
				}
				delete(detail, "dispatch")
				raw, err := json.Marshal(detail)
				if err != nil {
					t.Fatal(err)
				}
				records[len(records)-1].Detail = raw
			}
			delivery := "crash-" + strings.ReplaceAll(boundary, "_", "-")
			copyAnswerDelivery(t, r.store, delivery, records)
			resumeOpts := f.options("default", &out) // default CLI must not replay ACP
			resumeOpts.Resume = delivery
			resumed, err := Resume(context.Background(), resumeOpts)
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Release()
			task, _ := resumed.graph.Task("task_1")
			if boundary == "before_intent" || boundary == "old_cli" {
				if task.State != routing.GraphTaskPreparing || len(task.Attempts) != 2 {
					t.Fatalf("safe/legacy resume changed: %+v", task)
				}
				return
			}
			if task.State != routing.GraphTaskBlocked || len(task.Attempts) != 1 || task.BlockerCode != blockerSubmissionUncertain {
				t.Fatalf("uncertain crash replayed: %+v", task)
			}
			if state, err := resumed.Run(context.Background()); err != nil || state != StateBlocked {
				t.Fatalf("Run = %s, %v\n%s", state, err, out.String())
			}
			if body, err := os.ReadFile(filepath.Join(task.Attempts[0].WorktreeRoot, "out", "1.txt")); err != nil || string(body) != "ok\n" {
				t.Fatalf("crash work lost: %q / %v", body, err)
			}
			counts := kinds(answerRecords(t, resumed.store, delivery))
			if counts[KindStarted] != 1 || counts[KindCandidate] != 0 || counts[KindSettled] != 0 {
				t.Fatalf("unverified crash work consumed: %v", counts)
			}
		})
	}
}

func TestResumeIndependentVerifierCrashBoundaries(t *testing.T) {
	for _, boundary := range []string{"intent", "prompt", "result", "gates", "cli", "not_submitted"} {
		t.Run(boundary, func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			var r *Runner
			var promptRecords []journal.Record
			if boundary != "cli" {
				opts.VerifierTransport = loopACPTransport(t, func(e executor.Execution) (string, string) {
					promptRecords = readJournal(t, f, r.delivery)
					return "TASK 1: DONE\n", "end_turn"
				})
				if boundary == "not_submitted" {
					opts.VerifierTransport.Qualifications = nil
				}
			}
			r = prepareACPAttempt(t, f, opts)
			if _, err := r.runPreparingWaves(context.Background()); err != nil {
				t.Fatal(err)
			}
			records := readJournal(t, f, r.delivery)
			kind := journal.Kind("verifier_dispatch_intent")
			switch boundary {
			case "result", "not_submitted":
				kind = "verifier_dispatch_result"
			case "gates", "cli":
				kind = KindGates
			}
			if boundary == "prompt" {
				records = promptRecords
			} else {
				end := 0
				for i, record := range records {
					if record.Kind == kind {
						end = i + 1
						break
					}
				}
				if end == 0 {
					t.Fatalf("no %s boundary", kind)
				}
				records = records[:end]
			}
			if boundary != "cli" && boundary != "not_submitted" {
				if reason := supervisionPendingQuestion(records, SupervisionEvent{}); reason != "reconciliation_required" {
					t.Fatalf("supervision bypassed pending verifier before replay: %q", reason)
				}
			}
			delivery := "verifier-crash-" + strings.ReplaceAll(boundary, "_", "-")
			copyAnswerDelivery(t, r.store, delivery, records)
			resumeOpts := f.options("default", &out)
			resumeOpts.Resume = delivery
			resumed, err := Resume(context.Background(), resumeOpts)
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Release()
			task, _ := resumed.graph.Task("task_1")
			if boundary == "cli" || boundary == "not_submitted" {
				if task.State != routing.GraphTaskPreparing || len(task.Attempts) != 2 {
					t.Fatalf("safe verifier changed CLI recovery: %+v", task)
				}
				return
			}
			if task.State != routing.GraphTaskBlocked || len(task.Attempts) != 1 || task.BlockerCode != blockerSubmissionUncertain {
				t.Fatalf("verifier crash replayed: %+v", task)
			}
			var original routing.DeliveryGraph
			if err := json.Unmarshal(records[len(records)-1].Graph, &original); err != nil {
				t.Fatal(err)
			}
			originalTask, _ := original.Task("task_1")
			var taskDispatch, verifierDispatch dispatchDetail
			for _, record := range records {
				if record.TaskID != "task_1" {
					continue
				}
				switch record.Kind {
				case KindDispatchResult:
					if err := json.Unmarshal(record.Detail, &taskDispatch); err != nil {
						t.Fatal(err)
					}
				case KindVerifierIntent, KindVerifierResult:
					if err := json.Unmarshal(record.Detail, &verifierDispatch); err != nil {
						t.Fatal(err)
					}
				}
			}
			if taskDispatch.Backend != "cli" || taskDispatch.Execution != 1 || taskDispatch.RunID == "" || verifierDispatch.Backend != "acp" || verifierDispatch.RunID == "" || verifierDispatch.RunID == taskDispatch.RunID {
				t.Fatalf("independent dispatch identities missing: task=%+v verifier=%+v", taskDispatch, verifierDispatch)
			}
			if task.Attempts[0].ChildRunID != taskDispatch.RunID || task.Attempts[0].Runtime != originalTask.Attempts[0].Runtime {
				t.Fatalf("CLI identity overwritten: %+v", task)
			}
			if state, err := resumed.Run(context.Background()); err != nil || state != StateBlocked {
				t.Fatalf("Run = %s, %v\n%s", state, err, out.String())
			}
			if body, err := os.ReadFile(filepath.Join(task.Attempts[0].WorktreeRoot, "out", "1.txt")); err != nil || string(body) != "ok\n" {
				t.Fatalf("crash work lost: %q / %v", body, err)
			}
			replayed := answerRecords(t, resumed.store, delivery)
			if reason := supervisionPendingQuestion(replayed, SupervisionEvent{}); reason == "" {
				t.Fatal("supervision authorized continuation of uncertain verifier work")
			}
			observer := SupervisionOptions{Workspace: f.root, Delivery: delivery, CursorPath: filepath.Join(t.TempDir(), "cursor.json")}
			observation, err := ObserveSupervision(observer)
			if err != nil || observation.Completed || observation.TerminalState != StateBlocked || observation.Review != nil {
				t.Fatalf("supervision accepted unverified delivery: %+v, %v", observation, err)
			}
			counts := kinds(answerRecords(t, resumed.store, delivery))
			if counts[KindStarted] != 1 || counts[KindCandidate] != 0 || counts[KindSettled] != 0 {
				t.Fatalf("unverified crash work consumed: %v", counts)
			}
		})
	}
}
