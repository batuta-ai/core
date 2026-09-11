package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

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
