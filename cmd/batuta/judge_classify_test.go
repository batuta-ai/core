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

	"github.com/batuta-ai/core/judge"
)

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
