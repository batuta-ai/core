package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/loop"
)

// benchOutcomeTestPlan is a four-lane plan whose tasks are classified with
// one recorded outcome each: candidate, retried, escalated and failed.
const benchOutcomeTestPlan = `# Plan — Classify bench outcomes
<!-- inputs: profile.md@sha256:1a2b3c4d5e6f routing.md@sha256:0f0e0d0c0b0a -->

**Goal:** Score the classify decision against the recorded outcomes.
**Created:** 2026-09-20 · **Status:** approved

## Tasks
- [ ] 1. First attempt sticks — testing/low → opencode/kimi-k2.5
      Scope: tests/checkout/timeout.test.ts
      Accept: a failing test reproduces the timeout → npm test -- timeout
- [ ] 2. Retry the same lane — backend/medium → codex/gpt-5.6-terra
      Depends on: 1
      Scope: src/checkout/payment.ts
      Accept: the reproduction passes → npm test -- timeout
- [ ] 3. Bump to a higher lane — backend/medium → codex/gpt-5.6-terra
      Depends on: 2
      Scope: src/checkout/payment.ts
      Accept: the reproduction passes → npm test -- timeout
- [ ] 4. Only one try — docs/high
      Accept: README mentions the retry → grep -n retry README.md

## Decisions and context
Free prose for a fresh session.
`

// classifyTestPlan is a two-lane plan the classify runs score; the slug the
// command derives from the file name is classify-bench.
const classifyTestPlan = `# Plan — Classify bench
<!-- inputs: profile.md@sha256:1a2b3c4d5e6f routing.md@sha256:0f0e0d0c0b0a -->

**Goal:** Score the classify decision against the plan lanes.
**Created:** 2026-09-20 · **Status:** approved

## Tasks
- [ ] 1. Reproduce the timeout in a test — testing/low → opencode/kimi-k2.5
      Scope: tests/checkout/timeout.test.ts
      Accept: a failing test reproduces the timeout → npm test -- timeout
- [ ] 2. Retry the payment call once — backend/medium → codex/gpt-5.6-terra
      Depends on: 1
      Scope: src/checkout/payment.ts
      Accept: the reproduction passes → npm test -- timeout
- [ ] 3. Document the retry policy — docs/low
      Accept: README mentions the retry → grep -n retry README.md

## Decisions and context
Free prose for a fresh session.
`

// classifyRoute answers one task's classify request: the HTTP status, the
// two choices with their shared confidence and the input tokens the usage
// reports; a negative inputTokens omits the usage.
type classifyRoute struct {
	status      int
	complexity  string
	domain      string
	confidence  float64
	inputTokens int
}

// classifyCallRecord records the request bodies the fake judge received.
type classifyCallRecord struct {
	bodies []map[string]any
}

// classifyTestServer answers every classify request with the route of the
// request's task title, recording the bodies in call order.
func classifyTestServer(t *testing.T, routes map[string]classifyRoute) (*httptest.Server, *classifyCallRecord) {
	t.Helper()
	record := &classifyCallRecord{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		record.bodies = append(record.bodies, body)
		title := ""
		if state, ok := body["state"].(map[string]any); ok {
			if task, ok := state["task"].(map[string]any); ok {
				title, _ = task["title"].(string)
			}
		}
		route, ok := routes[title]
		if !ok {
			route = classifyRoute{status: http.StatusOK, complexity: "medium", domain: "general", confidence: 0.9, inputTokens: 12}
		}
		if route.status == 0 {
			route.status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		if route.status != http.StatusOK {
			w.WriteHeader(route.status)
			if _, err := w.Write([]byte(`{"error":{"code":"up","message":"no"}}`)); err != nil {
				t.Errorf("write response: %v", err)
			}
			return
		}
		response := map[string]any{
			"model": "jev-1.13.0",
			"answers": map[string]any{
				"complexity": map[string]any{"type": "choice", "choice": route.complexity, "confidence": route.confidence},
				"domain":     map[string]any{"type": "choice", "choice": route.domain, "confidence": route.confidence},
			},
		}
		if route.inputTokens >= 0 {
			response["usage"] = map[string]any{"input_tokens": route.inputTokens, "output_tokens": 3}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, record
}

// classifyTestPlanPath writes the classify test plan into the workspace.
func classifyTestPlanPath(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "classify-bench.md")
	if err := os.WriteFile(path, []byte(classifyTestPlan), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestJudgeClassifyPlan(t *testing.T) {
	server, record := classifyTestServer(t, map[string]classifyRoute{
		"Reproduce the timeout in a test": {complexity: "low", domain: "testing", confidence: 0.9, inputTokens: 12},
		"Retry the payment call once":     {complexity: "medium", domain: "backend", confidence: 0.85, inputTokens: 7},
		"Document the retry policy":       {complexity: "low", domain: "docs", confidence: 0.95, inputTokens: -1},
	})
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	planPath := classifyTestPlanPath(t, root)
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "classify", "--plan", planPath, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge classify = %v\nstderr: %s", err, stderr.String())
	}
	got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	want := []string{
		"threshold=0.7",
		"task_1 plan=testing/low judge=testing/low complexity=0.90 domain=0.90 fallback=false input_tokens=12",
		"task_2 plan=backend/medium judge=backend/medium complexity=0.85 domain=0.85 fallback=false input_tokens=7",
		"task_3 plan=docs/low judge=docs/low complexity=0.95 domain=0.95 fallback=false input_tokens=unknown",
	}
	if len(got) != len(want) {
		t.Fatalf("stdout = %q\nwant %q", got, want)
	}
	for i, line := range want {
		if got[i] != line {
			t.Errorf("line %d = %q, want %q", i+1, got[i], line)
		}
	}
	if len(record.bodies) != 3 {
		t.Fatalf("judge calls = %d, want one per task", len(record.bodies))
	}
	for i, body := range record.bodies {
		state, ok := body["state"].(map[string]any)
		if !ok {
			t.Fatalf("call %d state = %#v", i, body["state"])
		}
		if _, carries := state["complexity"]; carries {
			t.Errorf("call %d state carries the host complexity", i)
		}
		if _, carries := state["domain"]; carries {
			t.Errorf("call %d state carries the host domain", i)
		}
		task, ok := state["task"].(map[string]any)
		if !ok || task["title"] == "" {
			t.Errorf("call %d state task = %#v", i, state["task"])
		}
		questions, ok := body["questions"].(map[string]any)
		if !ok {
			t.Fatalf("call %d questions = %#v", i, body["questions"])
		}
		for _, key := range []string{"complexity", "domain"} {
			question, ok := questions[key].(map[string]any)
			if !ok || question["type"] != string(judge.QuestionChoice) {
				t.Errorf("call %d question %s = %#v", i, key, questions[key])
			}
		}
	}
}

func TestJudgeClassifyPlanJSON(t *testing.T) {
	server, record := classifyTestServer(t, map[string]classifyRoute{
		"Reproduce the timeout in a test": {complexity: "low", domain: "testing", confidence: 0.9, inputTokens: 12},
		"Retry the payment call once":     {complexity: "medium", domain: "backend", confidence: 0.85, inputTokens: 7},
		"Document the retry policy":       {complexity: "low", domain: "docs", confidence: 0.95, inputTokens: -1},
	})
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	planPath := classifyTestPlanPath(t, root)
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "classify", "--plan", planPath, "--json", "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge classify = %v\nstderr: %s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout lines = %d, want one object per task\n%s", len(lines), stdout.String())
	}
	type classifyJSON struct {
		Task                 string  `json:"task"`
		PlanComplexity       string  `json:"plan_complexity"`
		PlanDomain           string  `json:"plan_domain"`
		Complexity           string  `json:"complexity"`
		Domain               string  `json:"domain"`
		ComplexityConfidence float64 `json:"complexity_confidence"`
		DomainConfidence     float64 `json:"domain_confidence"`
		Fallback             bool    `json:"fallback"`
		Threshold            float64 `json:"threshold"`
		InputTokens          *int    `json:"input_tokens"`
		Unavailable          string  `json:"unavailable"`
	}
	want := []classifyJSON{
		{Task: "task_1", PlanComplexity: "low", PlanDomain: "testing", Complexity: "low", Domain: "testing",
			ComplexityConfidence: 0.9, DomainConfidence: 0.9, Threshold: 0.7, InputTokens: intPtr(12)},
		{Task: "task_2", PlanComplexity: "medium", PlanDomain: "backend", Complexity: "medium", Domain: "backend",
			ComplexityConfidence: 0.85, DomainConfidence: 0.85, Threshold: 0.7, InputTokens: intPtr(7)},
		{Task: "task_3", PlanComplexity: "low", PlanDomain: "docs", Complexity: "low", Domain: "docs",
			ComplexityConfidence: 0.95, DomainConfidence: 0.95, Threshold: 0.7},
	}
	for i, line := range lines {
		var got classifyJSON
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", i+1, err, line)
		}
		want := want[i]
		if got.Task != want.Task || got.PlanComplexity != want.PlanComplexity || got.PlanDomain != want.PlanDomain ||
			got.Complexity != want.Complexity || got.Domain != want.Domain ||
			got.ComplexityConfidence != want.ComplexityConfidence || got.DomainConfidence != want.DomainConfidence ||
			got.Fallback != want.Fallback || got.Threshold != want.Threshold {
			t.Errorf("line %d = %#v, want %#v", i+1, got, want)
		}
		switch {
		case want.InputTokens == nil && got.InputTokens != nil:
			t.Errorf("line %d input_tokens = %d, want unknown", i+1, *got.InputTokens)
		case want.InputTokens != nil && (got.InputTokens == nil || *got.InputTokens != *want.InputTokens):
			t.Errorf("line %d input_tokens = %v, want %d", i+1, got.InputTokens, *want.InputTokens)
		}
		if got.Unavailable != "" {
			t.Errorf("line %d unavailable = %q", i+1, got.Unavailable)
		}
	}
	if len(record.bodies) != 3 {
		t.Fatalf("judge calls = %d, want one per task", len(record.bodies))
	}
}

func intPtr(value int) *int { return &value }

func TestJudgeClassifyThreshold(t *testing.T) {
	const config = `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY","decisions":{"classify":{"mode":"shadow","threshold":0.9}}}`
	server, _ := classifyTestServer(t, map[string]classifyRoute{
		"Reproduce the timeout in a test": {complexity: "medium", domain: "testing", confidence: 0.85, inputTokens: 5},
	})
	root := judgeTestWorkspace(t, config)
	planPath := classifyTestPlanPath(t, root)
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "classify", "--plan", planPath, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge classify = %v\nstderr: %s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("stdout lines = %d, want 4\n%s", len(lines), stdout.String())
	}
	if lines[0] != "threshold=0.9" {
		t.Errorf("threshold line = %q, want threshold=0.9", lines[0])
	}
	// 0.85 sits below the configured 0.9, so both axes fall back and the
	// fallback flag says so.
	want := "task_1 plan=testing/low judge=general/critical complexity=0.85 domain=0.85 fallback=true input_tokens=5"
	if lines[1] != want {
		t.Errorf("task line = %q, want %q", lines[1], want)
	}

	// Without a classify decision the default threshold is 0.7 and 0.75
	// clears it, so the judge lane is kept.
	server, _ = classifyTestServer(t, map[string]classifyRoute{
		"Reproduce the timeout in a test": {complexity: "low", domain: "testing", confidence: 0.75, inputTokens: 5},
	})
	root = judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	planPath = classifyTestPlanPath(t, root)
	stdout.Reset()
	stderr.Reset()
	err = run([]string{"judge", "classify", "--plan", planPath, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge classify = %v\nstderr: %s", err, stderr.String())
	}
	lines = strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if lines[0] != "threshold=0.7" {
		t.Errorf("threshold line = %q, want threshold=0.7", lines[0])
	}
	want = "task_1 plan=testing/low judge=testing/low complexity=0.75 domain=0.75 fallback=false input_tokens=5"
	if lines[1] != want {
		t.Errorf("task line = %q, want %q", lines[1], want)
	}
}

func TestJudgeClassifyUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		reason string
	}{
		{"judge off", "", "judge_off"},
		{"key missing", `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_UNSET_KEY"}`, "key_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := judgeTestWorkspace(t, tc.config)
			planPath := classifyTestPlanPath(t, root)
			var stdout, stderr strings.Builder
			err := run([]string{"judge", "classify", "--plan", planPath, "--workspace", root}, &stdout, &stderr)
			if err != nil {
				t.Fatalf("judge classify = %v\nstderr: %s", err, stderr.String())
			}
			for i, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")[1:] {
				if !strings.Contains(line, "unavailable="+tc.reason) {
					t.Errorf("task line %d = %q, want unavailable=%s", i+1, line, tc.reason)
				}
				if strings.Contains(line, "judge=") {
					t.Errorf("line %d names a lane the judge never chose: %q", i+1, line)
				}
			}
		})
	}
}

func TestJudgeClassifyMidRunUnavailable(t *testing.T) {
	server, _ := classifyTestServer(t, map[string]classifyRoute{
		"Reproduce the timeout in a test": {complexity: "low", domain: "testing", confidence: 0.9, inputTokens: 12},
		"Retry the payment call once":     {status: http.StatusInternalServerError},
	})
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	planPath := classifyTestPlanPath(t, root)
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "classify", "--plan", planPath, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge classify = %v\nstderr: %s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("stdout lines = %d, want 4\n%s", len(lines), stdout.String())
	}
	if !strings.Contains(lines[1], "judge=testing/low") || !strings.Contains(lines[1], "fallback=false") {
		t.Errorf("task 1 line = %q, want the judged lane", lines[1])
	}
	if !strings.Contains(lines[2], "unavailable=") || strings.Contains(lines[2], "judge=") {
		t.Errorf("task 2 line = %q, want unavailable and no lane", lines[2])
	}
}

func TestJudgeClassifyRejectsInvalidArguments(t *testing.T) {
	root := t.TempDir()
	planPath := filepath.Join(root, "classify-bench.md")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing plan flag", []string{"judge", "classify", "--workspace", root}},
		{"positional argument", []string{"judge", "classify", "--plan", planPath, "--workspace", root, "extra"}},
		{"missing plan file", []string{"judge", "classify", "--plan", planPath, "--workspace", root}},
		{"bad plan file", []string{"judge", "classify", "--plan", writeBadPlan(t, root), "--workspace", root}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if err := run(tc.args, &stdout, &stderr); err == nil {
				t.Fatalf("judge classify = nil, want an error")
			}
		})
	}
}

// writeBadPlan writes a plan whose task line names an unknown lane, so
// ParsePlan rejects it.
func writeBadPlan(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "bad-bench.md")
	payload := fmt.Sprintf("%s\n%s", "# Plan — Bad bench", "- [ ] 1. Do the thing — backend/huge\n      Accept: done → grep done")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// benchJournalEntry is one fixture record appended to a test delivery.
type benchJournalEntry struct {
	kind   journal.Kind
	taskID string
	detail map[string]any
}

// benchStartedDetail is the executor_started detail of a fixture attempt.
func benchStartedDetail(execution int, executor string) map[string]any {
	return map[string]any{"execution": execution, "executor": executor, "model": "small"}
}

// benchFinishedDetail is the executor_finished detail of a fixture attempt.
func benchFinishedDetail(execution int) map[string]any {
	return map[string]any{"execution": execution, "tree_changed": true, "base_head_sha": "abc"}
}

// benchCandidateDetail is the candidate_recorded detail of a fixture attempt.
func benchCandidateDetail(execution int) map[string]any {
	return map[string]any{"execution": execution, "commit": "abc", "evidence": map[string]any{"base_sha": "def"}}
}

// writeBenchJournal appends the fixture records of one delivery into a
// journal directory; journal.Store fills the hash chain.
func writeBenchJournal(t *testing.T, dir, delivery string, entries []benchJournalEntry) {
	t.Helper()
	store, err := journal.Open(dir)
	if err != nil {
		t.Fatalf("open journal store: %v", err)
	}
	for _, entry := range entries {
		detail, err := json.Marshal(entry.detail)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(delivery, journal.Record{Kind: entry.kind, TaskID: entry.taskID, Detail: detail}); err != nil {
			t.Fatalf("append %s %s: %v", entry.kind, entry.taskID, err)
		}
	}
}

// writeBenchPlan writes a bench plan fixture under the workspace root.
func writeBenchPlan(t *testing.T, root, name, payload string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClassifyBenchSummary(t *testing.T) {
	server, _ := classifyTestServer(t, map[string]classifyRoute{
		"Reproduce the timeout in a test": {complexity: "low", domain: "testing", confidence: 0.9, inputTokens: 12},
		"Retry the payment call once":     {complexity: "low", domain: "backend", confidence: 0.85, inputTokens: 7},
		"Document the retry policy":       {complexity: "low", domain: "docs", confidence: 0.95, inputTokens: -1},
	})
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	planPath := classifyTestPlanPath(t, root)
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "classify", "bench", "--plan", planPath, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge classify bench = %v\nstderr: %s", err, stderr.String())
	}
	got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	want := []string{
		"threshold=0.7",
		"classify-bench task_1 plan=testing/low judge=testing/low complexity=0.90 domain=0.90 fallback=false input_tokens=12 outcome=unknown relation=equal",
		"classify-bench task_2 plan=backend/medium judge=backend/low complexity=0.85 domain=0.85 fallback=false input_tokens=7 outcome=unknown relation=lower",
		"classify-bench task_3 plan=docs/low judge=docs/low complexity=0.95 domain=0.95 fallback=false input_tokens=unknown outcome=unknown relation=equal",
		"bench tasks=3 complexity_exact=2/3 domain_exact=3/3 under=1 over=0 fallbacks=0 unavailable=0 baseline=2/3",
		"matrix plan=low judge: low=2 medium=0 high=0 critical=0",
		"matrix plan=medium judge: low=1 medium=0 high=0 critical=0",
		"matrix plan=high judge: low=0 medium=0 high=0 critical=0",
		"matrix plan=critical judge: low=0 medium=0 high=0 critical=0",
		"outcomes relation=lower: candidate=0 retried=0 escalated=0 failed=0 unknown=1",
		"outcomes relation=equal: candidate=0 retried=0 escalated=0 failed=0 unknown=2",
		"outcomes relation=higher: candidate=0 retried=0 escalated=0 failed=0 unknown=0",
	}
	if len(got) != len(want) {
		t.Fatalf("stdout = %q\nwant %q", got, want)
	}
	for i, line := range want {
		if got[i] != line {
			t.Errorf("line %d = %q, want %q", i+1, got[i], line)
		}
	}
}

// assertBenchBodiesLabelFree fails unless the fake judge received one call
// per task and no body carries the fixture delivery name classify-bench-run1,
// a journal kind or the host complexity or domain under task.
func assertBenchBodiesLabelFree(t *testing.T, record *classifyCallRecord, wantCalls int) {
	t.Helper()
	if len(record.bodies) != wantCalls {
		t.Fatalf("judge calls = %d, want one per task", len(record.bodies))
	}
	for i, body := range record.bodies {
		wire, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("re-marshal call %d body: %v", i, err)
		}
		for _, leaked := range []string{"classify-bench-run1", "executor_started"} {
			if strings.Contains(string(wire), leaked) {
				t.Errorf("call %d body carries %q", i, leaked)
			}
		}
		state, ok := body["state"].(map[string]any)
		if !ok {
			t.Fatalf("call %d state = %#v", i, body["state"])
		}
		task, ok := state["task"].(map[string]any)
		if !ok {
			t.Fatalf("call %d state task = %#v", i, state["task"])
		}
		for _, key := range []string{"complexity", "domain"} {
			if _, carries := task[key]; carries {
				t.Errorf("call %d task carries the host %s", i, key)
			}
		}
	}
}

func TestClassifyBenchOutcome(t *testing.T) {
	server, record := classifyTestServer(t, map[string]classifyRoute{
		"First attempt sticks":  {complexity: "low", domain: "testing", confidence: 0.9, inputTokens: 12},
		"Retry the same lane":   {complexity: "low", domain: "backend", confidence: 0.9, inputTokens: 12},
		"Bump to a higher lane": {complexity: "high", domain: "backend", confidence: 0.9, inputTokens: 12},
		"Only one try":          {complexity: "medium", domain: "docs", confidence: 0.9, inputTokens: 12},
	})
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	planPath := writeBenchPlan(t, root, "classify-bench.md", benchOutcomeTestPlan)
	journals := t.TempDir()
	writeBenchJournal(t, journals, "classify-bench-run1", []benchJournalEntry{
		{loop.KindStarted, "task_1", benchStartedDetail(1, "opencode")},
		{loop.KindFinished, "task_1", benchFinishedDetail(1)},
		{loop.KindCandidate, "task_1", benchCandidateDetail(1)},
		{loop.KindStarted, "task_2", benchStartedDetail(1, "codex")},
		{loop.KindFinished, "task_2", benchFinishedDetail(1)},
		{loop.KindStarted, "task_2", benchStartedDetail(2, "codex")},
		{loop.KindFinished, "task_2", benchFinishedDetail(2)},
		{loop.KindCandidate, "task_2", benchCandidateDetail(2)},
		{loop.KindStarted, "task_3", benchStartedDetail(1, "codex")},
		{loop.KindFinished, "task_3", benchFinishedDetail(1)},
		{loop.KindStarted, "task_3", benchStartedDetail(2, "opencode")},
		{loop.KindFinished, "task_3", benchFinishedDetail(2)},
		{loop.KindStarted, "task_4", benchStartedDetail(1, "codex")},
		{loop.KindFinished, "task_4", benchFinishedDetail(1)},
	})
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "classify", "bench", "--plan", planPath, "--journals", journals, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge classify bench = %v\nstderr: %s", err, stderr.String())
	}
	got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	want := []string{
		"threshold=0.7",
		"classify-bench task_1 plan=testing/low judge=testing/low complexity=0.90 domain=0.90 fallback=false input_tokens=12 outcome=candidate relation=equal",
		"classify-bench task_2 plan=backend/medium judge=backend/low complexity=0.90 domain=0.90 fallback=false input_tokens=12 outcome=retried relation=lower",
		"classify-bench task_3 plan=backend/medium judge=backend/high complexity=0.90 domain=0.90 fallback=false input_tokens=12 outcome=escalated relation=higher",
		"classify-bench task_4 plan=docs/high judge=docs/medium complexity=0.90 domain=0.90 fallback=false input_tokens=12 outcome=failed relation=lower",
		"bench tasks=4 complexity_exact=1/4 domain_exact=4/4 under=2 over=1 fallbacks=0 unavailable=0 baseline=2/4",
		"matrix plan=low judge: low=1 medium=0 high=0 critical=0",
		"matrix plan=medium judge: low=1 medium=0 high=1 critical=0",
		"matrix plan=high judge: low=0 medium=1 high=0 critical=0",
		"matrix plan=critical judge: low=0 medium=0 high=0 critical=0",
		"outcomes relation=lower: candidate=0 retried=1 escalated=0 failed=1 unknown=0",
		"outcomes relation=equal: candidate=1 retried=0 escalated=0 failed=0 unknown=0",
		"outcomes relation=higher: candidate=0 retried=0 escalated=1 failed=0 unknown=0",
	}
	if len(got) != len(want) {
		t.Fatalf("stdout = %q\nwant %q", got, want)
	}
	for i, line := range want {
		if got[i] != line {
			t.Errorf("line %d = %q, want %q", i+1, got[i], line)
		}
	}
	assertBenchBodiesLabelFree(t, record, 4)
}

// TestClassifyBenchOutcomeRetryThenEscalate builds the doctrine journal of a
// task that retried once on the same executor and then escalated: finished
// [1,2,3] with executors [A,A,B].
func TestClassifyBenchOutcomeRetryThenEscalate(t *testing.T) {
	var records []journal.Record
	for _, attempt := range []struct {
		execution int
		executor  string
	}{{1, "opencode"}, {2, "opencode"}, {3, "codex"}} {
		started, err := json.Marshal(benchStartedDetail(attempt.execution, attempt.executor))
		if err != nil {
			t.Fatal(err)
		}
		finished, err := json.Marshal(benchFinishedDetail(attempt.execution))
		if err != nil {
			t.Fatal(err)
		}
		records = append(records,
			journal.Record{Kind: loop.KindStarted, TaskID: "task_1", Detail: started},
			journal.Record{Kind: loop.KindFinished, TaskID: "task_1", Detail: finished},
		)
	}
	if got := benchOutcome(records, "task_1"); got != benchOutcomeEscalated {
		t.Fatalf("benchOutcome = %q, want %q", got, benchOutcomeEscalated)
	}
}

func TestClassifyBenchOutcomeUnknown(t *testing.T) {
	server, record := classifyTestServer(t, map[string]classifyRoute{
		"Reproduce the timeout in a test": {complexity: "low", domain: "testing", confidence: 0.9, inputTokens: 12},
		"Retry the payment call once":     {complexity: "medium", domain: "backend", confidence: 0.9, inputTokens: 12},
		"Document the retry policy":       {complexity: "low", domain: "docs", confidence: 0.9, inputTokens: 12},
	})
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	planPath := classifyTestPlanPath(t, root)
	journals := t.TempDir()
	writeBenchJournal(t, journals, "classify-bench-run1", []benchJournalEntry{
		{loop.KindStarted, "task_1", benchStartedDetail(1, "opencode")},
		{loop.KindFinished, "task_1", benchFinishedDetail(1)},
		{loop.KindCandidate, "task_1", benchCandidateDetail(1)},
	})
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "classify", "bench", "--plan", planPath, "--journals", journals, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge classify bench = %v\nstderr: %s", err, stderr.String())
	}
	got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(got) != 12 {
		t.Fatalf("stdout lines = %d, want 12\n%s", len(got), stdout.String())
	}
	if !strings.Contains(got[1], "task_1") || !strings.Contains(got[1], "outcome=candidate relation=equal") {
		t.Errorf("task 1 line = %q, want the journaled candidate", got[1])
	}
	for i, line := range got[2:4] {
		if !strings.Contains(line, "outcome=unknown relation=equal") {
			t.Errorf("task %d line = %q, want outcome=unknown", i+2, line)
		}
	}
	if got[4] != "bench tasks=3 complexity_exact=3/3 domain_exact=3/3 under=0 over=0 fallbacks=0 unavailable=0 baseline=2/3" {
		t.Errorf("summary = %q", got[4])
	}
	if got[10] != "outcomes relation=equal: candidate=1 retried=0 escalated=0 failed=0 unknown=2" {
		t.Errorf("equal outcomes = %q, want the candidate counted beside the unknowns", got[10])
	}
	assertBenchBodiesLabelFree(t, record, 3)
}

func TestClassifyBenchJSON(t *testing.T) {
	server, _ := classifyTestServer(t, map[string]classifyRoute{
		"Reproduce the timeout in a test": {complexity: "low", domain: "testing", confidence: 0.9, inputTokens: 12},
		"Retry the payment call once":     {complexity: "low", domain: "backend", confidence: 0.85, inputTokens: 7},
		"Document the retry policy":       {complexity: "low", domain: "docs", confidence: 0.95, inputTokens: -1},
	})
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	planPath := classifyTestPlanPath(t, root)
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "classify", "bench", "--plan", planPath, "--json", "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge classify bench = %v\nstderr: %s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("stdout lines = %d, want three tasks plus a summary\n%s", len(lines), stdout.String())
	}
	var task2 struct {
		PlanSlug string `json:"plan_slug"`
		Outcome  string `json:"outcome"`
		Relation string `json:"relation"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &task2); err != nil {
		t.Fatalf("line 2 is not JSON: %v\n%s", err, lines[1])
	}
	if task2.PlanSlug != "classify-bench" || task2.Outcome != "unknown" || task2.Relation != "lower" {
		t.Errorf("task 2 = %+v", task2)
	}
	var summary struct {
		Summary struct {
			Tasks           int `json:"tasks"`
			ComplexityExact int `json:"complexity_exact"`
			Under           int `json:"under"`
			Baseline        struct {
				Label string  `json:"label"`
				Count int     `json:"count"`
				Share float64 `json:"share"`
			} `json:"baseline"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(lines[3]), &summary); err != nil {
		t.Fatalf("line 4 is not JSON: %v\n%s", err, lines[3])
	}
	if summary.Summary.Tasks != 3 || summary.Summary.ComplexityExact != 2 || summary.Summary.Under != 1 {
		t.Errorf("summary = %+v", summary.Summary)
	}
	if summary.Summary.Baseline.Label != "low" || summary.Summary.Baseline.Count != 2 {
		t.Errorf("baseline = %+v", summary.Summary.Baseline)
	}
}

func TestClassifyBenchRejectsInvalidArguments(t *testing.T) {
	root := t.TempDir()
	planPath := filepath.Join(root, "classify-bench.md")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing plan flag", []string{"judge", "classify", "bench", "--workspace", root}},
		{"positional argument", []string{"judge", "classify", "bench", "--plan", planPath, "--workspace", root, "extra"}},
		{"missing plan file", []string{"judge", "classify", "bench", "--plan", planPath, "--workspace", root}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if err := run(tc.args, &stdout, &stderr); err == nil {
				t.Fatalf("judge classify bench = nil, want an error")
			}
		})
	}
}

type classifyV2Route struct {
	answers map[string]float64
}

func classifyV2TestServer(t *testing.T, routes map[string]classifyV2Route) (*httptest.Server, *classifyCallRecord) {
	t.Helper()
	record := &classifyCallRecord{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		record.bodies = append(record.bodies, body)
		state, _ := body["state"].(map[string]any)
		task, _ := state["task"].(map[string]any)
		title, _ := task["title"].(string)
		answers := map[string]any{}
		for key, value := range routes[title].answers {
			answers[key] = map[string]any{"type": "noul", "noul": value}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"model": "jev-test", "answers": answers}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, record
}

func classifyV2Fixture(t *testing.T) (string, string, string, *httptest.Server, *classifyCallRecord) {
	t.Helper()
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	plan := writeBenchPlan(t, root, "classify-bench.md", benchOutcomeTestPlan)
	journals := t.TempDir()
	writeBenchJournal(t, journals, "classify-bench-run1", []benchJournalEntry{
		{loop.KindStarted, "task_1", benchStartedDetail(1, "opencode")},
		{loop.KindFinished, "task_1", benchFinishedDetail(1)},
		{loop.KindCandidate, "task_1", benchCandidateDetail(1)},
		{loop.KindStarted, "task_2", benchStartedDetail(1, "codex")},
		{loop.KindFinished, "task_2", benchFinishedDetail(1)},
		{loop.KindStarted, "task_2", benchStartedDetail(2, "codex")},
		{loop.KindFinished, "task_2", benchFinishedDetail(2)},
		{loop.KindStarted, "task_3", benchStartedDetail(1, "codex")},
		{loop.KindFinished, "task_3", benchFinishedDetail(1)},
		{loop.KindStarted, "task_3", benchStartedDetail(2, "opencode")},
		{loop.KindFinished, "task_3", benchFinishedDetail(2)},
		{loop.KindStarted, "task_4", benchStartedDetail(1, "codex")},
		{loop.KindFinished, "task_4", benchFinishedDetail(1)},
	})
	server, record := classifyV2TestServer(t, map[string]classifyV2Route{
		"First attempt sticks":  {answers: map[string]float64{"contract": 0.1, "lifecycle": 0.1, "security": 0.1, "open_decision": 0.1, "mechanical": 0.9}},
		"Retry the same lane":   {answers: map[string]float64{"contract": 0.9, "lifecycle": 0.1, "security": 0.1, "open_decision": 0.1, "mechanical": 0.1}},
		"Bump to a higher lane": {answers: map[string]float64{"contract": 0.1, "lifecycle": 0.1, "security": 0.9, "open_decision": 0.1, "mechanical": 0.1}},
		"Only one try":          {answers: map[string]float64{"contract": 0.1, "lifecycle": 0.1, "security": 0.1, "open_decision": 0.9, "mechanical": 0.1}},
	})
	return root, plan, journals, server, record
}

func TestClassifyBenchV2Lines(t *testing.T) {
	root, plan, journals, server, _ := classifyV2Fixture(t)
	var stdout, stderr strings.Builder
	if err := run([]string{"judge", "classify", "bench", "--rubric", "v2", "--plan", plan, "--journals", journals, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr); err != nil {
		t.Fatalf("bench v2: %v\nstderr: %s", err, stderr.String())
	}
	for _, want := range []string{"task_1 plan=low judge=low", "task_2 plan=medium judge=medium", "task_3 plan=medium judge=high", "task_4 plan=high judge=critical", "contract=no confidence=0.90 defaulted=false", "mechanical=yes confidence=0.90 defaulted=false", "outcome=candidate", "outcome=retried", "outcome=escalated", "outcome=failed"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestClassifyBenchV2Summary(t *testing.T) {
	root, plan, journals, server, _ := classifyV2Fixture(t)
	unknown := writeBenchPlan(t, root, "unknown-bench.md", strings.Replace(classifyTestPlan, "# Plan — Classify bench", "# Plan — Unknown bench", 1))
	var stdout, stderr strings.Builder
	if err := run([]string{"judge", "classify", "bench", "--rubric", "v2", "--plan", plan, "--plan", unknown, "--journals", journals, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr); err != nil {
		t.Fatalf("bench v2: %v\nstderr: %s", err, stderr.String())
	}
	for _, want := range []string{"sufficient=2 insufficient=2 unknown=3", "economy=2/2 (1.00) PASS", "safety=2/2 (1.00) PASS reported only", "balance=1.00 PASS", "discrimination=", "agreement=", "unavailable_answers="} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("summary missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestClassifyBenchV2JSON(t *testing.T) {
	root, plan, journals, server, _ := classifyV2Fixture(t)
	var stdout, stderr strings.Builder
	if err := run([]string{"judge", "classify", "bench", "--rubric", "v2", "--json", "--plan", plan, "--journals", journals, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr); err != nil {
		t.Fatalf("bench v2 JSON: %v\nstderr: %s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want four tasks and summary:\n%s", len(lines), stdout.String())
	}
	var task struct {
		JudgeLane string `json:"judge_lane"`
		Answers   map[string]struct {
			Answer     bool    `json:"answer"`
			Confidence float64 `json:"confidence"`
			Defaulted  bool    `json:"defaulted"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &task); err != nil {
		t.Fatal(err)
	}
	if task.JudgeLane != "low" || !task.Answers["mechanical"].Answer || task.Answers["mechanical"].Confidence != 0.9 || task.Answers["mechanical"].Defaulted {
		t.Errorf("task = %+v", task)
	}
	var summary struct {
		Summary struct {
			Economy struct {
				Share float64 `json:"share"`
				Pass  bool    `json:"pass"`
			} `json:"economy"`
			Safety struct {
				Share        float64 `json:"share"`
				ReportedOnly bool    `json:"reported_only"`
			} `json:"safety"`
			Balance struct {
				Share float64 `json:"share"`
				Pass  bool    `json:"pass"`
			} `json:"balance"`
			Sufficient   int `json:"sufficient"`
			Insufficient int `json:"insufficient"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(lines[4]), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Summary.Sufficient != 2 || summary.Summary.Insufficient != 2 || summary.Summary.Economy.Share != 1 || !summary.Summary.Economy.Pass || summary.Summary.Safety.Share != 1 || !summary.Summary.Safety.ReportedOnly || summary.Summary.Balance.Share != 1 || !summary.Summary.Balance.Pass {
		t.Errorf("summary = %+v", summary.Summary)
	}
}

func TestClassifyBenchV2LabelFree(t *testing.T) {
	root, plan, journals, server, record := classifyV2Fixture(t)
	var stdout, stderr strings.Builder
	if err := run([]string{"judge", "classify", "bench", "--rubric", "v2", "--plan", plan, "--journals", journals, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr); err != nil {
		t.Fatalf("bench v2: %v\nstderr: %s", err, stderr.String())
	}
	assertBenchBodiesLabelFree(t, record, 4)
	for i, body := range record.bodies {
		questions, ok := body["questions"].(map[string]any)
		if !ok || len(questions) != 5 {
			t.Fatalf("call %d questions = %#v", i, body["questions"])
		}
		for _, key := range []string{"contract", "lifecycle", "security", "open_decision", "mechanical"} {
			question, ok := questions[key].(map[string]any)
			if !ok || question["type"] != "noul" {
				t.Errorf("call %d question %s = %#v", i, key, questions[key])
			}
		}
		wire, _ := json.Marshal(body)
		if strings.Contains(string(wire), `"complexity":"low"`) || strings.Contains(string(wire), `"complexity":"medium"`) || strings.Contains(string(wire), `"complexity":"high"`) {
			t.Errorf("call %d carries plan lane: %s", i, wire)
		}
	}
}
