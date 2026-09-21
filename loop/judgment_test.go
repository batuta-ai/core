package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

func TestBuildClaimEvidenceState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 20, 15, 4, 5, 0, time.UTC)
	input := ClaimEvidenceInput{
		Task: routing.PlanTask{
			TaskArtifact: routing.TaskArtifact{ID: "task_1", Title: "Wire the claim check"},
			Scope:        []string{"loop/", "cmd/batuta/main.go"},
		},
		Criteria: []gates.Criterion{
			{Text: "state is bounded", Proof: "go test ./loop -run TestBuildClaimEvidenceState"},
			{Text: "questions are noul"},
		},
		Report: gates.Report{
			Passed:   false,
			Finished: gates.Verdict{Name: "finished", Pass: true, Signal: "exit 0"},
			Tree:     gates.Verdict{Name: "tree", Pass: true, Signal: "the worktree differs from the attempt's base"},
			Tests:    gates.Verdict{Name: "tests", Pass: true, Signal: "go test ./... passed"},
			Scope:    gates.Verdict{Name: "scope", Pass: true, Signal: "in scope"},
			Proofs: []gates.Verdict{
				{Name: "proof 1", Pass: true, Signal: "state is bounded — `go test ./loop -run TestBuildClaimEvidenceState` passed"},
				{Name: "proof 2", Pass: false, Signal: "questions are noul — no proof command; left to the verifier"},
			},
			Verifier: &gates.Verdict{Name: "verifier", Pass: false, Signal: "1 criterion(s) INCOMPLETE", Detail: "TASK 2: INCOMPLETE — questions missing"},
		},
		OutputTail: strings.Join([]string{
			"BATUTA-PROGRESS 1 START",
			"edited loop/judgment.go",
			"BATUTA-PROGRESS 1 DONE",
			"TASK 1: DONE",
			"TASK 2: INCOMPLETE — still writing questions",
		}, "\n"),
		Progress: []executor.ProgressEvent{
			{Criterion: 1, State: "START", At: now},
			{Criterion: 1, State: "DONE", At: now.Add(time.Second)},
		},
		ChangedPaths: []string{"loop/judgment.go", "loop/judgment_test.go"},
		TreeChanged:  true,
	}

	state, err := BuildClaimEvidenceState(input, 16<<10)
	if err != nil {
		t.Fatalf("BuildClaimEvidenceState() error = %v", err)
	}
	got := marshalState(t, state)
	if budget, err := json.Marshal(state); err != nil {
		t.Fatal(err)
	} else if len(budget) > 16<<10 {
		t.Fatalf("state is %d bytes, exceeds the 16 KiB budget", len(budget))
	}

	task, _ := got["task"].(map[string]any)
	if task["id"] != "task_1" || task["title"] != "Wire the claim check" {
		t.Fatalf("task = %#v", task)
	}
	scope, _ := task["scope"].([]any)
	if len(scope) != 2 || scope[0] != "loop/" || scope[1] != "cmd/batuta/main.go" {
		t.Fatalf("task.scope = %#v", task["scope"])
	}

	criteria, _ := got["criteria"].([]any)
	if len(criteria) != 2 {
		t.Fatalf("criteria = %#v", got["criteria"])
	}
	first, _ := criteria[0].(map[string]any)
	if first["index"] != float64(1) || first["text"] != "state is bounded" || first["proof"] != "go test ./loop -run TestBuildClaimEvidenceState" {
		t.Fatalf("criteria[0] = %#v", first)
	}
	if first["pass"] != true {
		t.Fatalf("criteria[0].pass = %#v, want true", first["pass"])
	}
	if first["signal"] != "state is bounded — `go test ./loop -run TestBuildClaimEvidenceState` passed" {
		t.Fatalf("criteria[0].signal = %#v", first["signal"])
	}
	second, _ := criteria[1].(map[string]any)
	if second["index"] != float64(2) || second["proof"] != nil && second["proof"] != "" {
		t.Fatalf("criteria[1] = %#v", second)
	}
	if second["pass"] != false {
		t.Fatalf("criteria[1].pass = %#v, want false", second["pass"])
	}

	report, _ := got["executor_report"].(string)
	for _, want := range []string{
		"BATUTA-PROGRESS 1 START",
		"BATUTA-PROGRESS 1 DONE",
		"TASK 1: DONE",
		"TASK 2: INCOMPLETE — still writing questions",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("executor_report missing %q:\n%s", want, report)
		}
	}

	progress, _ := got["progress"].([]any)
	if len(progress) != 2 {
		t.Fatalf("progress = %#v", got["progress"])
	}
	start, _ := progress[0].(map[string]any)
	if start["criterion"] != float64(1) || start["state"] != "START" {
		t.Fatalf("progress[0] = %#v", start)
	}

	tree, _ := got["tree"].(map[string]any)
	if tree["changed"] != true && tree["status"] != "changed" {
		t.Fatalf("tree = %#v, want changed", tree)
	}
	paths, _ := tree["changed_paths"].([]any)
	if len(paths) != 2 || paths[0] != "loop/judgment.go" {
		t.Fatalf("changed_paths = %#v", tree["changed_paths"])
	}

	verifier, _ := got["verifier"].(map[string]any)
	if verifier["signal"] != "1 criterion(s) INCOMPLETE" || verifier["detail"] != "TASK 2: INCOMPLETE — questions missing" {
		t.Fatalf("verifier = %#v", verifier)
	}

	outcome, _ := got["outcome"].(map[string]any)
	if outcome["passed"] != false {
		t.Fatalf("outcome.passed = %#v", outcome["passed"])
	}
	failing, _ := outcome["failing_gates"].([]any)
	if !containsAll(failing, "proof 2", "verifier") {
		t.Fatalf("failing_gates = %#v", failing)
	}

	t.Run("last sixty lines and eight kib cap keep protocol lines whole", func(t *testing.T) {
		t.Parallel()
		var lines []string
		for i := 0; i < 80; i++ {
			lines = append(lines, strings.Repeat("x", 200))
		}
		protected := "BATUTA-PROGRESS 3 DONE"
		taskLine := "TASK 3: DONE"
		lines = append(lines, protected, taskLine)
		state, err := BuildClaimEvidenceState(ClaimEvidenceInput{
			Task:         routing.PlanTask{TaskArtifact: routing.TaskArtifact{ID: "task_2", Title: "cap"}},
			OutputTail:   strings.Join(lines, "\n"),
			ChangedPaths: []string{"a.go"},
			TreeChanged:  true,
		}, 32<<10)
		if err != nil {
			t.Fatalf("BuildClaimEvidenceState() error = %v", err)
		}
		body := marshalState(t, state)
		report, _ := body["executor_report"].(string)
		if strings.Count(report, "\n")+1 > 60 {
			t.Fatalf("executor_report has %d lines, want at most 60", strings.Count(report, "\n")+1)
		}
		if len(report) > 8<<10+len(protected)+len(taskLine)+2 {
			t.Fatalf("executor_report is %d bytes, want around 8 KiB", len(report))
		}
		if !strings.Contains(report, protected) || !strings.Contains(report, taskLine) {
			t.Fatalf("executor_report dropped a protocol line:\n%s", report)
		}
		if strings.Contains(report, "BATUTA-PROG") && !strings.Contains(report, protected) {
			t.Fatalf("BATUTA-PROGRESS line was split:\n%s", report)
		}
	})

	t.Run("trims executor_report then changed_paths to fit maxBytes", func(t *testing.T) {
		t.Parallel()
		filler := strings.Repeat("changed a very long output line that should be trimmed\n", 40)
		state, err := BuildClaimEvidenceState(ClaimEvidenceInput{
			Task:         routing.PlanTask{TaskArtifact: routing.TaskArtifact{ID: "task_3", Title: "trim"}},
			OutputTail:   filler + "BATUTA-PROGRESS 1 DONE\n",
			ChangedPaths: []string{"one.go", "two.go", "three.go", "four.go"},
			TreeChanged:  true,
		}, 900)
		if err != nil {
			t.Fatalf("BuildClaimEvidenceState() error = %v", err)
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 900 {
			t.Fatalf("state is %d bytes, exceeds 900", len(encoded))
		}
		body := marshalState(t, state)
		report, _ := body["executor_report"].(string)
		if len(report) >= len(filler) {
			t.Fatalf("executor_report was not trimmed: %d bytes", len(report))
		}
		tree, _ := body["tree"].(map[string]any)
		paths, _ := tree["changed_paths"].([]any)
		if report != "" && len(paths) != 4 {
			t.Fatalf("changed_paths trimmed before executor_report was exhausted: report=%d paths=%#v", len(report), paths)
		}
	})

	t.Run("fails only when the skeleton exceeds the budget", func(t *testing.T) {
		t.Parallel()
		_, err := BuildClaimEvidenceState(ClaimEvidenceInput{
			Task: routing.PlanTask{TaskArtifact: routing.TaskArtifact{
				ID:    "task_4",
				Title: strings.Repeat("huge title ", 80),
			}},
			OutputTail:   "ok",
			ChangedPaths: []string{"a.go"},
		}, 200)
		if err == nil {
			t.Fatal("BuildClaimEvidenceState() error = nil, want skeleton overflow")
		}
	})

	t.Run("equal tree omits a change claim", func(t *testing.T) {
		t.Parallel()
		state, err := BuildClaimEvidenceState(ClaimEvidenceInput{
			Task:        routing.PlanTask{TaskArtifact: routing.TaskArtifact{ID: "task_5", Title: "silent"}},
			TreeChanged: false,
		}, 4<<10)
		if err != nil {
			t.Fatalf("BuildClaimEvidenceState() error = %v", err)
		}
		tree, _ := marshalState(t, state)["tree"].(map[string]any)
		if tree["changed"] == true || tree["status"] == "changed" {
			t.Fatalf("tree = %#v, want equal to base", tree)
		}
	})
}

func TestClaimEvidenceStateRedaction(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	inside := filepath.Join(root, "loop", "judgment.go")
	secretLine := "TYPESAFE_API_KEY=sk-test-not-a-real-secret"
	output := strings.Join([]string{
		"wrote " + inside,
		secretLine,
		"OPENROUTER_API_KEY=or-also-secret",
		"note=this lowercase assignment stays",
		"looked at /etc/hosts",
		"BATUTA-PROGRESS 1 DONE",
	}, "\n")

	state, err := BuildClaimEvidenceState(ClaimEvidenceInput{
		Workspace:  root,
		Task:       routing.PlanTask{TaskArtifact: routing.TaskArtifact{ID: "task_1", Title: "redact"}},
		OutputTail: output,
		Report: gates.Report{
			Verifier: &gates.Verdict{Name: "verifier", Signal: "read " + inside, Detail: "HOME=/root"},
		},
		ChangedPaths: []string{inside, filepath.Join(root, "cmd", "batuta", "main.go")},
		TreeChanged:  true,
	}, 16<<10)
	if err != nil {
		t.Fatalf("BuildClaimEvidenceState() error = %v", err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, root) {
		t.Fatalf("state still carries the workspace root %q:\n%s", root, text)
	}
	if strings.Contains(text, "sk-test-not-a-real-secret") || strings.Contains(text, "or-also-secret") {
		t.Fatalf("state still carries an environment value:\n%s", text)
	}
	if strings.Contains(text, "TYPESAFE_API_KEY=") || strings.Contains(text, "OPENROUTER_API_KEY=") {
		t.Fatalf("state still carries a KEY=value line:\n%s", text)
	}
	if strings.Contains(text, "HOME=/root") {
		t.Fatalf("state still carries an environment assignment in verifier detail:\n%s", text)
	}
	if strings.Contains(text, "/etc/hosts") {
		t.Fatalf("state still carries an absolute path outside the repository:\n%s", text)
	}
	body := marshalState(t, state)
	report, _ := body["executor_report"].(string)
	if !strings.Contains(report, "loop/judgment.go") {
		t.Fatalf("workspace path was not made repository-relative:\n%s", report)
	}
	if !strings.Contains(report, "note=this lowercase assignment stays") {
		t.Fatalf("lowercase assignment was dropped:\n%s", report)
	}
	tree, _ := body["tree"].(map[string]any)
	paths, _ := tree["changed_paths"].([]any)
	if !containsAll(paths, "loop/judgment.go", "cmd/batuta/main.go") {
		t.Fatalf("changed_paths = %#v", paths)
	}
	for _, path := range paths {
		text, _ := path.(string)
		if filepath.IsAbs(text) {
			t.Fatalf("changed path is still absolute: %q", text)
		}
	}
}

func TestClaimEvidenceQuestions(t *testing.T) {
	t.Parallel()

	questions := ClaimEvidenceQuestions()
	if len(questions) != 2 {
		t.Fatalf("ClaimEvidenceQuestions() returned %d questions, want 2", len(questions))
	}
	unsupported, ok := questions["claim_unsupported"]
	if !ok {
		t.Fatal("missing claim_unsupported")
	}
	contradicted, ok := questions["verifier_contradicted"]
	if !ok {
		t.Fatal("missing verifier_contradicted")
	}
	if unsupported.Type != judge.QuestionNoul || contradicted.Type != judge.QuestionNoul {
		t.Fatalf("types = %q, %q, want noul", unsupported.Type, contradicted.Type)
	}
	wantUnsupported := "The executor's report claims work (files changed, tests passed, criteria done) that the tree, proof and verifier evidence in this state does not show."
	if unsupported.Instructions != wantUnsupported {
		t.Fatalf("claim_unsupported instructions = %q", unsupported.Instructions)
	}
	wantContradicted := "The verifier's DONE lines are contradicted by a failed proof or by the executor's own report."
	if contradicted.Instructions != wantContradicted {
		t.Fatalf("verifier_contradicted instructions = %q", contradicted.Instructions)
	}
	if got := criteriaMap(t, unsupported.Criteria); got["true"] != "a claim in executor_report is contradicted by tree, proofs or verifier" ||
		got["false"] != "every claim in executor_report is consistent with the evidence" {
		t.Fatalf("claim_unsupported criteria = %#v", got)
	}
	if got := criteriaMap(t, contradicted.Criteria); got["true"] != "a verifier DONE line is contradicted by a failed proof or by executor_report" ||
		got["false"] != "every verifier DONE line is consistent with the proofs and executor_report" {
		t.Fatalf("verifier_contradicted criteria = %#v", got)
	}

	again := ClaimEvidenceQuestions()
	if again["claim_unsupported"].Instructions != unsupported.Instructions || again["verifier_contradicted"].Instructions != contradicted.Instructions {
		t.Fatal("ClaimEvidenceQuestions() instructions were not stable across calls")
	}
}

func TestClaimEvidenceRequestV2(t *testing.T) {
	t.Parallel()

	input := ClaimEvidenceInput{
		Task: routing.PlanTask{
			TaskArtifact: routing.TaskArtifact{ID: "task_1", Title: "Wire v2 claims"},
			Scope:        []string{"loop/", "docs/judge.md"},
		},
		Report: gates.Report{
			Passed: false,
			Tests:  gates.Verdict{Name: "tests", Pass: false, Signal: "failed"},
		},
	}
	claims := []Claim{
		{
			Kind: ClaimKindPath, Text: "loop/claims.go", Line: "edited `loop/claims.go`",
			Path: "loop/claims.go", Status: ClaimStatusSupported, Source: ClaimSourceCode,
			Evidence: "in changed_paths",
		},
		{
			Kind: ClaimKindCommit, Text: "committed 1a2b3c4", Line: "committed 1a2b3c4 on the feature branch",
			Status: ClaimStatusUnsettled, Evidence: "tree unchanged; commit not verified by code",
		},
		{
			Kind: ClaimKindCriterion, Text: "questions are noul", Line: "criterion 2 passed",
			Criterion: 2, Status: ClaimStatusUnsettled, Evidence: "no proof verdict; no verifier line",
		},
	}

	req := BuildClaimEvidenceRequest(input, claims)
	if req.Decision != claimEvidenceDecision {
		t.Fatalf("Decision = %q, want %s", req.Decision, claimEvidenceDecision)
	}

	body := marshalState(t, req.State)
	if body["task_id"] != "task_1" || body["title"] != "Wire v2 claims" {
		t.Fatalf("state task = %#v", body)
	}
	scope, _ := body["scope"].([]any)
	if len(scope) != 2 || scope[0] != "loop/" || scope[1] != "docs/judge.md" {
		t.Fatalf("state.scope = %#v", body["scope"])
	}
	outcomeGates, _ := body["outcome_gates"].([]any)
	if !containsAll(outcomeGates, "tests") {
		t.Fatalf("outcome_gates = %#v, want tests", body["outcome_gates"])
	}
	for _, leaked := range []string{"criteria", "executor_report", "progress", "tree", "verifier", "outcome"} {
		if _, ok := body[leaked]; ok {
			t.Fatalf("v2 state still carries %q: %#v", leaked, body[leaked])
		}
	}

	if len(req.Questions) != 2 {
		t.Fatalf("Questions = %#v, want one per unsettled claim", req.Questions)
	}
	if _, ok := req.Questions["claim_1"]; ok {
		t.Fatal("settled claim_1 was sent to the judge")
	}
	for _, key := range []string{"claim_2", "claim_3"} {
		question, ok := req.Questions[key]
		if !ok {
			t.Fatalf("missing %s", key)
		}
		if question.Type != judge.QuestionChoice {
			t.Fatalf("%s type = %q, want choice", key, question.Type)
		}
		inst := stringMap(t, question.Instructions)
		if inst["question"] != "How does the evidence relate to the claim?" {
			t.Fatalf("%s question = %q", key, inst["question"])
		}
		if inst["claim"] == "" || inst["evidence"] == "" {
			t.Fatalf("%s instructions missing claim or evidence: %#v", key, inst)
		}
		got := stringMap(t, question.Criteria)
		if got["supported"] != "the evidence states or directly implies the claim" ||
			got["contradicted"] != "the evidence shows the claim is false" ||
			got["unverifiable"] != "the evidence says nothing about the claim" {
			t.Fatalf("%s criteria = %#v", key, got)
		}
	}
	if stringMap(t, req.Questions["claim_2"].Instructions)["claim"] != "committed 1a2b3c4" {
		t.Fatalf("claim_2 claim = %#v", req.Questions["claim_2"].Instructions)
	}
	if stringMap(t, req.Questions["claim_3"].Instructions)["claim"] != "questions are noul" {
		t.Fatalf("claim_3 claim = %#v", req.Questions["claim_3"].Instructions)
	}
	if stringMap(t, req.Questions["claim_2"].Instructions)["evidence"] != "tree unchanged; commit not verified by code" {
		t.Fatalf("claim_2 evidence = %#v", req.Questions["claim_2"].Instructions)
	}
}

func TestClaimEvidenceNoCallWhenSettled(t *testing.T) {
	t.Parallel()

	t.Run("supported claims skip the judge and stay passing", func(t *testing.T) {
		t.Parallel()
		fake := &fakeLoopJudge{choice: "contradicted", confidence: 0.99}
		r := newJudgmentRunner(t, fake, judge.ModeEnforce, 0.9)
		report := passingClaimReport()
		result := executor.Result{Stdout: []byte("BATUTA-PROGRESS 1 DONE\nedited `out/1.txt`\n")}
		ac := judgmentAttempt("out/1.txt exists → test -f out/1.txt")
		got, err := r.judgeClaimEvidence(t.Context(), ac, &report, result, true, []string{"out/1.txt"})
		if err != nil {
			t.Fatalf("judgeClaimEvidence() error = %v", err)
		}
		if fake.Asks() != 0 {
			t.Fatalf("asks = %d, want 0 when every claim is settled", fake.Asks())
		}
		if got.Asked {
			t.Fatal("Asked = true, want false")
		}
		if got.Flagged {
			t.Fatal("supported claims flagged the attempt")
		}
		if !report.Passed {
			t.Fatal("enforce failed a passing attempt with only supported claims")
		}
		if len(got.Claims) != 2 {
			t.Fatalf("claims = %#v, want the two settled claims", got.Claims)
		}
		for _, claim := range got.Claims {
			if claim.Source != string(ClaimSourceCode) || claim.Choice != string(ClaimStatusSupported) {
				t.Fatalf("claim = %#v, want code/supported", claim)
			}
		}
	})

	t.Run("code-contradicted claims skip the judge and flag", func(t *testing.T) {
		t.Parallel()
		fake := &fakeLoopJudge{choice: "supported", confidence: 0.99}
		r := newJudgmentRunner(t, fake, judge.ModeEnforce, 0.9)
		report := passingClaimReport()
		result := executor.Result{Stdout: []byte("edited `docs/missing.md`\n")}
		ac := judgmentAttempt("out/1.txt exists → test -f out/1.txt")
		ac.plan.Scope = []string{"docs/"}
		got, err := r.judgeClaimEvidence(t.Context(), ac, &report, result, true, []string{"out/1.txt"})
		if err != nil {
			t.Fatalf("judgeClaimEvidence() error = %v", err)
		}
		if fake.Asks() != 0 {
			t.Fatalf("asks = %d, want 0 when code settles every claim", fake.Asks())
		}
		if !got.Flagged {
			t.Fatal("code-contradicted claim did not flag")
		}
		if report.Passed {
			t.Fatal("enforce left a passing attempt after a code contradiction")
		}
		if !failingJudge(report) {
			t.Fatal("enforce did not append a failing judge proof")
		}
		joined := strings.Join(report.Failures(), "\n")
		if !strings.Contains(joined, "docs/missing.md") || !strings.Contains(joined, "edited `docs/missing.md`") {
			t.Fatalf("judge proof does not name the contradicted claim and report line:\n%s", joined)
		}
	})
}

func TestClaimEvidenceNoEmptyEvidence(t *testing.T) {
	t.Parallel()

	t.Run("sent questions always carry evidence", func(t *testing.T) {
		t.Parallel()
		rec := &claimQuestionRecorder{}
		r := newJudgmentRunner(t, rec, judge.ModeShadow, 0.9)
		report := passingClaimReport()
		report.Proofs = nil
		report.Verifier = nil
		result := executor.Result{Stdout: []byte("BATUTA-PROGRESS 1 DONE\ncommitted abcdef1 on the feature branch\n")}
		ac := judgmentAttempt("out/1.txt exists → test -f out/1.txt")
		got, err := r.judgeClaimEvidence(t.Context(), ac, &report, result, true, nil)
		if err != nil {
			t.Fatalf("judgeClaimEvidence() error = %v", err)
		}
		if rec.Asks() == 0 {
			t.Fatal("judge was not asked for unsettled claims with evidence")
		}
		if !got.Asked {
			t.Fatal("Asked = false, want true")
		}
		for key, question := range rec.Questions() {
			inst := stringMap(t, question.Instructions)
			if inst["evidence"] == "" {
				t.Fatalf("%s was sent with empty evidence: %#v", key, inst)
			}
		}
	})

	t.Run("empty evidence is unverifiable by code without a judge call", func(t *testing.T) {
		t.Parallel()
		rec := &claimQuestionRecorder{}
		claims := []Claim{{
			Text:   "the routing now matches the brief",
			Line:   "the routing now matches the brief",
			Status: ClaimStatusUnsettled,
		}}
		req := BuildClaimEvidenceRequest(ClaimEvidenceInput{
			Task: routing.PlanTask{TaskArtifact: routing.TaskArtifact{ID: "task_1"}},
		}, claims)
		if len(req.Questions) != 0 {
			t.Fatalf("Questions = %#v, want none", req.Questions)
		}
		if claims[0].Status != ClaimStatusUnverifiable || claims[0].Source != ClaimSourceCode {
			t.Fatalf("claim = %#v, want unverifiable by code", claims[0])
		}

		r := newJudgmentRunner(t, rec, judge.ModeShadow, 0.9)
		report := passingClaimReport()
		result := executor.Result{Stdout: []byte("the routing now matches the brief\n")}
		ac := judgmentAttempt("out/1.txt exists → test -f out/1.txt")
		got, err := r.judgeClaimEvidence(t.Context(), ac, &report, result, true, nil)
		if err != nil {
			t.Fatalf("judgeClaimEvidence() error = %v", err)
		}
		if rec.Asks() != 0 || got.Asked {
			t.Fatalf("asks = %d asked = %t, want no judge call", rec.Asks(), got.Asked)
		}
		flagged, records, uncertain := aggregateClaimEvidence(claims, nil, 0.9)
		if flagged || len(uncertain) != 0 {
			t.Fatalf("unverifiable flagged or uncertain: flagged=%t uncertain=%#v", flagged, uncertain)
		}
		if len(records) != 1 || records[0].Source != string(ClaimSourceCode) || records[0].Choice != string(ClaimStatusUnverifiable) {
			t.Fatalf("records = %#v, want code/unverifiable", records)
		}
	})
}

type claimQuestionRecorder struct {
	mu        sync.Mutex
	asks      int
	questions map[string]judge.Question
}

func (r *claimQuestionRecorder) Ask(_ context.Context, req judge.Request) (judge.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asks++
	r.questions = req.Questions
	answers := make(map[string]judge.Answer, len(req.Questions))
	for key := range req.Questions {
		answers[key] = judge.Answer{Type: judge.QuestionChoice, Choice: claimChoiceUnverifiable, Confidence: 0.4}
	}
	return judge.Response{Model: "jev-test", Answers: answers}, nil
}

func (r *claimQuestionRecorder) Asks() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asks
}

func (r *claimQuestionRecorder) Questions() map[string]judge.Question {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]judge.Question, len(r.questions))
	for key, question := range r.questions {
		out[key] = question
	}
	return out
}

func TestClaimEvidenceAggregation(t *testing.T) {
	t.Parallel()

	codeContradicted := Claim{
		Kind: ClaimKindPath, Text: "a.go", Line: "edited `a.go`",
		Status: ClaimStatusContradicted, Source: ClaimSourceCode,
	}
	unsettledCommit := Claim{
		Kind: ClaimKindCommit, Text: "committed", Line: "committed the greeting",
		Status: ClaimStatusUnsettled,
	}

	t.Run("code contradicted flags and is not uncertain", func(t *testing.T) {
		t.Parallel()
		flagged, records, uncertain := aggregateClaimEvidence([]Claim{codeContradicted}, nil, 0.9)
		if !flagged {
			t.Fatal("code contradicted did not flag")
		}
		if len(uncertain) != 0 {
			t.Fatalf("uncertain = %#v, want empty", uncertain)
		}
		if len(records) != 1 || records[0].Source != string(ClaimSourceCode) || records[0].Choice != string(ClaimStatusContradicted) {
			t.Fatalf("records = %#v", records)
		}
	})

	t.Run("judge contradicted at threshold flags", func(t *testing.T) {
		t.Parallel()
		answers := map[string]judge.Answer{
			"claim_1": {
				Type: judge.QuestionChoice, Choice: "contradicted", Confidence: 0.9,
				Probabilities: map[string]float64{"contradicted": 0.92, "supported": 0.05, "unverifiable": 0.03},
			},
		}
		flagged, records, uncertain := aggregateClaimEvidence([]Claim{unsettledCommit}, answers, 0.9)
		if !flagged {
			t.Fatal("judge contradicted at threshold did not flag")
		}
		if len(uncertain) != 0 {
			t.Fatalf("uncertain = %#v, want empty", uncertain)
		}
		if records[0].Source != string(ClaimSourceJudge) || records[0].Choice != "contradicted" || records[0].Confidence != 0.9 {
			t.Fatalf("records = %#v", records)
		}
	})

	t.Run("confidence below threshold is uncertain and never flags", func(t *testing.T) {
		t.Parallel()
		answers := map[string]judge.Answer{
			"claim_1": {
				Type: judge.QuestionChoice, Choice: "contradicted", Confidence: 0.5,
				Probabilities: map[string]float64{"contradicted": 0.8, "supported": 0.1, "unverifiable": 0.1},
			},
		}
		flagged, _, uncertain := aggregateClaimEvidence([]Claim{unsettledCommit}, answers, 0.9)
		if flagged {
			t.Fatal("low-confidence contradicted flagged")
		}
		if len(uncertain) != 1 || uncertain[0].Key != "claim_1" || uncertain[0].Choice != "contradicted" {
			t.Fatalf("uncertain = %#v", uncertain)
		}
	})

	t.Run("contradicted probability in 0.30-0.70 is uncertain and never flags", func(t *testing.T) {
		t.Parallel()
		answers := map[string]judge.Answer{
			"claim_1": {
				Type: judge.QuestionChoice, Choice: "contradicted", Confidence: 0.95,
				Probabilities: map[string]float64{"contradicted": 0.5, "supported": 0.3, "unverifiable": 0.2},
			},
		}
		flagged, _, uncertain := aggregateClaimEvidence([]Claim{unsettledCommit}, answers, 0.9)
		if flagged {
			t.Fatal("mid-probability contradicted flagged")
		}
		if len(uncertain) != 1 || uncertain[0].Contradicted != 0.5 {
			t.Fatalf("uncertain = %#v", uncertain)
		}
	})

	t.Run("code contradicted still flags beside an uncertain judge answer", func(t *testing.T) {
		t.Parallel()
		answers := map[string]judge.Answer{
			"claim_2": {
				Type: judge.QuestionChoice, Choice: "contradicted", Confidence: 0.4,
				Probabilities: map[string]float64{"contradicted": 0.8},
			},
		}
		flagged, _, uncertain := aggregateClaimEvidence([]Claim{codeContradicted, unsettledCommit}, answers, 0.9)
		if !flagged {
			t.Fatal("code contradicted did not flag when a judge answer was uncertain")
		}
		if len(uncertain) != 1 || uncertain[0].Key != "claim_2" {
			t.Fatalf("uncertain = %#v, want the judge answer only", uncertain)
		}
	})
}

func TestParseRunLog(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		log       string
		wantTail  string
		wantEvent []executor.ProgressEvent
	}{
		{
			name:     "both sections",
			log:      "## stdout\n\nline one\nBATUTA-PROGRESS 1 START\nBATUTA-PROGRESS 2 DONE\n\n## stderr\n\nwarned\n",
			wantTail: "line one\nBATUTA-PROGRESS 1 START\nBATUTA-PROGRESS 2 DONE\nwarned\n",
			wantEvent: []executor.ProgressEvent{
				{Criterion: 1, State: "START"},
				{Criterion: 2, State: "DONE"},
			},
		},
		{
			name:     "trailing newline on stdout is not doubled",
			log:      "## stdout\n\nhello\n\n\n## stderr\n\n",
			wantTail: "hello\n",
		},
		{
			name:     "empty stdout keeps stderr",
			log:      "## stdout\n\n\n\n## stderr\n\nhit a limit\n",
			wantTail: "hit a limit\n",
		},
		{
			name:     "both empty",
			log:      "## stdout\n\n\n\n## stderr\n\n",
			wantTail: "",
		},
		{
			name:      "not a run log falls back to the whole content",
			log:       "BATUTA-PROGRESS 1 DONE\n",
			wantTail:  "BATUTA-PROGRESS 1 DONE\n",
			wantEvent: []executor.ProgressEvent{{Criterion: 1, State: "DONE"}},
		},
		{
			name:     "progress lines from both streams in order",
			log:      "## stdout\n\nBATUTA-PROGRESS 1 START\n\n## stderr\n\nBATUTA-PROGRESS 1 DONE\n",
			wantTail: "BATUTA-PROGRESS 1 START\nBATUTA-PROGRESS 1 DONE\n",
			wantEvent: []executor.ProgressEvent{
				{Criterion: 1, State: "START"},
				{Criterion: 1, State: "DONE"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tail, progress := ParseRunLog(tc.log)
			if tail != tc.wantTail {
				t.Fatalf("tail = %q, want %q", tail, tc.wantTail)
			}
			if len(progress) != len(tc.wantEvent) {
				t.Fatalf("progress = %#v, want %#v", progress, tc.wantEvent)
			}
			for index, event := range progress {
				if event.Criterion != tc.wantEvent[index].Criterion || event.State != tc.wantEvent[index].State {
					t.Fatalf("progress[%d] = %#v, want %#v", index, event, tc.wantEvent[index])
				}
				if !event.At.IsZero() {
					t.Fatalf("progress[%d].At = %v, want zero: replay cannot recover timestamps", index, event.At)
				}
			}
		})
	}

	t.Run("empty log", func(t *testing.T) {
		tail, progress := ParseRunLog("")
		if tail != "" || len(progress) != 0 {
			t.Fatalf("ParseRunLog(\"\") = %q, %#v", tail, progress)
		}
	})
}

func TestCriteriaFromProofs(t *testing.T) {
	t.Parallel()

	criteria := CriteriaFromProofs([]gates.Verdict{
		{Name: "proof 1", Pass: true, Signal: "state is bounded — `go test ./loop` passed"},
		{Name: "proof 2", Pass: false, Signal: "questions are noul — `go vet ./loop` exited 3"},
		{Name: "proof 3", Pass: true, Signal: "verifier asked — no proof command; left to the verifier"},
		{Name: "proof 4", Pass: false, Signal: "wrote the file — could not run `test -f out/1.txt`: no shell"},
		{Name: "proof 5", Pass: false},
	})
	want := []gates.Criterion{
		{Text: "state is bounded", Proof: "go test ./loop"},
		{Text: "questions are noul", Proof: "go vet ./loop"},
		{Text: "verifier asked"},
		{Text: "wrote the file", Proof: "test -f out/1.txt"},
		{Text: ""},
	}
	if len(criteria) != len(want) {
		t.Fatalf("criteria = %#v, want %#v", criteria, want)
	}
	for index, criterion := range want {
		if criteria[index] != criterion {
			t.Fatalf("criteria[%d] = %#v, want %#v", index, criteria[index], criterion)
		}
	}

	if got := CriteriaFromProofs(nil); len(got) != 0 {
		t.Fatalf("CriteriaFromProofs(nil) = %#v, want empty", got)
	}
}

func TestReplayRunLogName(t *testing.T) {
	t.Parallel()

	name := ReplayRunLogName("greetings-20260906-040001", "greetings", "task_1", time.Time{}, 1)
	if want := "2026-09-06-greetings-task-1-e1.out.log"; name != want {
		t.Fatalf("name = %q, want %q", name, want)
	}

	fallback := ReplayRunLogName("satisfied", "satisfied", "task_2", time.Date(2026, 9, 20, 4, 0, 1, 0, time.UTC), 3)
	if want := "2026-09-20-satisfied-task-2-e3.out.log"; fallback != want {
		t.Fatalf("fallback name = %q, want %q", fallback, want)
	}
}

func TestReplayClaimEvidenceState(t *testing.T) {
	t.Parallel()

	log := fmt.Sprintf("# exit 0 · finished true · timed out false · rate limited false · %s\n\n## stdout\n\n%s\n\n## stderr\n\n%s",
		(3 * time.Second).Round(time.Second),
		strings.Join([]string{
			"edited loop/judgment.go",
			"BATUTA-PROGRESS 1 START",
			"BATUTA-PROGRESS 1 DONE",
		}, "\n"),
		"TASK 2: INCOMPLETE — still writing questions",
	)
	report := gates.Report{
		TaskID:    "task_1",
		Execution: 1,
		Finished:  gates.Verdict{Name: "finished", Pass: true, Signal: "exit 0"},
		Tree:      gates.Verdict{Name: "tree", Pass: true, Signal: "the worktree differs from the attempt's base"},
		Tests:     gates.Verdict{Name: "tests", Pass: true, Signal: "`go test ./...` passed"},
		Scope:     gates.Verdict{Name: "scope", Pass: false, Signal: "1 path(s) outside Scope", Detail: "docs/judge.md\nscratch.txt"},
		Proofs: []gates.Verdict{
			{Name: "proof 1", Pass: true, Signal: "state is bounded — `go test ./loop` passed"},
			{Name: "proof 2", Pass: false, Signal: "questions are noul — no proof command; left to the verifier"},
		},
		Verifier: &gates.Verdict{Name: "verifier", Pass: false, Signal: "1 criterion(s) INCOMPLETE", Detail: "TASK 2: INCOMPLETE — questions missing"},
		Passed:   false,
	}

	state, err := ReplayClaimEvidenceState(ReplayClaimEvidenceInput{
		Workspace:   "/work/core",
		TaskID:      "task_1",
		TaskTitle:   "Wire the claim check",
		Report:      report,
		TreeChanged: true,
		RunLog:      log,
	}, 16<<10)
	if err != nil {
		t.Fatalf("ReplayClaimEvidenceState() error = %v", err)
	}
	body := marshalState(t, state)

	task, _ := body["task"].(map[string]any)
	if task["id"] != "task_1" || task["title"] != "Wire the claim check" {
		t.Fatalf("task = %#v", task)
	}

	criteria, _ := body["criteria"].([]any)
	if len(criteria) != 2 {
		t.Fatalf("criteria = %#v", body["criteria"])
	}
	first, _ := criteria[0].(map[string]any)
	if first["text"] != "state is bounded" || first["proof"] != "go test ./loop" || first["pass"] != true {
		t.Fatalf("criteria[0] = %#v", first)
	}
	second, _ := criteria[1].(map[string]any)
	if second["text"] != "questions are noul" || second["pass"] != false {
		t.Fatalf("criteria[1] = %#v", second)
	}

	report_, _ := body["executor_report"].(string)
	for _, want := range []string{"edited loop/judgment.go", "BATUTA-PROGRESS 1 START", "BATUTA-PROGRESS 1 DONE", "TASK 2: INCOMPLETE"} {
		if !strings.Contains(report_, want) {
			t.Fatalf("executor_report missing %q:\n%s", want, report_)
		}
	}

	progress, _ := body["progress"].([]any)
	if len(progress) != 2 {
		t.Fatalf("progress = %#v, want two events from the run log", body["progress"])
	}

	tree, _ := body["tree"].(map[string]any)
	if tree["changed"] != true {
		t.Fatalf("tree = %#v, want changed", tree)
	}
	paths, _ := tree["changed_paths"].([]any)
	if len(paths) != 2 || paths[0] != "docs/judge.md" || paths[1] != "scratch.txt" {
		t.Fatalf("changed_paths = %#v, want the failed scope verdict's paths", tree["changed_paths"])
	}

	verifier, _ := body["verifier"].(map[string]any)
	if verifier == nil || verifier["signal"] != "1 criterion(s) INCOMPLETE" {
		t.Fatalf("verifier = %#v", body["verifier"])
	}

	outcome, _ := body["outcome"].(map[string]any)
	if outcome["passed"] != false {
		t.Fatalf("outcome = %#v", body["outcome"])
	}

	t.Run("the live and replayed states agree on the shared evidence", func(t *testing.T) {
		live, err := BuildClaimEvidenceState(ClaimEvidenceInput{
			Workspace: "/work/core",
			Task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "task_1", Title: "Wire the claim check"},
			},
			Criteria: []gates.Criterion{
				{Text: "state is bounded", Proof: "go test ./loop"},
				{Text: "questions are noul"},
			},
			Report: report,
			OutputTail: strings.Join([]string{
				"edited loop/judgment.go",
				"BATUTA-PROGRESS 1 START",
				"BATUTA-PROGRESS 1 DONE",
			}, "\n") + "\nTASK 2: INCOMPLETE — still writing questions",
			Progress: []executor.ProgressEvent{
				{Criterion: 1, State: "START", At: time.Date(2026, 9, 20, 4, 0, 1, 0, time.UTC)},
				{Criterion: 1, State: "DONE", At: time.Date(2026, 9, 20, 4, 0, 2, 0, time.UTC)},
			},
			ChangedPaths: []string{"docs/judge.md", "scratch.txt"},
			TreeChanged:  true,
		}, 16<<10)
		if err != nil {
			t.Fatalf("BuildClaimEvidenceState() error = %v", err)
		}
		liveBody := marshalState(t, live)
		for _, key := range []string{"task", "criteria", "executor_report", "tree", "verifier", "outcome"} {
			liveJSON, _ := json.Marshal(liveBody[key])
			replayJSON, _ := json.Marshal(body[key])
			if string(liveJSON) != string(replayJSON) {
				t.Fatalf("%s differs between live and replayed states:\nlive:    %s\nreplayed: %s", key, liveJSON, replayJSON)
			}
		}
		// Progress agrees on every pair the journal can carry; the live
		// events also carry the timestamps, which replay cannot recover.
		pairs := func(events []any) []string {
			out := make([]string, 0, len(events))
			for _, event := range events {
				body, _ := event.(map[string]any)
				out = append(out, fmt.Sprintf("%v %v", body["criterion"], body["state"]))
			}
			return out
		}
		liveProgress, _ := liveBody["progress"].([]any)
		replayProgress, _ := body["progress"].([]any)
		if fmt.Sprint(pairs(liveProgress)) != fmt.Sprint(pairs(replayProgress)) || len(replayProgress) == 0 {
			t.Fatalf("progress differs between live and replayed states:\nlive:    %#v\nreplayed: %#v", liveProgress, replayProgress)
		}
	})

	t.Run("equal tree and missing log leave the state honest", func(t *testing.T) {
		state, err := ReplayClaimEvidenceState(ReplayClaimEvidenceInput{
			TaskID: "task_2",
			Report: gates.Report{Passed: true, Scope: gates.Verdict{Name: "scope", Pass: true, Signal: "within Scope"}},
		}, 16<<10)
		if err != nil {
			t.Fatalf("ReplayClaimEvidenceState() error = %v", err)
		}
		body := marshalState(t, state)
		tree, _ := body["tree"].(map[string]any)
		if tree["changed"] != false || len(tree["changed_paths"].([]any)) != 0 {
			t.Fatalf("tree = %#v, want equal to base without paths", tree)
		}
		report_, _ := body["executor_report"].(string)
		if report_ != "" {
			t.Fatalf("executor_report = %q, want empty without a run log", report_)
		}
	})
}

func TestJudgeRecordKinds(t *testing.T) {
	t.Parallel()

	if KindJudgeIntent != "judge_intent" || KindJudgeResult != "judge_result" {
		t.Fatalf("kinds = %q, %q", KindJudgeIntent, KindJudgeResult)
	}
	if KindJudgeIntent != journal.Kind(judge.KindIntent) || KindJudgeResult != journal.Kind(judge.KindResult) {
		t.Fatalf("loop kinds drifted from judge.KindIntent/KindResult: %q, %q", KindJudgeIntent, KindJudgeResult)
	}

	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	graph := json.RawMessage(`{"tasks":[]}`)
	opened, err := store.Append("demo", journal.Record{
		Kind:   KindOpened,
		Detail: json.RawMessage(`{"slug":"demo"}`),
		Graph:  graph,
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := json.Marshal(judge.IntentRecord{
		Decision:     "claim_evidence",
		QuestionKeys: []string{"claim_unsupported", "verifier_contradicted"},
		StateDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("demo", journal.Record{
		Kind:   KindJudgeIntent,
		TaskID: "task_1",
		Detail: intent,
		Graph:  opened.Graph,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(judge.ResultRecord{
		Decision:          "claim_evidence",
		QuestionKeys:      []string{"claim_unsupported", "verifier_contradicted"},
		StateDigest:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		UnavailableReason: judge.ReasonJudgeOff,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("demo", journal.Record{
		Kind:   KindJudgeResult,
		TaskID: "task_1",
		Detail: result,
		Graph:  opened.Graph,
	}); err != nil {
		t.Fatal(err)
	}

	records, err := store.Read("demo")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("len(records) = %d, want 3", len(records))
	}
	if records[1].Kind != KindJudgeIntent || records[1].TaskID != "task_1" {
		t.Fatalf("intent record = %+v", records[1])
	}
	if records[2].Kind != KindJudgeResult || records[2].TaskID != "task_1" {
		t.Fatalf("result record = %+v", records[2])
	}
	if string(records[1].Graph) != string(opened.Graph) || string(records[2].Graph) != string(opened.Graph) {
		t.Fatalf("judge records mutated the graph: intent=%s result=%s opened=%s", records[1].Graph, records[2].Graph, opened.Graph)
	}
	var intentDetail judge.IntentRecord
	if err := json.Unmarshal(records[1].Detail, &intentDetail); err != nil || intentDetail.Decision != "claim_evidence" {
		t.Fatalf("intent detail = %+v, %v", intentDetail, err)
	}
	var resultDetail judge.ResultRecord
	if err := json.Unmarshal(records[2].Detail, &resultDetail); err != nil || resultDetail.UnavailableReason != judge.ReasonJudgeOff {
		t.Fatalf("result detail = %+v, %v", resultDetail, err)
	}
}

func stringMap(t *testing.T, value any) map[string]string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("string map %s: %v", encoded, err)
	}
	return got
}

func newJudgmentRunner(t *testing.T, j judge.Judge, mode judge.Mode, threshold float64) *Runner {
	t.Helper()
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{
		opts: Options{
			Judge:       j,
			JudgeConfig: claimEvidenceJudgeConfig(mode, threshold),
		},
		root:     root,
		store:    store,
		graph:    &routing.DeliveryGraph{},
		delivery: "demo",
		now:      func() time.Time { return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) },
	}
}

func passingClaimReport() gates.Report {
	return gates.Report{
		Passed:   true,
		Finished: gates.Verdict{Name: "finished", Pass: true, Signal: "exit 0"},
		Tree:     gates.Verdict{Name: "tree", Pass: true, Signal: "changed"},
		Tests:    gates.Verdict{Name: "tests", Pass: true, Signal: "passed"},
		Scope:    gates.Verdict{Name: "scope", Pass: true, Signal: "in scope"},
		Proofs:   []gates.Verdict{{Name: "proof 1", Pass: true, Signal: "out/1.txt exists — `test -f out/1.txt` passed"}},
		Verifier: &gates.Verdict{Name: "verifier", Pass: true, Signal: "all DONE", Detail: "TASK 1: DONE"},
	}
}

func judgmentAttempt(accept string) attemptContext {
	return attemptContext{
		taskID: "task_1",
		plan: routing.PlanTask{
			TaskArtifact: routing.TaskArtifact{ID: "task_1", Title: "Add greeting one"},
			Accept:       []string{accept},
		},
	}
}

func marshalState(t *testing.T, state any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func criteriaMap(t *testing.T, value any) map[string]string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("criteria %s: %v", encoded, err)
	}
	return got
}

func containsAll(values []any, want ...string) bool {
	have := map[string]bool{}
	for _, value := range values {
		text, _ := value.(string)
		have[text] = true
	}
	for _, item := range want {
		if !have[item] {
			return false
		}
	}
	return true
}
