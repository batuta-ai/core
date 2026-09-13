package loop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

func supervisionPolicyFixture(t *testing.T) (*journal.Store, SupervisionOptions, SupervisionEvent, SupervisionPolicy) {
	t.Helper()
	store, opts := supervisionFixture(t)
	graph := routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskWaitingInput, Attempts: []routing.GraphTaskAttempt{{
		Execution: 1, State: routing.GraphTaskWaitingInput, Runtime: routing.RuntimeValue{Provider: "codex", Model: "small", Reasoning: "high"},
		BaseHeadSHA: strings.Repeat("a", 40), WorktreeID: "worktree-one", WorktreeRoot: opts.Workspace, ChildRunID: "child-one",
		Question: &routing.TaskQuestion{RequestID: "sha256:" + strings.Repeat("c", 64), Prompt: "Clarify the approved task ownership", ContextDigest: "sha256:" + strings.Repeat("b", 64)},
	}}}}}
	data, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []journal.Record{{Kind: KindOpened, Detail: json.RawMessage(`{"slug":"approved"}`)}, {Kind: KindQuestion, TaskID: "task_1", Detail: json.RawMessage(`{"execution":1,"request_id":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`)}} {
		record.Graph, record.At = data, opts.Now()
		if _, err := store.Append(opts.Delivery, record); err != nil {
			t.Fatal(err)
		}
	}
	events := supervisionObserve(t, opts).Events
	event := events[len(events)-1]
	plan := []byte("Approved: task_1 belongs to its worker; preserve scope and tests.")
	if err := os.WriteFile(filepath.Join(opts.Workspace, "approved-plan.md"), plan, 0600); err != nil {
		t.Fatal(err)
	}
	policy := SupervisionPolicy{Delivery: event.Delivery, TaskID: event.TaskID, Execution: event.Execution, QuestionID: event.QuestionID,
		QuestionDigest: event.Evidence.Digest, Action: SupervisionContinueApprovedTask, Ownership: "approved_task", MaxAttempts: 2,
		PlanEvidence: SupervisionEvidence{Path: "approved-plan.md", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(plan))}}
	return store, opts, event, policy
}

func TestSupervisionPolicyLeavesUnsafeStatePending(t *testing.T) {
	for _, scenario := range []string{"stale question", "different execution", "answered", "uncertain", "running", "canceled", "changed plan", "owner", "stale owner", "broken owner", "dispatch intent", "uncertain result", "disconnected success claim"} {
		t.Run(scenario, func(t *testing.T) {
			store, opts, event, policy := supervisionPolicyFixture(t)
			records := answerRecords(t, store, opts.Delivery)
			last := records[len(records)-1]
			var graph routing.DeliveryGraph
			if err := json.Unmarshal(last.Graph, &graph); err != nil {
				t.Fatal(err)
			}
			kind, detail := KindProgress, `{}`
			switch scenario {
			case "stale question":
				graph.Tasks[0].Attempts[0].Question.RequestID = "sha256:" + strings.Repeat("d", 64)
			case "different execution":
				graph.Tasks[0].Attempts[0].Execution++
			case "answered":
				graph.Tasks[0].Attempts[0].Question.Answer = &routing.TaskAnswer{Value: "already answered"}
			case "uncertain":
				graph.Tasks[0].BlockerCode = blockerSubmissionUncertain
			case "running":
				graph.Tasks = append(graph.Tasks, routing.GraphTask{TaskID: "other", State: routing.GraphTaskRunning})
			case "canceled":
				kind, detail = KindTerminal, `{"state":"canceled"}`
			case "dispatch intent":
				kind, detail = KindDispatchIntent, `{"execution":1,"backend":"acp","submission":"uncertain"}`
			case "uncertain result":
				kind, detail = KindDispatchResult, `{"execution":1,"backend":"acp","submission":"uncertain"}`
			case "disconnected success claim":
				kind, detail = KindDispatchResult, `{"execution":1,"backend":"acp","submission":"submitted","receipt":{"submission":{"state":"submitted"},"transport":{"outcome":"disconnected"},"worker":{"outcome":"success"}}}`
			case "changed plan":
				if err := os.WriteFile(filepath.Join(opts.Workspace, policy.PlanEvidence.Path), []byte("different scope"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				owner := presenceLock{PID: os.Getpid(), Host: "another-host", StartedAt: opts.Now(), RefreshedAt: opts.Now()}
				if scenario == "stale owner" {
					owner.RefreshedAt = owner.RefreshedAt.Add(-10 * presenceFresh)
				}
				data, err := json.Marshal(owner)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "broken owner" {
					data = []byte("{")
				}
				if err := os.WriteFile(filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".lock"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, err := json.Marshal(graph)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append(opts.Delivery, journal.Record{Kind: kind, TaskID: event.TaskID, Detail: json.RawMessage(detail), Graph: data, At: opts.Now()}); err != nil {
				t.Fatal(err)
			}
			before := len(answerRecords(t, store, opts.Delivery))
			decision, err := InterveneSupervision(opts, event.ID, &policy)
			if err != nil || decision.Outcome != "pending" || decision.Attempts != 0 {
				t.Fatalf("decision = %+v, %v", decision, err)
			}
			if len(answerRecords(t, store, opts.Delivery)) != before {
				t.Fatal("unsafe state changed")
			}
		})
	}
}

func TestSupervisionPolicyBudgetSurvivesRestartAndPolicyChange(t *testing.T) {
	store, opts, event, policy := supervisionPolicyFixture(t)
	records := answerRecords(t, store, opts.Delivery)
	valid := records[len(records)-1]
	var graph routing.DeliveryGraph
	if err := json.Unmarshal(valid.Graph, &graph); err != nil {
		t.Fatal(err)
	}
	// A malformed transition is rejected by the real bound-answer API.
	graph.Tasks[0].Attempts[0].ChildRunID = ""
	data, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(opts.Delivery, journal.Record{Kind: KindProgress, Detail: json.RawMessage(`{}`), Graph: data}); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		decision, err := InterveneSupervision(opts, event.ID, &policy)
		if err == nil || decision.Attempts != attempt || decision.Reason != "bound_answer_rejected" {
			t.Fatalf("attempt = %+v, %v", decision, err)
		}
	}
	if _, err := store.Append(opts.Delivery, journal.Record{Kind: KindProgress, Detail: json.RawMessage(`{}`), Graph: valid.Graph}); err != nil {
		t.Fatal(err)
	}
	policy.MaxAttempts = 3
	opts.CursorPath = filepath.Join(opts.Workspace, "fresh-observer.json")
	decision, err := InterveneSupervision(opts, event.ID, &policy)
	if err != nil || decision.Attempts != 2 || decision.MaxAttempts != 2 || decision.Reason != "attempt_limit" {
		t.Fatalf("reset budget: %+v, %v", decision, err)
	}
	for _, record := range answerRecords(t, store, opts.Delivery) {
		if record.Kind == KindAnswer {
			t.Fatal("answered beyond budget")
		}
	}
}

func TestSupervisionPolicyBoundAnswer(t *testing.T) {
	store, opts, event, policy := supervisionPolicyFixture(t)
	before := answerRecords(t, store, opts.Delivery)
	decision, err := InterveneSupervision(opts, event.ID, &policy)
	if err != nil || decision.Outcome != "answered" || decision.Attempts != 1 || decision.QuestionID != event.QuestionID || decision.Evidence != event.Evidence || decision.PolicyDigest == "" {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
	after := answerRecords(t, store, opts.Delivery)
	if len(after) != len(before)+1 || after[len(after)-1].Kind != KindAnswer {
		t.Fatal("bound answer not recorded")
	}
	var detail struct{ Answer string }
	if err := json.Unmarshal(after[len(after)-1].Detail, &detail); err != nil || detail.Answer != SupervisionRoutineAnswer {
		t.Fatalf("answer = %+v, %v", detail, err)
	}
	again, err := InterveneSupervision(opts, event.ID, &policy)
	if err != nil || again != decision || len(answerRecords(t, store, opts.Delivery)) != len(after) {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
}

func TestSupervisionPolicyRequiresExplicitScope(t *testing.T) {
	for _, field := range []string{"absent", "delivery", "task", "execution", "question", "digest", "ownership", "plan", "budget", "permission", "scope", "quota", "replay"} {
		t.Run(field, func(t *testing.T) {
			store, opts, event, policy := supervisionPolicyFixture(t)
			p := &policy
			switch field {
			case "absent":
				p = nil
			case "delivery":
				policy.Delivery = "another"
			case "task":
				policy.TaskID = "another"
			case "execution":
				policy.Execution++
			case "question":
				policy.QuestionID = "another"
			case "digest":
				policy.QuestionDigest = "another"
			case "ownership":
				policy.Ownership = ""
			case "plan":
				policy.PlanEvidence = SupervisionEvidence{}
			case "budget":
				policy.MaxAttempts = 100
			default:
				policy.Action = field
			}
			before := len(answerRecords(t, store, opts.Delivery))
			decision, err := InterveneSupervision(opts, event.ID, p)
			if err != nil || decision.Outcome != "pending" || decision.Attempts != 0 {
				t.Fatalf("decision = %+v, %v", decision, err)
			}
			if len(answerRecords(t, store, opts.Delivery)) != before || len(supervisionObserve(t, opts).Pending) == 0 {
				t.Fatal("unauthorized policy changed the delivery or consumed the notification")
			}
		})
	}
}

func TestSupervisionPolicyConcurrentObservers(t *testing.T) {
	store, opts, event, policy := supervisionPolicyFixture(t)
	before := len(answerRecords(t, store, opts.Delivery))
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			other := opts
			if i%2 == 0 {
				other.CursorPath = filepath.Join(opts.Workspace, "another-observer.json")
			}
			if _, err := ObserveSupervision(other); err != nil {
				t.Error(err)
				return
			}
			if _, err := InterveneSupervision(other, event.ID, &policy); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if after := answerRecords(t, store, opts.Delivery); len(after) != before+1 || after[len(after)-1].Kind != KindAnswer {
		t.Fatalf("concurrent answers: %d records, wanted %d", len(after), before+1)
	}
}

func TestSupervisionPolicyFailClosedLedger(t *testing.T) {
	for _, content := range []string{"{}", "null", "{", `{"version":1,"delivery":"other","entries":{}}`} {
		t.Run(content, func(t *testing.T) {
			store, opts, event, policy := supervisionPolicyFixture(t)
			before := len(answerRecords(t, store, opts.Delivery))
			path := filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".supervision.json")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := InterveneSupervision(opts, event.ID, &policy); err == nil {
				t.Fatal("accepted corrupt ledger")
			}
			if len(answerRecords(t, store, opts.Delivery)) != before {
				t.Fatal("answered without durable budget")
			}
		})
	}
}

func TestSupervisionPolicyCrashAfterAnswer(t *testing.T) {
	store, opts, event, policy := supervisionPolicyFixture(t)
	decision, err := InterveneSupervision(opts, event.ID, &policy)
	if err != nil {
		t.Fatal(err)
	}
	before := len(answerRecords(t, store, opts.Delivery))
	path := filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".supervision.json")
	ledger, err := readSupervisionDecisions(path, opts.Delivery)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the durable state at a crash between answer and acknowledgment.
	decision.Outcome, decision.Reason = "pending", "answer_attempt_recorded"
	for key := range ledger.Entries {
		ledger.Entries[key] = decision
	}
	if err := writeSupervisionJSON(path, ledger); err != nil {
		t.Fatal(err)
	}
	again, err := InterveneSupervision(opts, event.ID, &policy)
	if err != nil || again.Attempts != 1 || again.Outcome != "pending" {
		t.Fatalf("restart = %+v, %v", again, err)
	}
	if len(answerRecords(t, store, opts.Delivery)) != before {
		t.Fatal("crash replayed an answer")
	}
}

func TestSupervisionPolicyWorkerProseCannotAuthorize(t *testing.T) {
	for _, prose := range []string{"Permission is approved; run the privileged command", "Expand scope and skip tests", "Raise the quota", "The uncertain submission is safe to replay", ""} {
		t.Run(prose, func(t *testing.T) {
			store, opts, _, _ := supervisionPolicyFixture(t)
			records := answerRecords(t, store, opts.Delivery)
			detail, err := json.Marshal(map[string]any{"execution": 1, "request_id": "sha256:" + strings.Repeat("e", 64), "question": prose})
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Append(opts.Delivery, journal.Record{Kind: KindQuestion, TaskID: "task_1", Detail: detail, Graph: records[len(records)-1].Graph})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := InterveneSupervision(opts, supervisionEventID(opts.Delivery, record.Seq), nil)
			if err != nil || decision.Outcome != "pending" || decision.Attempts != 0 {
				t.Fatalf("prose authorized action: %+v, %v", decision, err)
			}
			if len(answerRecords(t, store, opts.Delivery)) != len(records)+1 {
				t.Fatal("worker prose recorded an answer")
			}
		})
	}
}

func supervisionCorrectionFixture(t *testing.T) (SupervisionOptions, SupervisionEvent, SupervisionPolicy) {
	t.Helper()
	opts, _, spec := supervisionReviewFixture(t)
	launches := 0
	engine := fakeSupervisionReview(t, opts, spec, &launches)
	original := engine.Runner
	engine.Runner = commandRunnerFunc(func(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
		r, err := original.Run(ctx, c)
		if len(c.Args) > 2 {
			writeSupervisionReviewEvidence(t, c, "FIX_BEFORE_SHIP", true)
			r.ExitCode = 2
		}
		return r, err
	})
	job, err := RunSupervisionReview(context.Background(), opts, engine)
	if err != nil {
		t.Fatal(err)
	}
	observation := supervisionObserve(t, opts)
	var event SupervisionEvent
	for _, e := range observation.Pending {
		if e.Kind == "review" {
			event = e
		}
	}
	policy := SupervisionPolicy{Delivery: opts.Delivery, Action: SupervisionProposeCorrection, Ownership: "approved_correction", MaxAttempts: 2,
		PlanEvidence: SupervisionEvidence{Path: "correction-plan.md", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(spec)))},
		Correction:   &SupervisionCorrectionPolicy{ReviewID: job.ID, ReportDigest: event.Evidence.Digest, SpecDigest: job.SpecDigest, Delivery: "correction-one", MaxCorrections: 1},
	}
	if err := os.WriteFile(filepath.Join(opts.Workspace, "correction-plan.md"), []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	return opts, event, policy
}

func TestSupervisionCorrectionExplicitPolicyAndBudget(t *testing.T) {
	for _, scenario := range []string{"approved", "missing", "scope", "digest", "replay", "changed plan", "owner"} {
		t.Run(scenario, func(t *testing.T) {
			opts, event, policy := supervisionCorrectionFixture(t)
			p := &policy
			switch scenario {
			case "missing":
				p = nil
			case "scope":
				policy.Correction.SpecDigest = "expanded"
			case "digest":
				policy.Correction.ReportDigest = "wrong"
			case "replay":
				policy.Action = "replay"
			case "changed plan":
				if err := os.WriteFile(filepath.Join(opts.Workspace, "correction-plan.md"), []byte("expanded"), 0600); err != nil {
					t.Fatal(err)
				}
			case "owner":
				if _, err := (presenceLock{RefreshedAt: opts.Now()}).writeAtomic(filepath.Join(opts.Workspace, journal.Dir, "other.lock")); err != nil {
					t.Fatal(err)
				}
			}
			before, err := readSupervisionRecords(opts)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := InterveneSupervision(opts, event.ID, p)
			if err != nil {
				t.Fatal(err)
			}
			want := "pending"
			if scenario == "approved" {
				want = "proposed"
			}
			if decision.Outcome != want {
				t.Fatalf("decision=%+v", decision)
			}
			after, err := readSupervisionRecords(opts)
			if err != nil || len(after) != len(before) {
				t.Fatalf("policy mutated delivery: %v", err)
			}
			if scenario == "approved" {
				if decision.Correction == nil || decision.Correction.Delivery != "correction-one" || decision.Correction.ReviewID != event.ReviewID {
					t.Fatalf("proposal=%+v", decision)
				}
				policy.MaxAttempts = 3
				policy.Correction.MaxCorrections = 3
				policy.Correction.Delivery = "another-child"
				again, err := InterveneSupervision(opts, event.ID, &policy)
				if err != nil || again.Attempts != 1 || again.Correction.Delivery != "correction-one" {
					t.Fatalf("replayed proposal: %+v, %v", again, err)
				}
			}
		})
	}
}

func TestSupervisionCorrectionFailedAttemptsDoNotReset(t *testing.T) {
	opts, event, policy := supervisionCorrectionFixture(t)
	if err := os.WriteFile(filepath.Join(opts.Workspace, "correction-plan.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		decision, err := InterveneSupervision(opts, event.ID, &policy)
		if err != nil || decision.Attempts != i || decision.Reason != "plan_evidence_mismatch" {
			t.Fatalf("attempt: %+v %v", decision, err)
		}
	}
	policy.MaxAttempts = 3
	decision, err := InterveneSupervision(opts, event.ID, &policy)
	if err != nil || decision.Attempts != 2 || decision.Outcome != "exhausted" {
		t.Fatalf("reset: %+v %v", decision, err)
	}
}

func TestSupervisionCorrectionChainCannotResetBudget(t *testing.T) {
	opts, event, policy := supervisionCorrectionFixture(t)
	first, err := InterveneSupervision(opts, event.ID, &policy)
	if err != nil || first.Outcome != "proposed" {
		t.Fatalf("first: %+v %v", first, err)
	}
	parent := supervisionObserve(t, opts).Review
	store, err := journal.Open(opts.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	opts.Delivery = "correction-one"
	opts.CursorPath = filepath.Join(opts.Workspace, "child-cursor.json")
	opened, _ := json.Marshal(openedDetail{Slug: parent.Slug, PlanPath: ".batuta/plans/done/delivery.md", PlanDigest: parent.SpecDigest, Head: parent.FinalCommit})
	supervisionAppend(t, store, opts, KindOpened, string(opened))
	if _, err := supervisionReviewGit(context.Background(), opts.Workspace, "commit", "--allow-empty", "-qm", "correction"); err != nil {
		t.Fatal(err)
	}
	head, err := supervisionReviewGit(context.Background(), opts.Workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	terminal, _ := json.Marshal(terminalDetail{State: StateDone, FinalCommit: strings.TrimSpace(string(head))})
	supervisionAppend(t, store, opts, KindTerminal, string(terminal))
	spec, err := os.ReadFile(filepath.Join(opts.Workspace, "correction-plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	launches := 0
	engine := fakeSupervisionReview(t, opts, string(spec), &launches)
	original := engine.Runner
	engine.Runner = commandRunnerFunc(func(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
		r, err := original.Run(ctx, c)
		if len(c.Args) > 2 {
			writeSupervisionReviewEvidence(t, c, "REWORK", true)
			r.ExitCode = 3
		}
		return r, err
	})
	job, err := RunSupervisionReview(context.Background(), opts, engine)
	if err != nil || job.State != "reported" || launches != 1 || job.ID == parent.ID {
		t.Fatalf("child review: %+v %v", job, err)
	}
	observation := supervisionObserve(t, opts)
	for _, e := range observation.Pending {
		if e.Kind == "review" {
			event = e
		}
	}
	policy.Delivery = opts.Delivery
	policy.MaxAttempts = 3
	policy.Correction = &SupervisionCorrectionPolicy{ReviewID: job.ID, ReportDigest: event.Evidence.Digest, SpecDigest: job.SpecDigest, Delivery: "correction-two", MaxCorrections: 3}
	decision, err := InterveneSupervision(opts, event.ID, &policy)
	if err != nil || decision.Outcome != "exhausted" || decision.Attempts != 1 || decision.Correction.Depth != 2 || decision.Correction.MaxCorrections != 1 || decision.MaxAttempts != 2 {
		t.Fatalf("chain reset: %+v %v", decision, err)
	}
}

func TestSupervisionCorrectionObserverDispatchAfterAcknowledgment(t *testing.T) {
	opts, event, policy := supervisionCorrectionFixture(t)
	if err := AcknowledgeSupervision(opts, event.ID); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Supervise(context.Background(), SuperviseOptions{Observer: opts, Interval: 100 * time.Millisecond, Once: true, Policy: &policy, Output: &output}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"outcome":"proposed"`) || !strings.Contains(output.String(), `"acceptance":"pending"`) {
		t.Fatalf("missing decision: %s", &output)
	}
}

func TestSupervisionCorrectionShrinkingBudgetRemainsReadable(t *testing.T) {
	opts, event, policy := supervisionCorrectionFixture(t)
	if err := os.WriteFile(filepath.Join(opts.Workspace, "correction-plan.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := InterveneSupervision(opts, event.ID, &policy); err != nil {
			t.Fatal(err)
		}
	}
	policy.MaxAttempts = 1
	for i := 0; i < 2; i++ {
		decision, err := InterveneSupervision(opts, event.ID, &policy)
		if err != nil || decision.Outcome != "exhausted" || decision.Attempts != 2 {
			t.Fatalf("shrunk budget: %+v %v", decision, err)
		}
	}
}

func TestSupervisionCorrectionEvidenceCannotAuthorizeItself(t *testing.T) {
	for _, outcome := range []string{"SHIP", "incomplete_coverage", "execution_failed", "uncertain"} {
		t.Run(outcome, func(t *testing.T) {
			opts, event, policy := supervisionCorrectionFixture(t)
			job := supervisionObserve(t, opts).Review
			job.Outcome = outcome
			if outcome == "execution_failed" {
				job.State = "failed"
			}
			if outcome == "uncertain" {
				job.State = "uncertain"
			}
			if err := writeSupervisionJSON(filepath.Join(supervisionReviewDirectory(opts, *job), "job.json"), job); err != nil {
				t.Fatal(err)
			}
			for _, e := range supervisionObserve(t, opts).Pending {
				if e.Kind == "review" && e.ReviewOutcome == outcome {
					event = e
				}
			}
			policy.Correction.ReportDigest = event.Evidence.Digest
			decision, err := InterveneSupervision(opts, event.ID, &policy)
			if err != nil || decision.Outcome != "pending" || decision.Attempts != 0 || decision.Reason != "review_requires_judgment" {
				t.Fatalf("self-authorization: %+v %v", decision, err)
			}
		})
	}
}

func TestSupervisionCorrectionConcurrentObservers(t *testing.T) {
	opts, event, policy := supervisionCorrectionFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			decision, err := InterveneSupervision(opts, event.ID, &policy)
			if err != nil || decision.Outcome != "proposed" || decision.Attempts != 1 {
				t.Errorf("concurrent proposal: %+v %v", decision, err)
			}
		})
	}
	wg.Wait()
}
