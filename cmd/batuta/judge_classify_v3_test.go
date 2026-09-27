package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/batuta-ai/core/classify"
)

const benchV3TestPlan = `# Plan — V3 bench
<!-- inputs: profile.md@sha256:1a2b3c4d5e6f routing.md@sha256:0f0e0d0c0b0a -->

**Goal:** Score packets.
**Created:** 2026-09-27 · **Status:** approved

## Tasks
- [ ] 1. Change the Exported API — backend/low
      Scope: api/contract.go
      Accept: Go tests pass → go test ./...
- [ ] 2. Document behavior — docs/medium
      Scope: docs/readme.md
      Accept: documentation exists → test -f docs/readme.md

## Decisions and context
The first task changes Exported.
`

func benchV3Fixture(t *testing.T, slug string) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".batuta", "judge.json"), []byte(`{"provider":"typesafe","model":"jev-test","key_env":"PATH"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "contract.go"), []byte("package api\nfunc Exported() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planDir := filepath.Join(root, ".batuta", "plans", "done")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(planDir, slug+".md")
	if err := os.WriteFile(plan, []byte(benchV3TestPlan), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, plan
}

func benchV3Server(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	bodies := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request: %v", err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-test","answers":{"contract":{"type":"choice","choice":"exported_change","confidence":0.95,"probabilities":{"exported_change":0.95,"internal_only":0.03,"insufficient":0.02}}},"usage":{"input_tokens":19}}`))
	}))
	t.Cleanup(server.Close)
	return server, &bodies
}

func benchV3Run(t *testing.T, root, plan, url string, extra ...string) (string, error) {
	t.Helper()
	args := []string{"judge", "classify", "bench", "--plan", plan, "--rubric", "v3", "--workspace", root, "--base-url", url}
	args = append(args, extra...)
	var stdout, stderr strings.Builder
	err := run(args, &stdout, &stderr)
	return stdout.String(), err
}

func TestClassifyBenchV3Flags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"v1 split", []string{"--rubric", "v1", "--split", "test"}},
		{"v1 empty split", []string{"--rubric", "v1", "--split", ""}},
		{"v2 rule", []string{"--rubric", "v2", "--rule", "rule.json"}},
		{"v2 empty rule", []string{"--rubric", "v2", "--rule", ""}},
		{"invalid split", []string{"--rubric", "v3", "--split", "other"}},
		{"invalid rubric", []string{"--rubric", "v4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := append([]string{"--plan", "missing.md"}, tc.args...)
			err := runJudgeClassifyBench(args, &strings.Builder{}, &strings.Builder{})
			if err == nil || !strings.Contains(err.Error(), "usage: batuta judge classify bench") {
				t.Fatalf("args %v: %v", args, err)
			}
		})
	}
}

func TestClassifyBenchV3Split(t *testing.T) {
	t.Parallel()
	root, plan := benchV3Fixture(t, "split-a")
	server, bodies := benchV3Server(t)
	want := classify.SplitV3("split-a")
	output, err := benchV3Run(t, root, plan, server.URL, "--split", want, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(output, `"plan_slug":"split-a"`) != 2 || !strings.Contains(output, `"split":"`+want+`"`) || len(*bodies) != 1 {
		t.Fatalf("split output/calls = %s / %d", output, len(*bodies))
	}
	other := "test"
	if want == "test" {
		other = "calibrate"
	}
	output, err = benchV3Run(t, root, plan, server.URL, "--split", other, "--json")
	if err != nil || strings.Contains(output, `"plan_slug"`) {
		t.Fatalf("other split = %q, %v", output, err)
	}
}

func TestClassifyBenchV3Rule(t *testing.T) {
	t.Parallel()
	root, plan := benchV3Fixture(t, "rule-a")
	server, _ := benchV3Server(t)
	rule := filepath.Join(root, "rule.json")
	if err := os.WriteFile(rule, []byte(`{"FHigh":4,"FLow":1,"DocsLow":true,"Threshold":0.7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := benchV3Run(t, root, plan, server.URL, "--rule", rule, "--json")
	if err != nil || !strings.Contains(output, `"threshold":0.7`) {
		t.Fatalf("valid rule = %q, %v", output, err)
	}
	if err := os.WriteFile(rule, []byte(`{"FHigh":99,"FLow":1,"DocsLow":true,"Threshold":0.7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := benchV3Run(t, root, plan, server.URL, "--rule", rule); err == nil {
		t.Fatal("invalid rule accepted")
	}
}

func TestClassifyBenchV3Record(t *testing.T) {
	t.Parallel()
	root, plan := benchV3Fixture(t, "record-a")
	server, _ := benchV3Server(t)
	output, err := benchV3Run(t, root, plan, server.URL, "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"plan_lane":"low"`, `"code_lane":"low"`, `"judge_lane":"medium"`, `"open_marker":false`, `"scope":`, `"contract":`, `"found":true`, `"size":1`, `"choice":"exported_change"`, `"probabilities":`, `"status":"firm"`, `"outcome":"unknown"`} {
		if !strings.Contains(output, field) {
			t.Errorf("missing %s in %s", field, output)
		}
	}
}

func TestClassifyBenchV3NoPacketNoCall(t *testing.T) {
	t.Parallel()
	root, plan := benchV3Fixture(t, "no-packet-a")
	server, bodies := benchV3Server(t)
	output, err := benchV3Run(t, root, plan, server.URL, "--json")
	if err != nil || len(*bodies) != 1 || !strings.Contains(output, `"code_lane":"medium","judge_lane":"medium"`) {
		t.Fatalf("no packet = %s / calls %d / %v", output, len(*bodies), err)
	}
}

func TestClassifyBenchV3PlanRoot(t *testing.T) {
	t.Parallel()
	root, plan := benchV3Fixture(t, "root-a")
	server, bodies := benchV3Server(t)
	_, err := benchV3Run(t, root, plan, server.URL)
	if err != nil || len(*bodies) != 1 {
		t.Fatalf("root packet calls = %d, %v", len(*bodies), err)
	}
}

func TestClassifyBenchV3Summary(t *testing.T) {
	t.Parallel()
	root, plan := benchV3Fixture(t, "summary-a")
	server, _ := benchV3Server(t)
	output, err := benchV3Run(t, root, plan, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"economy=", "safety=", "discrimination=", "balance=", "balance_delta=", "contract packets=", "security packets=", "input_tokens=19", "unknown=2"} {
		if !strings.Contains(output, field) {
			t.Errorf("missing %q in %s", field, output)
		}
	}
}

func TestClassifyBenchV3UnavailableCall(t *testing.T) {
	t.Parallel()
	root, plan := benchV3Fixture(t, "unavailable-a")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	output, err := benchV3Run(t, root, plan, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !strings.Contains(output, "contract packets=1 calls=1 firm=0 insufficient=0 below_threshold=0 unavailable=1") ||
		!strings.Contains(output, "security=packet:false,size:0,status:not_asked") ||
		!strings.Contains(output, "security packets=0 calls=0 firm=0 insufficient=0 below_threshold=0 unavailable=0") ||
		!strings.Contains(output, "code=low judge=low") {
		t.Fatalf("calls=%d output=%s", calls.Load(), output)
	}
}

func TestClassifyBenchV3JSON(t *testing.T) {
	t.Parallel()
	root, plan := benchV3Fixture(t, "json-a")
	server, _ := benchV3Server(t)
	output, err := benchV3Run(t, root, plan, server.URL, "--json")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d: %s", len(lines), output)
	}
	var summary map[string]map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &summary); err != nil || summary["summary"]["code"] == nil || summary["summary"]["judge"] == nil {
		t.Fatalf("summary = %v, %v", summary, err)
	}
}

func TestClassifyBenchV3LabelFree(t *testing.T) {
	t.Parallel()
	root, plan := benchV3Fixture(t, "label-a")
	server, bodies := benchV3Server(t)
	_, err := benchV3Run(t, root, plan, server.URL)
	if err != nil || len(*bodies) != 1 {
		t.Fatalf("calls = %d, %v", len(*bodies), err)
	}
	encoded, _ := json.Marshal((*bodies)[0])
	if strings.Contains(string(encoded), "plan_lane") || strings.Contains(string(encoded), "plan_complexity") || strings.Contains(string(encoded), "\"low\"") {
		t.Fatalf("plan lane leaked: %s", encoded)
	}
}
