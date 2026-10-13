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

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func parkedRecovery(t *testing.T, scenario string) (fixture, Options, string, string, string) {
	return parkedRecoveryFixture(t, setup(t), scenario)
}

func parkedRecoverySingle(t *testing.T, scenario, complexity string) (fixture, Options, string, string, string) {
	t.Helper()
	f := setup(t)
	oneMediumTaskPlan(t, f, "Add greeting one — backend/"+complexity, "out/1.txt", "out/1.txt exists → test -f out/1.txt")
	return parkedRecoveryFixture(t, f, scenario)
}

func parkedRecoveryFixture(t *testing.T, f fixture, scenario string) (fixture, Options, string, string, string) {
	t.Helper()
	var out bytes.Buffer
	opts := f.options(scenario, &out)
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	delivery := r.Delivery()
	opts.Resume = delivery
	wt := r.worktrees[attemptKey("task_1", 1)]
	ask := filepath.Join(f.root, ".batuta", "asks", "greetings-task-1.md")
	if _, err := os.Stat(ask); err != nil {
		t.Fatal(err)
	}
	return f, opts, delivery, wt.Root, ask
}

func TestConductorRecoverVerifierPass(t *testing.T) {
	f, opts, delivery, _, _ := parkedRecoverySingle(t, "continuation-ask-untracked", "high")
	if err := Recover(context.Background(), opts, delivery, "task_1"); err != nil {
		t.Fatalf("Recover() = %v", err)
	}
	var intents, results int
	for _, record := range readJournal(t, f, delivery) {
		if record.TaskID != "task_1" {
			continue
		}
		switch record.Kind {
		case KindVerifierIntent:
			intents++
			var detail dispatchDetail
			if err := json.Unmarshal(record.Detail, &detail); err != nil {
				t.Fatal(err)
			}
			if detail.Executor != "codex" || detail.Model != "fake-high" || detail.Execution != 1 {
				t.Fatalf("verifier dispatch = %+v", detail)
			}
		case KindVerifierResult:
			results++
		}
	}
	if intents != 1 || results != 1 {
		t.Fatalf("verifier dispatch records: intents %d, results %d", intents, results)
	}
	if report := conductorGate(t, f, delivery); !report.Passed || report.Verifier == nil || !report.Verifier.Pass {
		t.Fatalf("conductor gates = %+v", report)
	}
	if task := graphTask(recoveryGraph(t, f, delivery), "task_1"); task == nil || task.State != routing.GraphTaskCandidate {
		t.Fatalf("recovered task = %+v", task)
	}
}

func TestConductorRecoverVerifierRejection(t *testing.T) {
	f, opts, delivery, wt, ask := parkedRecoverySingle(t, "continuation-ask-verifier-reject", "high")
	head := f.run(t, "-C", wt, "rev-parse", "HEAD")
	before := recoveryGraph(t, f, delivery)
	err := Recover(context.Background(), opts, delivery, "task_1")
	var failure *GateFailureError
	if !errors.As(err, &failure) || !strings.Contains(err.Error(), "TASK 1: INCOMPLETE — greeting is wrong") {
		t.Fatalf("Recover() = %v, want verifier rejection for criterion 1", err)
	}
	report := conductorGate(t, f, delivery)
	if report.Passed || report.Verifier == nil || report.Verifier.Pass {
		t.Fatalf("conductor gates = %+v", report)
	}
	if got := f.run(t, "-C", wt, "rev-parse", "HEAD"); got != head {
		t.Fatalf("worktree HEAD = %s, want %s", got, head)
	}
	if _, err := os.Stat(ask); err != nil {
		t.Fatal(err)
	}
	a, b := graphTask(before, "task_1"), graphTask(recoveryGraph(t, f, delivery), "task_1")
	if b.State != routing.GraphTaskWaitingInput || len(b.Attempts) != len(a.Attempts) || b.Attempts[0].Question == nil || b.Attempts[0].Question.Prompt != a.Attempts[0].Question.Prompt {
		t.Fatalf("task changed after verifier rejection: before %+v, after %+v", a, b)
	}
	for _, record := range readJournal(t, f, delivery) {
		if record.Kind == KindCandidate && record.TaskID == "task_1" {
			t.Fatal("rejected recovery recorded a candidate")
		}
	}
}

func TestConductorRecoverSkipsVerifierWhenNotNeeded(t *testing.T) {
	f, opts, delivery, _, _ := parkedRecoverySingle(t, "continuation-ask-untracked", "low")
	if err := Recover(context.Background(), opts, delivery, "task_1"); err != nil {
		t.Fatalf("Recover() = %v", err)
	}
	for _, record := range readJournal(t, f, delivery) {
		if record.TaskID == "task_1" && (record.Kind == KindVerifierIntent || record.Kind == KindVerifierResult) {
			t.Fatalf("unneeded verifier record: %s", record.Kind)
		}
	}
	if report := conductorGate(t, f, delivery); !report.Passed || report.Verifier != nil || len(report.Proofs) != 1 || !report.Proofs[0].Pass {
		t.Fatalf("conductor gates = %+v", report)
	}
	if task := graphTask(recoveryGraph(t, f, delivery), "task_1"); task == nil || task.State != routing.GraphTaskCandidate {
		t.Fatalf("recovered task = %+v", task)
	}
}

func TestConductorRecoverMissingAdapterRefusesBeforeWrite(t *testing.T) {
	f, opts, delivery, wt, ask := parkedRecoverySingle(t, "continuation-ask-untracked", "high")
	journalPath := filepath.Join(f.root, journal.Dir, delivery+".jsonl")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	head := f.run(t, "-C", wt, "rev-parse", "HEAD")
	opts.Skills = tempDir(t)
	if err := os.MkdirAll(filepath.Join(opts.Skills, "adapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = Recover(context.Background(), opts, delivery, "task_1")
	var failure *GateFailureError
	if err == nil || errors.As(err, &failure) || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("Recover() = %v, want missing adapter refusal", err)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refusal modified journal")
	}
	if got := f.run(t, "-C", wt, "rev-parse", "HEAD"); got != head {
		t.Fatalf("worktree HEAD = %s, want %s", got, head)
	}
	if _, err := os.Stat(ask); err != nil {
		t.Fatal(err)
	}
}

func recoveryGraph(t *testing.T, f fixture, delivery string) *routing.DeliveryGraph {
	t.Helper()
	records := readJournal(t, f, delivery)
	var graph routing.DeliveryGraph
	if err := json.Unmarshal(records[len(records)-1].Graph, &graph); err != nil {
		t.Fatal(err)
	}
	return &graph
}

func conductorGate(t *testing.T, f fixture, delivery string) gatesReportDetail {
	t.Helper()
	var found []gatesReportDetail
	for _, record := range readJournal(t, f, delivery) {
		if record.Kind != KindGates || record.TaskID != "task_1" {
			continue
		}
		var detail gatesReportDetail
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Source == "conductor" {
			found = append(found, detail)
		}
	}
	if len(found) != 1 {
		t.Fatalf("conductor gates = %d, want 1", len(found))
	}
	return found[0]
}

func TestConductorRecoverIntegrates(t *testing.T) {
	f, opts, delivery, _, ask := parkedRecovery(t, "continuation-ask-untracked")
	if err := Recover(context.Background(), opts, delivery, "task_1"); err != nil {
		t.Fatalf("Recover() = %v", err)
	}
	if report := conductorGate(t, f, delivery); !report.Passed {
		t.Fatalf("conductor gates = %+v", report)
	}
	if _, err := os.Stat(ask); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ask remains: %v", err)
	}
	graph := recoveryGraph(t, f, delivery)
	if task := graphTask(graph, "task_1"); task == nil || task.State != routing.GraphTaskCandidate || len(task.Attempts) != 1 || task.Attempts[0].Question == nil {
		t.Fatalf("recovered task = %+v", task)
	}
	var candidateCount int
	for _, record := range readJournal(t, f, delivery) {
		if record.Kind == KindCandidate && record.TaskID == "task_1" {
			candidateCount++
			var detail struct {
				Source string `json:"source"`
			}
			if err := json.Unmarshal(record.Detail, &detail); err != nil || detail.Source != "conductor" {
				t.Fatalf("candidate source = %q, %v", detail.Source, err)
			}
		}
	}
	if candidateCount != 1 {
		t.Fatalf("candidate records = %d", candidateCount)
	}
	r, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Resume.Run() = %s, %v", state, err)
	}
	if got := f.run(t, "show", "HEAD:out/1.txt"); got != "ok" {
		t.Fatalf("integrated greeting = %q", got)
	}
}

func TestConductorRecoverGateFailureKeepsWaiting(t *testing.T) {
	f, opts, delivery, wt, ask := parkedRecovery(t, "continuation-ask-broken")
	before := recoveryGraph(t, f, delivery)
	head := f.run(t, "-C", wt, "rev-parse", "HEAD")
	err := Recover(context.Background(), opts, delivery, "task_1")
	var failure *GateFailureError
	if !errors.As(err, &failure) || !strings.Contains(err.Error(), "broken files") {
		t.Fatalf("Recover() = %v, want gate failure with tests.sh output", err)
	}
	report := conductorGate(t, f, delivery)
	if report.Passed || !strings.Contains(strings.Join(report.Failures(), "\n"), "broken files") {
		t.Fatalf("conductor gates = %+v", report)
	}
	if got := f.run(t, "-C", wt, "rev-parse", "HEAD"); got != head {
		t.Fatalf("worktree HEAD = %s, want %s", got, head)
	}
	if _, err := os.Stat(ask); err != nil {
		t.Fatal(err)
	}
	a, b := graphTask(before, "task_1"), graphTask(recoveryGraph(t, f, delivery), "task_1")
	if b.State != routing.GraphTaskWaitingInput || len(b.Attempts) != len(a.Attempts) || b.Attempts[0].Question == nil || b.Attempts[0].Question.Prompt != a.Attempts[0].Question.Prompt {
		t.Fatalf("task changed after gate failure: before %+v, after %+v", a, b)
	}
}

func TestConductorRecoverSilentTree(t *testing.T) {
	f, opts, delivery, wt, ask := parkedRecovery(t, "ask")
	head := f.run(t, "-C", wt, "rev-parse", "HEAD")
	err := Recover(context.Background(), opts, delivery, "task_1")
	var failure *GateFailureError
	if !errors.As(err, &failure) || !strings.Contains(err.Error(), "nothing to recover") {
		t.Fatalf("Recover() = %v", err)
	}
	if report := conductorGate(t, f, delivery); report.Passed || !strings.Contains(report.Tree.Signal, "nothing to recover") {
		t.Fatalf("conductor gates = %+v", report)
	}
	if got := f.run(t, "-C", wt, "rev-parse", "HEAD"); got != head {
		t.Fatalf("worktree HEAD = %s, want %s", got, head)
	}
	if _, err := os.Stat(ask); err != nil {
		t.Fatal(err)
	}
	if task := graphTask(recoveryGraph(t, f, delivery), "task_1"); task.State != routing.GraphTaskWaitingInput || len(task.Attempts) != 1 {
		t.Fatalf("task changed: %+v", task)
	}
}

func TestConductorRecoverRefusals(t *testing.T) {
	cases := []struct {
		name, task, want string
		change           func(*testing.T, fixture, *Options, string, string)
	}{
		{"live owner", "task_1", "owned", func(t *testing.T, f fixture, opts *Options, delivery, wt string) {
			writePresenceFixture(t, f.root, delivery, opts.Now())
		}},
		{"unknown delivery", "task_1", "unknown", func(t *testing.T, f fixture, opts *Options, delivery, wt string) { opts.Resume = "absent" }},
		{"unknown task", "task_99", "task_99", nil},
		{"integrated task", "task_2", "already integrated", nil},
		{"missing worktree", "task_1", "worktree", func(t *testing.T, f fixture, opts *Options, delivery, wt string) {
			if err := os.RemoveAll(wt); err != nil {
				t.Fatal(err)
			}
		}},
		{"changed task set", "task_1", "digest", func(t *testing.T, f fixture, opts *Options, delivery, wt string) {
			path := filepath.Join(f.root, ".batuta", "plans", "greetings.md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte("- [ ] 3. Add greeting three"), []byte("- [ ] 3. Add other greeting three"), 1)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong branch", "task_1", "branch", func(t *testing.T, f fixture, opts *Options, delivery, wt string) {
			f.run(t, "checkout", "-q", "-b", "other")
		}},
		{"terminal delivery", "task_1", "already ended", func(t *testing.T, f fixture, opts *Options, delivery, wt string) {
			opts.KeepWorktrees = true
			if _, err := Abandon(context.Background(), *opts); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, opts, delivery, wt, _ := parkedRecovery(t, "continuation-ask-untracked")
			if tc.change != nil {
				tc.change(t, f, &opts, delivery, wt)
			}
			selected := delivery
			if tc.name == "unknown delivery" {
				selected = opts.Resume
			}
			journalPath := filepath.Join(f.root, journal.Dir, delivery+".jsonl")
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			err = Recover(context.Background(), opts, selected, tc.task)
			var failure *GateFailureError
			if err == nil || errors.As(err, &failure) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Recover() = %v, want refusal containing %q", err, tc.want)
			}
			after, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("refusal modified journal")
			}
		})
	}
}

func journalBytes(t *testing.T, f fixture, delivery string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, journal.Dir, delivery+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestConductorRecoverIsIdempotent(t *testing.T) {
	f, opts, delivery, wt, _ := parkedRecovery(t, "continuation-ask-untracked")
	if err := Recover(context.Background(), opts, delivery, "task_1"); err != nil {
		t.Fatalf("Recover() = %v", err)
	}
	var commit string
	candidates := 0
	for _, record := range readJournal(t, f, delivery) {
		if record.Kind == KindCandidate && record.TaskID == "task_1" {
			candidates++
			var detail struct {
				Commit string `json:"commit"`
			}
			if err := json.Unmarshal(record.Detail, &detail); err != nil || detail.Commit == "" {
				t.Fatalf("candidate detail = %s, %v", record.Detail, err)
			}
			commit = detail.Commit
		}
	}
	if candidates != 1 {
		t.Fatalf("candidate records = %d, want 1", candidates)
	}
	before := journalBytes(t, f, delivery)
	head := f.run(t, "-C", wt, "rev-parse", "HEAD")
	err := Recover(context.Background(), opts, delivery, "task_1")
	var failure *GateFailureError
	if err == nil || errors.As(err, &failure) || !strings.Contains(err.Error(), "not waiting_input") {
		t.Fatalf("second Recover() = %v, want refusal", err)
	}
	if !bytes.Equal(before, journalBytes(t, f, delivery)) {
		t.Fatal("second Recover modified journal")
	}
	if got := f.run(t, "-C", wt, "rev-parse", "HEAD"); got != head {
		t.Fatalf("worktree HEAD = %s, want %s", got, head)
	}
	if got := f.run(t, "-C", wt, "cat-file", "-t", commit); got != "commit" {
		t.Fatalf("candidate commit %s is %q", commit, got)
	}
}

func TestConductorRecoverRefusesLiveAttempts(t *testing.T) {
	cases := []struct {
		name    string
		advance func(*testing.T, *routing.DeliveryGraph)
	}{
		{"preparing", func(*testing.T, *routing.DeliveryGraph) {}},
		{"running", func(t *testing.T, graph *routing.DeliveryGraph) {
			digest := "sha256:" + strings.Repeat("a", 64)
			intent := routing.TaskWorktreeIntent{OperationID: digest, RequestDigest: digest, Name: "wt-task-3", Branch: "batuta/task-3"}
			if _, err := graph.PlanWorktree("task_3", 1, intent); err != nil {
				t.Fatal(err)
			}
			if _, err := graph.AttachWorktree("task_3", 1, routing.GraphWorktree{ID: "wt-task-3", Root: tempDir(t), Ready: true}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, opts, delivery, _, _ := parkedRecovery(t, "continuation-ask-untracked")
			records := readJournal(t, f, delivery)
			var opened openedDetail
			if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
				t.Fatal(err)
			}
			var graph routing.DeliveryGraph
			if err := json.Unmarshal(records[len(records)-1].Graph, &graph); err != nil {
				t.Fatal(err)
			}
			// task_3 waits on the parked task_1: free it so the graph's own
			// admission can start it without a runner.
			pending := graphTask(&graph, "task_3")
			if pending == nil || pending.State != routing.GraphTaskPending {
				t.Fatalf("task_3 = %+v, want pending", pending)
			}
			pending.Dependencies = []string{}
			head := f.run(t, "rev-parse", "HEAD")
			wave, err := graph.AdmitReadyWave(routing.ReadyWaveInput{IntegrationHeadSHA: head, RemainingSlots: 1, ReachableCommits: map[string]bool{head: true}})
			if err != nil || len(wave.TaskIDs) != 1 || wave.TaskIDs[0] != "task_3" {
				t.Fatalf("AdmitReadyWave() = %+v, %v", wave, err)
			}
			if err := graph.BeginWaveAttempts(wave.Number, opened.Generation); err != nil {
				t.Fatal(err)
			}
			tc.advance(t, &graph)
			raw, err := json.Marshal(graph)
			if err != nil {
				t.Fatal(err)
			}
			detail, err := json.Marshal(map[string]any{"wave": wave.Number})
			if err != nil {
				t.Fatal(err)
			}
			store, err := journal.Open(f.root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append(delivery, journal.Record{Kind: KindAttempts, Detail: detail, Graph: raw, At: opts.Now()}); err != nil {
				t.Fatal(err)
			}
			before := journalBytes(t, f, delivery)
			err = Recover(context.Background(), opts, delivery, "task_1")
			var failure *GateFailureError
			if err == nil || errors.As(err, &failure) || !strings.Contains(err.Error(), "task_3") {
				t.Fatalf("Recover() = %v, want refusal naming task_3", err)
			}
			if !bytes.Equal(before, journalBytes(t, f, delivery)) {
				t.Fatal("refusal modified journal")
			}
			for _, record := range readJournal(t, f, delivery) {
				if record.Kind == KindFailure && record.TaskID == "task_3" {
					t.Fatalf("failure recorded for the live task: %s", record.Detail)
				}
			}
		})
	}
}

func TestConductorRecoverAfterGateFailure(t *testing.T) {
	f, opts, delivery, wt, ask := parkedRecovery(t, "continuation-ask-broken")
	err := Recover(context.Background(), opts, delivery, "task_1")
	var failure *GateFailureError
	if !errors.As(err, &failure) {
		t.Fatalf("first Recover() = %v, want gate failure", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "out", "1.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Recover(context.Background(), opts, delivery, "task_1"); err != nil {
		t.Fatalf("second Recover() = %v", err)
	}
	var reports []gatesReportDetail
	candidates := 0
	for _, record := range readJournal(t, f, delivery) {
		if record.TaskID != "task_1" {
			continue
		}
		switch record.Kind {
		case KindGates:
			var detail gatesReportDetail
			if err := json.Unmarshal(record.Detail, &detail); err != nil {
				t.Fatal(err)
			}
			if detail.Source == "conductor" {
				reports = append(reports, detail)
			}
		case KindCandidate:
			candidates++
		}
	}
	if len(reports) != 2 || reports[0].Passed || !reports[1].Passed || candidates != 1 {
		t.Fatalf("conductor gates %+v, candidates %d", reports, candidates)
	}
	if task := graphTask(recoveryGraph(t, f, delivery), "task_1"); task == nil || task.State != routing.GraphTaskCandidate {
		t.Fatalf("recovered task = %+v", task)
	}
	if _, err := os.Stat(ask); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ask remains: %v", err)
	}
}
