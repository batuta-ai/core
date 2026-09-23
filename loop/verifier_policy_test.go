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

// mediumTaskPlan swaps the default plan for a single medium task whose one
// criterion carries the given Accept line, with or without a proof.
func mediumTaskPlan(t *testing.T, f fixture, accept string) {
	t.Helper()
	plan := "# Plan — Greetings\n\n**Goal:** Greeting.\n**Created:** 2026-09-06 · **Status:** approved\n\n## Tasks\n- [ ] 1. Add greeting one — backend/medium\n      Scope: out/1.txt\n      Accept: " + accept + "\n"
	if err := os.WriteFile(filepath.Join(f.root, ".batuta", "plans", "greetings.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", "-A")
	f.run(t, "commit", "-q", "-m", "test: one medium task")
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
	mediumTaskPlan(t, f, accept)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	return r, out.String()
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
