package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/gates"
)

// oneMediumTaskPlan swaps the default plan for a single medium task with the
// given title, Scope line and Accept line, whose criteria may carry proofs.
func oneMediumTaskPlan(t *testing.T, f fixture, title, scope, accept string) {
	t.Helper()
	plan := "# Plan — Greetings\n\n**Goal:** Greeting.\n**Created:** 2026-09-06 · **Status:** approved\n\n## Tasks\n- [ ] 1. " + title + "\n      Scope: " + scope + "\n      Accept: " + accept + "\n"
	if err := os.WriteFile(filepath.Join(f.root, ".batuta", "plans", "greetings.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", "-A")
	f.run(t, "commit", "-q", "-m", "test: one medium task")
}

// mediumTaskPlan swaps the default plan for a single medium task whose one
// criterion carries the given Accept line, with or without a proof.
func mediumTaskPlan(t *testing.T, f fixture, accept string) {
	t.Helper()
	oneMediumTaskPlan(t, f, "Add greeting one — backend/medium", "out/1.txt", accept)
}

func reportedGates(t *testing.T, f fixture, delivery string) gates.Report {
	t.Helper()
	var report gates.Report
	found := false
	for _, record := range readJournal(t, f, delivery) {
		if record.Kind != KindGates {
			continue
		}
		if err := json.Unmarshal(record.Detail, &report); err != nil {
			t.Fatal(err)
		}
		found = true
	}
	if !found {
		t.Fatal("no gates report in the journal")
	}
	return report
}

func runOneMediumTask(t *testing.T, f fixture, accept string) (*Runner, string) {
	t.Helper()
	return runOneMediumTaskScenario(t, f, "default", "Add greeting one — backend/medium", "out/1.txt", accept)
}

// runOneMediumTaskScenario runs the one-medium-task plan under a fake
// executor scenario; the run ends done or blocked, per the attempt's gates.
func runOneMediumTaskScenario(t *testing.T, f fixture, scenario, title, scope, accept string) (*Runner, string) {
	t.Helper()
	oneMediumTaskPlan(t, f, title, scope, accept)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options(scenario, &out))
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.Run(context.Background())
	if err != nil || (state != StateDone && state != StateBlocked) {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	return r, out.String()
}

// gatesRecord is a gates_reported detail: the report plus the skip reason
// recorded when the verifier was not dispatched on a rejected attempt.
type gatesRecord struct {
	gates.Report
	VerifierSkipped string `json:"verifier_skipped"`
}

func gatesByExecution(t *testing.T, f fixture, delivery string) map[int]gatesRecord {
	t.Helper()
	reports := map[int]gatesRecord{}
	for _, record := range readJournal(t, f, delivery) {
		if record.Kind != KindGates {
			continue
		}
		var detail gatesRecord
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		reports[detail.Execution] = detail
	}
	return reports
}

func verifierDispatches(t *testing.T, f fixture, delivery string) map[int]int {
	t.Helper()
	counts := map[int]int{}
	for _, record := range readJournal(t, f, delivery) {
		if record.Kind != KindVerifierIntent {
			continue
		}
		var detail dispatchDetail
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		counts[detail.Execution]++
	}
	return counts
}

func TestProoflessCriterionDispatchesVerifier(t *testing.T) {
	t.Parallel()
	f := setup(t)
	r, out := runOneMediumTask(t, f, "greeting exists")
	records := readJournal(t, f, r.delivery)
	dispatches := 0
	for _, record := range records {
		if record.Kind == KindVerifierIntent {
			dispatches++
		}
	}
	report := reportedGates(t, f, r.delivery)
	if dispatches != 1 {
		t.Fatalf("verifier dispatches = %d\n%s", dispatches, out)
	}
	if report.Verifier == nil || !report.Verifier.Pass || !strings.Contains(report.Verifier.Signal, "1/1 DONE") {
		t.Fatalf("report lost the verifier verdict: %+v", report)
	}
}

func TestAllProvenCriteriaSkipVerifierOnMedium(t *testing.T) {
	t.Parallel()
	f := setup(t)
	r, out := runOneMediumTask(t, f, "greeting exists → test -f out/1.txt")
	for _, record := range readJournal(t, f, r.delivery) {
		if record.Kind == KindVerifierIntent || record.Kind == KindVerifierResult {
			t.Fatalf("verifier ran on an all-proven medium attempt: %s\n%s", record.Kind, out)
		}
	}
	report := reportedGates(t, f, r.delivery)
	if report.Verifier != nil {
		t.Fatalf("verifier verdict recorded: %+v", report)
	}
	if !report.Passed {
		t.Fatalf("report failed: %+v", report)
	}
}

func TestRejectedAttemptSkipsVerifier(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		title    string
		scope    string
		scenario string
		accept   string
		wantPass bool
		skipped  string
	}{
		{"tests gate", "Add greeting one — backend/medium", "out/1.txt", "always-broken", "greeting exists", false, "tests"},
		{"scope gate", "Add greeting two — backend/medium", "out/2.txt", "fail-scope", "greeting exists", false, "scope"},
		{"proof gate", "Add greeting one — backend/medium", "out/1.txt", "default", "greeting exists -> test -f out/nope.txt; greeting also", false, "proof 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			r, out := runOneMediumTaskScenario(t, f, tc.scenario, tc.title, tc.scope, tc.accept)
			reports := gatesByExecution(t, f, r.delivery)
			first, found := reports[1]
			if !found {
				t.Fatalf("no gates report for execution 1\n%s", out)
			}
			if first.Verifier != nil {
				t.Fatalf("report kept a verifier verdict: %+v", first.Report)
			}
			if first.VerifierSkipped != tc.skipped {
				t.Fatalf("verifier_skipped = %q, want %q\n%s", first.VerifierSkipped, tc.skipped, out)
			}
			if first.Passed != tc.wantPass {
				t.Fatalf("report passed = %v\n%+v", first.Passed, first.Report)
			}
			if dispatches := verifierDispatches(t, f, r.delivery); dispatches[1] != 0 {
				t.Fatalf("verifier dispatched on the rejected attempt: %v\n%s", dispatches, out)
			}
		})
	}
}

func TestRejectedAttemptKeepsOutcome(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		scenario string
		accept   string
		blocker  string
		gate     string
	}{
		{"tests gate", "always-broken", "greeting exists", blockerTestsFailed, "gate tests:"},
		{"proof gate", "default", "greeting exists -> test -f out/nope.txt; greeting also", blockerProof, "gate proof 1:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			r, out := runOneMediumTaskScenario(t, f, tc.scenario, "Add greeting one — backend/medium", "out/1.txt", tc.accept)
			var failure string
			for _, record := range readJournal(t, f, r.delivery) {
				if record.Kind == KindFailure && record.TaskID == "task_1" && strings.Contains(string(record.Detail), `"execution":1`) {
					failure = string(record.Detail)
					break
				}
			}
			if failure == "" {
				t.Fatalf("no failure_recorded for execution 1\n%s", out)
			}
			if !strings.Contains(failure, `"blocker":"`+tc.blocker+`"`) {
				t.Fatalf("blocker changed: %s", failure)
			}
			if !strings.Contains(failure, tc.gate) {
				t.Fatalf("retry feedback lost the failing gate line: %s", failure)
			}
			if strings.Contains(strings.ToLower(failure), "verifier") {
				t.Fatalf("retry feedback grew verifier lines: %s", failure)
			}
			if !strings.Contains(failure, `"same_runtime":true`) || !strings.Contains(failure, `"reuse_worktree":true`) {
				t.Fatalf("retry policy changed: %s", failure)
			}
		})
	}
}
