package loop

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
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
