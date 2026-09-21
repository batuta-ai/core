package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/loop"
)

const judgeTestAnswer = `{"model":"jev-1.13.0","answers":{"ok":{"type":"noul","noul":0.93}},"usage":{"input_tokens":12,"output_tokens":3}}`

const judgeTestReplayAnswer = `{"model":"jev-1.13.0","answers":{"c1_relation":{"type":"choice","choice":"proof_failed","confidence":0.93,"probabilities":{"supported":0.02,"proof_failed":0.93,"verifier_incomplete":0.00,"unverifiable":0.05}},"c1_material":{"type":"noul","noul":0.93}},"usage":{"input_tokens":12,"output_tokens":3}}`

// judgeTestServer answers every request with status and body, recording the
// wire facts of the last request.
func judgeTestServer(t *testing.T, status int, body string) (*httptest.Server, *judgeCall) {
	t.Helper()
	call := &judgeCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call.method = r.Method
		call.path = r.URL.Path
		call.authorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&call.body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, call
}

type judgeCall struct {
	method        string
	path          string
	authorization string
	body          map[string]any
}

// judgeTestWorkspace writes .batuta/judge.json pointing at a fake key
// environment variable and returns the workspace root. An empty config
// writes no file, so the judge stays off.
func judgeTestWorkspace(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	if config != "" {
		if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".batuta", "judge.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BATUTA_JUDGE", "")
	t.Setenv("JUDGE_TEST_KEY", "test-key")
	return root
}

func TestJudgeAskCommand(t *testing.T) {
	server, call := judgeTestServer(t, http.StatusOK, judgeTestAnswer)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	state := filepath.Join(root, "state.json")
	if err := os.WriteFile(state, []byte(`{"connection":"up"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	questions := filepath.Join(root, "questions.json")
	if err := os.WriteFile(questions, []byte(`{"ok":{"type":"noul","instructions":"The state says the connection works."}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "ask", "--state-file", state, "--questions-file", questions,
		"--decision", "manual", "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge ask = %v\nstderr: %s", err, stderr.String())
	}
	var response judge.Response
	if err := json.Unmarshal([]byte(stdout.String()), &response); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if response.Model != "jev-1.13.0" {
		t.Fatalf("model = %q, want jev-1.13.0", response.Model)
	}
	answer, ok := response.Answers["ok"]
	if !ok || answer.Type != judge.QuestionNoul || answer.Noul != 0.93 {
		t.Fatalf("answer for ok = %#v", answer)
	}
	if response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if call.method != http.MethodPost || call.path != "/v1/systemone" {
		t.Fatalf("request = %s %s, want POST /v1/systemone", call.method, call.path)
	}
	if call.authorization != "Bearer test-key" {
		t.Fatalf("authorization = %q", call.authorization)
	}
	if call.body["model"] != "jev-test" {
		t.Fatalf("request model = %#v", call.body["model"])
	}
	if state, ok := call.body["state"].(map[string]any); !ok || state["connection"] != "up" {
		t.Fatalf("request state = %#v", call.body["state"])
	}
	questionsSent, ok := call.body["questions"].(map[string]any)
	if !ok || len(questionsSent) != 1 {
		t.Fatalf("request questions = %#v", call.body["questions"])
	}
}

func TestJudgeAskUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  string
		withKey bool
		reason  string
	}{
		{name: "judge off", config: "", withKey: true, reason: judge.ReasonJudgeOff},
		{name: "key missing", config: `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`, withKey: false, reason: judge.ReasonKeyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := judgeTestWorkspace(t, tc.config)
			if !tc.withKey {
				t.Setenv("JUDGE_TEST_KEY", "")
			}
			state := filepath.Join(root, "state.json")
			if err := os.WriteFile(state, []byte(`"connection check"`), 0o600); err != nil {
				t.Fatal(err)
			}
			questions := filepath.Join(root, "questions.json")
			if err := os.WriteFile(questions, []byte(`{"ok":{"type":"noul","instructions":"works?"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr strings.Builder
			err := run([]string{"judge", "ask", "--state-file", state, "--questions-file", questions,
				"--workspace", root}, &stdout, &stderr)
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 2 {
				t.Fatalf("judge ask = %v, want exit 2", err)
			}
			if !strings.Contains(stderr.String(), tc.reason) {
				t.Fatalf("stderr = %q, want reason %q", stderr.String(), tc.reason)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing", stdout.String())
			}
		})
	}
}

func TestJudgeProbeCommand(t *testing.T) {
	t.Run("answers", func(t *testing.T) {
		server, call := judgeTestServer(t, http.StatusOK, judgeTestAnswer)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		var stdout, stderr strings.Builder
		err := run([]string{"judge", "probe", "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("judge probe = %v\nstderr: %s", err, stderr.String())
		}
		var response judge.Response
		if err := json.Unmarshal([]byte(stdout.String()), &response); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
		if state, ok := call.body["state"].(string); !ok || state != "connection check" {
			t.Fatalf("request state = %#v", call.body["state"])
		}
		questions, ok := call.body["questions"].(map[string]any)
		if !ok || len(questions) != 1 {
			t.Fatalf("request questions = %#v", call.body["questions"])
		}
		question, ok := questions["ok"].(map[string]any)
		if !ok || question["type"] != "noul" || question["instructions"] != "The state says the connection works." {
			t.Fatalf("probe question = %#v", question)
		}
	})
	t.Run("auto reports provider", func(t *testing.T) {
		server, _ := judgeTestServer(t, http.StatusOK, judgeTestAnswer)
		root := judgeTestWorkspace(t, `{"provider":"auto"}`)
		t.Setenv("TYPESAFE_API_KEY", "ts-key")
		t.Setenv("AI_GATEWAY_API_KEY", "gw-key")
		t.Setenv("OPENROUTER_API_KEY", "or-key")
		var stdout, stderr strings.Builder
		err := run([]string{"judge", "probe", "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("judge probe = %v\nstderr: %s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "provider: typesafe") {
			t.Fatalf("stdout = %q, want answering provider typesafe", stdout.String())
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		server, _ := judgeTestServer(t, http.StatusInternalServerError, `{"error":"no"}`)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		var stdout, stderr strings.Builder
		err := run([]string{"judge", "probe", "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
		var exit *ExitError
		if !errors.As(err, &exit) || exit.Code != 2 {
			t.Fatalf("judge probe = %v, want exit 2", err)
		}
		if !strings.Contains(stderr.String(), judge.ReasonServerError) {
			t.Fatalf("stderr = %q, want reason %q", stderr.String(), judge.ReasonServerError)
		}
	})
	t.Run("invalid config", func(t *testing.T) {
		server, _ := judgeTestServer(t, http.StatusOK, judgeTestAnswer)
		root := judgeTestWorkspace(t, `{"provider":"nonsense","model":"jev-test"}`)
		var stdout, stderr strings.Builder
		err := run([]string{"judge", "probe", "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
		var exit *ExitError
		if err == nil || errors.As(err, &exit) {
			t.Fatalf("judge probe = %v, want a plain exit-1 config error", err)
		}
	})
}

func TestJudgeAskRejectsInvalidArguments(t *testing.T) {
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	state := filepath.Join(root, "state.json")
	if err := os.WriteFile(state, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, "broken.json")
	if err := os.WriteFile(broken, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	questions := filepath.Join(root, "questions.json")
	if err := os.WriteFile(questions, []byte(`{"ok":{"type":"noul","instructions":"works?"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing state file flag", []string{"judge", "ask", "--questions-file", questions, "--workspace", root}},
		{"missing questions file flag", []string{"judge", "ask", "--state-file", state, "--workspace", root}},
		{"missing state file", []string{"judge", "ask", "--state-file", filepath.Join(root, "nope.json"), "--questions-file", questions, "--workspace", root}},
		{"invalid state json", []string{"judge", "ask", "--state-file", broken, "--questions-file", questions, "--workspace", root}},
		{"missing explicit config", []string{"judge", "ask", "--state-file", state, "--questions-file", questions, "--workspace", root, "--config", filepath.Join(root, "missing.json")}},
		{"positional argument", []string{"judge", "ask", "--state-file", state, "--questions-file", questions, "--workspace", root, "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			err := run(tc.args, &stdout, &stderr)
			var exit *ExitError
			if err == nil || errors.As(err, &exit) {
				t.Fatalf("judge ask = %v, want a plain usage or config error", err)
			}
		})
	}
}

func TestJudgeAskInvalidQuestionsJSON(t *testing.T) {
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	state := filepath.Join(root, "state.json")
	if err := os.WriteFile(state, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	questions := filepath.Join(root, "questions.json")
	if err := os.WriteFile(questions, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "ask", "--state-file", state, "--questions-file", questions, "--workspace", root}, &stdout, &stderr)
	if err == nil {
		t.Fatal("judge ask accepted a non-object questions file")
	}
}

// judgeReplayFixture writes a delivery journal with one opened record and
// one recorded attempt per execution in attempts, plus each attempt's run
// log unless runsMissing names its execution. It returns the journal path
// and the run-log directory.
func judgeReplayFixture(t *testing.T, root string, attempts []map[string]any, runsMissing ...int) (string, string) {
	t.Helper()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	delivery := "greetings-20260906-040001"
	opened, err := json.Marshal(map[string]any{
		"slug":  "greetings",
		"tasks": []map[string]any{{"task_id": "task_1", "title": "Greet once"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(delivery, journal.Record{Kind: loop.KindOpened, Detail: opened}); err != nil {
		t.Fatal(err)
	}
	for index, attempt := range attempts {
		execution := attempt["execution"].(int)
		finishedDetail := map[string]any{
			"execution": execution, "exit_code": 0, "finished": true, "tree_changed": attempt["tree_changed"],
		}
		if base, ok := attempt["base_sha"].(string); ok {
			finishedDetail["base_head_sha"] = base
		}
		finished, err := json.Marshal(finishedDetail)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(delivery, journal.Record{Kind: loop.KindFinished, TaskID: "task_1", Detail: finished}); err != nil {
			t.Fatal(err)
		}
		report := gates.Report{
			TaskID: "task_1", Execution: execution,
			Finished: gates.Verdict{Name: "finished", Pass: true, Signal: "exit 0"},
			Tree:     gates.Verdict{Name: "tree", Pass: attempt["tree_changed"] == true, Signal: "the worktree differs from the attempt's base"},
			Tests:    gates.Verdict{Name: "tests", Pass: true, Signal: "`go test ./...` passed"},
			Scope:    gates.Verdict{Name: "scope", Pass: true, Signal: "within Scope"},
			Proofs: []gates.Verdict{
				{Name: "proof 1", Pass: true, Signal: "a greeting exists — `test -f out/1.txt` passed"},
			},
			Verifier: &gates.Verdict{Name: "verifier", Pass: true, Signal: "1 criterion(s) DONE"},
			Passed:   true,
		}
		if paths, ok := attempt["paths"].([]string); ok {
			report.Scope.Paths = paths
		}
		if detail, ok := attempt["scope_detail"].(string); ok {
			report.Scope.Pass = false
			report.Scope.Detail = detail
			report.Scope.Signal = "1 path(s) outside Scope"
			report.Passed = false
		}
		detail, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(delivery, journal.Record{Kind: loop.KindGates, TaskID: "task_1", Detail: detail}); err != nil {
			t.Fatal(err)
		}
		outcome, err := json.Marshal(attempt["outcome"])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(delivery, journal.Record{Kind: attempt["kind"].(journal.Kind), TaskID: "task_1", Detail: outcome}); err != nil {
			t.Fatal(err)
		}
		if sha, ok := attempt["snapshot_sha"].(string); ok {
			snap, err := json.Marshal(map[string]any{"execution": execution, "sha": sha})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append(delivery, journal.Record{Kind: loop.KindSnapshot, TaskID: "task_1", Detail: snap}); err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Contains(runsMissing, execution) {
			log := fmt.Sprintf("# exit 0 · finished true · timed out false · rate limited false · %d\n\n## stdout\n\n%s\n\n## stderr\n\n",
				index+1, attempt["log"])
			if err := os.MkdirAll(filepath.Join(root, ".batuta", "runs"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".batuta", "runs",
				fmt.Sprintf("2026-09-06-greetings-task-1-e%d.out.log", execution)), []byte(log), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return filepath.Join(root, journal.Dir, delivery+".jsonl"), filepath.Join(root, ".batuta", "runs")
}

func judgeReplayRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr strings.Builder
	err := run(append([]string{"judge", "replay"}, args...), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestJudgeReplayCommand(t *testing.T) {
	server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
		{"execution": 1, "tree_changed": false, "kind": loop.KindFailure,
			"outcome": map[string]any{"execution": 1, "blocker": "already_satisfied", "blocked": true},
			"log":     "wrote nothing\nTASK 1: DONE"},
		{"execution": 2, "tree_changed": true, "kind": loop.KindCandidate,
			"outcome": map[string]any{"execution": 2, "commit": "sha"},
			"log":     "BATUTA-PROGRESS 1 START\nBATUTA-PROGRESS 1 DONE"},
	})

	stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
	if err != nil {
		t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want one line per attempt", stdout)
	}
	if want := "task_1 e1 outcome=already_satisfied asked=true claims=1 changed_paths=0 code_contradicted=0 judge_contradicted=1 uncertain=0 max_contradicted=0.93 material_max=0.93 flagged=true provider=typesafe"; lines[0] != want {
		t.Fatalf("first line = %q, want %q", lines[0], want)
	}
	if want := "task_1 e2 outcome=candidate asked=true claims=1 changed_paths=unknown code_contradicted=0 judge_contradicted=1 uncertain=0 max_contradicted=0.93 material_max=0.93 flagged=true provider=typesafe"; lines[1] != want {
		t.Fatalf("second line = %q, want %q", lines[1], want)
	}
	if call.method != http.MethodPost || call.path != "/v1/systemone" {
		t.Fatalf("request = %s %s, want POST /v1/systemone", call.method, call.path)
	}
	if call.body["model"] != "jev-test" {
		t.Fatalf("request model = %#v", call.body["model"])
	}
	state, ok := call.body["state"].(map[string]any)
	if !ok {
		t.Fatalf("request state = %#v", call.body["state"])
	}
	if state["task"] == nil {
		t.Fatalf("request state = %#v, want the task object", state)
	}
	task, _ := state["task"].(map[string]any)
	if task["id"] != "task_1" || task["title"] != "Greet once" {
		t.Fatalf("request state.task = %#v, want the task summary", task)
	}
	if _, ok := state["executor_report"]; ok {
		t.Fatalf("request state carries the v1 executor report: %#v", state)
	}
	if note, _ := state["note"].(string); note == "" {
		t.Fatalf("request state missing the untrusted-data note: %#v", state)
	}
	questions, ok := call.body["questions"].(map[string]any)
	if !ok || len(questions) != 2 {
		t.Fatalf("request questions = %#v, want relation and material per unsettled claim", call.body["questions"])
	}
	question, ok := questions["c1_relation"].(map[string]any)
	if !ok || question["type"] != "choice" {
		t.Fatalf("c1_relation = %#v", question)
	}
	instructions, _ := question["instructions"].(string)
	if !strings.Contains(instructions, "claims.c1.evidence") || !strings.Contains(instructions, "claims.c1.claim") {
		t.Fatalf("c1_relation instructions = %#v", question["instructions"])
	}
	material, ok := questions["c1_material"].(map[string]any)
	if !ok || material["type"] != "noul" {
		t.Fatalf("c1_material = %#v", questions["c1_material"])
	}
}

func TestJudgeReplayJSON(t *testing.T) {
	server, _ := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
		{"execution": 1, "tree_changed": false, "kind": loop.KindFailure,
			"outcome": map[string]any{"execution": 1, "blocker": "already_satisfied", "blocked": true},
			"log":     "wrote `out/1.txt`"},
		{"execution": 2, "tree_changed": true, "kind": loop.KindCandidate,
			"outcome": map[string]any{"execution": 2, "commit": "sha"},
			"log":     "BATUTA-PROGRESS 1 DONE"},
	})

	stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL, "--json")
	if err != nil {
		t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want one object per attempt", stdout)
	}
	var attempts [2]struct {
		TaskID          string            `json:"task_id"`
		Execution       int               `json:"execution"`
		Outcome         string            `json:"outcome"`
		Asked           bool              `json:"asked"`
		Flagged         bool              `json:"flagged"`
		MaxContradicted float64           `json:"max_contradicted"`
		MaterialMax     float64           `json:"material_max"`
		Provider        string            `json:"provider"`
		Claims          []replayClaim     `json:"claims"`
		Uncertain       []replayUncertain `json:"uncertain"`
	}
	for index, line := range lines {
		if err := json.Unmarshal([]byte(line), &attempts[index]); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", index+1, err, line)
		}
	}
	first, second := attempts[0], attempts[1]
	if first.TaskID != "task_1" || first.Execution != 1 || first.Outcome != "already_satisfied" {
		t.Fatalf("first object = %#v", first)
	}
	if first.Asked || !first.Flagged || first.MaxContradicted != 0 || first.MaterialMax != 0 || first.Provider != "typesafe" {
		t.Fatalf("first object = %#v, want a code-settled attempt with no judge call", first)
	}
	if len(first.Claims) != 1 || first.Claims[0].Kind != "path" || first.Claims[0].Text != "out/1.txt" ||
		first.Claims[0].Source != "code" || first.Claims[0].Choice != "contradicted" || first.Claims[0].Confidence != 1 ||
		first.Claims[0].Material != 0 {
		t.Fatalf("first claims = %#v, want the code-contradicted path claim", first.Claims)
	}
	if len(first.Uncertain) != 0 {
		t.Fatalf("first uncertain = %#v", first.Uncertain)
	}
	if second.TaskID != "task_1" || second.Execution != 2 || second.Outcome != "candidate" {
		t.Fatalf("second object = %#v", second)
	}
	if !second.Asked || !second.Flagged || second.MaxContradicted != 0.93 || second.MaterialMax != 0.93 || second.Provider != "typesafe" {
		t.Fatalf("second object = %#v, want a judged attempt", second)
	}
	if len(second.Claims) != 1 || second.Claims[0].Kind != "criterion" || second.Claims[0].Text != "a greeting exists" ||
		second.Claims[0].Source != "judge" || second.Claims[0].Choice != "contradicted" || second.Claims[0].Confidence != 0.93 ||
		second.Claims[0].Material != 0.93 {
		t.Fatalf("second claims = %#v, want the judge-contradicted criterion claim", second.Claims)
	}
	if len(second.Uncertain) != 0 {
		t.Fatalf("second uncertain = %#v", second.Uncertain)
	}
}

func TestJudgeReplayNoCall(t *testing.T) {
	server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
		{"execution": 1, "tree_changed": false, "kind": loop.KindFailure,
			"outcome": map[string]any{"execution": 1, "blocker": "already_satisfied", "blocked": true},
			"log":     "wrote `out/1.txt`"},
	})

	stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
	if err != nil {
		t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
	}
	want := "task_1 e1 outcome=already_satisfied asked=false claims=1 changed_paths=0 code_contradicted=1 judge_contradicted=0 uncertain=0 max_contradicted=0.00 material_max=0.00 flagged=true provider=typesafe\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if call.method != "" {
		t.Fatalf("the judge was called (%s %s) for an attempt with no unsettled claims", call.method, call.path)
	}
}

func TestJudgeReplayMissingLog(t *testing.T) {
	server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
		{"execution": 1, "tree_changed": false, "kind": loop.KindFailure,
			"outcome": map[string]any{"execution": 1, "blocker": "already_satisfied"},
			"log":     "wrote nothing"},
	}, 1)

	stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
	if err != nil {
		t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
	}
	want := fmt.Sprintf("task_1 e1 skipped %s\n", filepath.Join(runs, "2026-09-06-greetings-task-1-e1.out.log"))
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if call.method != "" {
		t.Fatalf("the judge was called (%s %s) for a skipped attempt", call.method, call.path)
	}
}

func TestJudgeReplayReadOnly(t *testing.T) {
	server, _ := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
		{"execution": 1, "tree_changed": true, "kind": loop.KindCandidate,
			"outcome": map[string]any{"execution": 1, "commit": "sha"},
			"log":     "BATUTA-PROGRESS 1 DONE"},
	})
	logPath := filepath.Join(runs, "2026-09-06-greetings-task-1-e1.out.log")
	before := map[string]string{}
	for _, path := range []string{journalPath, logPath} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = string(content)
	}

	stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
	if err != nil {
		t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "outcome=candidate") {
		t.Fatalf("stdout = %q, want a judged attempt", stdout)
	}
	for path, want := range before {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != want {
			t.Fatalf("%s changed during replay:\nbefore: %q\nafter:  %q", path, want, string(content))
		}
	}
}

func TestJudgeReplayUnavailable(t *testing.T) {
	t.Run("judge off", func(t *testing.T) {
		root := judgeTestWorkspace(t, "")
		journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
			{"execution": 1, "tree_changed": false, "kind": loop.KindFailure,
				"outcome": map[string]any{"execution": 1, "blocker": "already_satisfied"},
				"log":     "wrote nothing"},
		})
		stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root)
		var exit *ExitError
		if !errors.As(err, &exit) || exit.Code != 2 {
			t.Fatalf("judge replay = %v, want exit 2", err)
		}
		if !strings.Contains(stderr, judge.ReasonJudgeOff) {
			t.Fatalf("stderr = %q, want reason %q", stderr, judge.ReasonJudgeOff)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want nothing", stdout)
		}
	})
	t.Run("server error before the first answer", func(t *testing.T) {
		server, _ := judgeTestServer(t, http.StatusInternalServerError, `{"error":"no"}`)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
			{"execution": 1, "tree_changed": false, "kind": loop.KindFailure,
				"outcome": map[string]any{"execution": 1, "blocker": "already_satisfied"},
				"log":     "wrote nothing\nTASK 1: DONE"},
		})
		_, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
		var exit *ExitError
		if !errors.As(err, &exit) || exit.Code != 2 {
			t.Fatalf("judge replay = %v, want exit 2", err)
		}
		if !strings.Contains(stderr, judge.ReasonServerError) {
			t.Fatalf("stderr = %q, want reason %q", stderr, judge.ReasonServerError)
		}
		if strings.Contains(stderr, `{"error":"no"}`) {
			t.Fatalf("stderr carries the provider error body: %q", stderr)
		}
	})
}

func TestJudgeReplayRejectsInvalidArguments(t *testing.T) {
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing journal flag", []string{"judge", "replay", "--workspace", root}},
		{"missing journal file", []string{"judge", "replay", "--journal", filepath.Join(root, "nope.jsonl"), "--workspace", root}},
		{"positional argument", []string{"judge", "replay", "--journal", filepath.Join(root, "nope.jsonl"), "--workspace", root, "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			err := run(tc.args, &stdout, &stderr)
			if err == nil {
				t.Fatalf("judge replay = nil, want a usage error")
			}
		})
	}
}

func TestJudgeReplayChangedPaths(t *testing.T) {
	t.Run("scope.paths", func(t *testing.T) {
		server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
			{"execution": 1, "tree_changed": true, "kind": loop.KindCandidate,
				"paths":        []string{"cmd/greet.go", "outside.txt"},
				"scope_detail": "outside.txt",
				"outcome":      map[string]any{"execution": 1, "commit": "sha"},
				"log":          "edited `cmd/greet.go`"},
		})
		stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
		if err != nil {
			t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
		}
		want := "task_1 e1 outcome=candidate asked=false claims=1 changed_paths=2 code_contradicted=0 judge_contradicted=0 uncertain=0 max_contradicted=0.00 material_max=0.00 flagged=false provider=typesafe\n"
		if stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
		if call.method != "" {
			t.Fatalf("the judge was called (%s %s) for a code-settled path claim", call.method, call.path)
		}
	})
	t.Run("candidate git diff", func(t *testing.T) {
		server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		base, commit := judgeReplayGitCommits(t, root)
		journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
			{"execution": 1, "tree_changed": true, "kind": loop.KindCandidate,
				"outcome": map[string]any{
					"execution": 1, "commit": commit,
					"evidence": map[string]any{"base_sha": base},
				},
				"log": "edited `out/1.txt`"},
		})
		stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
		if err != nil {
			t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
		}
		want := "task_1 e1 outcome=candidate asked=false claims=1 changed_paths=1 code_contradicted=0 judge_contradicted=0 uncertain=0 max_contradicted=0.00 material_max=0.00 flagged=false provider=typesafe\n"
		if stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
		if call.method != "" {
			t.Fatalf("the judge was called (%s %s) for a git-recovered path claim", call.method, call.path)
		}
	})
	t.Run("worktree snapshot git diff", func(t *testing.T) {
		server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		base, commit := judgeReplayGitCommits(t, root)
		journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
			{"execution": 1, "tree_changed": true, "kind": loop.KindFailure,
				"base_sha":     base,
				"snapshot_sha": commit,
				"outcome":      map[string]any{"execution": 1, "blocker": "tests_failed"},
				"log":          "edited `out/1.txt`"},
		})
		stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
		if err != nil {
			t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
		}
		want := "task_1 e1 outcome=tests_failed asked=false claims=1 changed_paths=1 code_contradicted=0 judge_contradicted=0 uncertain=0 max_contradicted=0.00 material_max=0.00 flagged=false provider=typesafe\n"
		if stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
		if call.method != "" {
			t.Fatalf("the judge was called (%s %s) for a snapshot-recovered path claim", call.method, call.path)
		}
	})
	t.Run("scope.paths wins over git", func(t *testing.T) {
		server, _ := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		base, commit := judgeReplayGitCommits(t, root)
		journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
			{"execution": 1, "tree_changed": true, "kind": loop.KindCandidate,
				"paths": []string{"cmd/greet.go"},
				"outcome": map[string]any{
					"execution": 1, "commit": commit,
					"evidence": map[string]any{"base_sha": base},
				},
				"log": "edited `cmd/greet.go`"},
		})
		stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
		if err != nil {
			t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
		}
		want := "task_1 e1 outcome=candidate asked=false claims=1 changed_paths=1 code_contradicted=0 judge_contradicted=0 uncertain=0 max_contradicted=0.00 material_max=0.00 flagged=false provider=typesafe\n"
		if stdout != want {
			t.Fatalf("stdout = %q, want %q — git would have contradicted cmd/greet.go", stdout, want)
		}
	})
}

func TestReplayEvidenceInputDiff(t *testing.T) {
	progressLog := "BATUTA-PROGRESS 1 START\nBATUTA-PROGRESS 1 DONE"
	assertDiff := func(t *testing.T, call *judgeCall, want string) {
		t.Helper()
		state, ok := call.body["state"].(map[string]any)
		if !ok {
			t.Fatalf("request state = %#v", call.body["state"])
		}
		diff, _ := state["diff"].(string)
		if !strings.Contains(diff, want) {
			t.Fatalf("state.diff = %q, want %q", diff, want)
		}
	}

	t.Run("candidate base and commit", func(t *testing.T) {
		server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		base, commit := judgeReplayGitCommits(t, root)
		journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
			{"execution": 1, "tree_changed": true, "kind": loop.KindCandidate,
				"outcome": map[string]any{
					"execution": 1, "commit": commit,
					"evidence": map[string]any{"base_sha": base},
				},
				"log": progressLog},
		})
		_, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
		if err != nil {
			t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
		}
		assertDiff(t, call, "+ok")
	})

	t.Run("falls back to base and snapshot", func(t *testing.T) {
		server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		base, commit := judgeReplayGitCommits(t, root)
		journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
			{"execution": 1, "tree_changed": true, "kind": loop.KindFailure,
				"base_sha":     base,
				"snapshot_sha": commit,
				"outcome":      map[string]any{"execution": 1, "blocker": "tests_failed"},
				"log":          progressLog},
		})
		_, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
		if err != nil {
			t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
		}
		assertDiff(t, call, "+ok")
	})

	t.Run("leaves the diff empty when neither pair resolves", func(t *testing.T) {
		server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
		root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
		journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
			{"execution": 1, "tree_changed": true, "kind": loop.KindCandidate,
				"outcome": map[string]any{"execution": 1, "commit": "sha"},
				"log":     progressLog},
		})
		_, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
		if err != nil {
			t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
		}
		state, ok := call.body["state"].(map[string]any)
		if !ok {
			t.Fatalf("request state = %#v", call.body["state"])
		}
		if diff, ok := state["diff"]; ok {
			t.Fatalf("state.diff = %q, want no diff without a resolvable pair", diff)
		}
	})
}

func judgeReplayGitCommits(t *testing.T, root string) (base, commit string) {
	t.Helper()
	git := mustGit(t)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
		{"config", "gc.autoDetach", "false"},
		{"config", "maintenance.auto", "false"},
	} {
		runGit(t, git, root, args...)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, root, "add", "tracked.txt")
	runGit(t, git, root, "commit", "-qm", "initial")
	base = strings.TrimSpace(runGit(t, git, root, "rev-parse", "HEAD"))
	if err := os.MkdirAll(filepath.Join(root, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "out", "1.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, root, "add", "out/1.txt")
	runGit(t, git, root, "commit", "-qm", "greeting")
	commit = strings.TrimSpace(runGit(t, git, root, "rev-parse", "HEAD"))
	return base, commit
}

func TestJudgeReplayUnknownPaths(t *testing.T) {
	server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
		{"execution": 1, "tree_changed": true, "kind": loop.KindCandidate,
			"outcome": map[string]any{"execution": 1, "commit": "sha"},
			"log":     "edited `out/1.txt`"},
	})
	stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
	if err != nil {
		t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
	}
	want := "task_1 e1 outcome=candidate asked=false claims=1 changed_paths=unknown code_contradicted=0 judge_contradicted=0 uncertain=0 max_contradicted=0.00 material_max=0.00 flagged=false provider=typesafe\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if call.method != "" {
		t.Fatalf("the judge was called (%s %s) for an unverifiable path claim", call.method, call.path)
	}
	jsonOut, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL, "--json")
	if err != nil {
		t.Fatalf("judge replay --json = %v\nstderr: %s", err, stderr)
	}
	var record replayJSONRecord
	if err := json.Unmarshal([]byte(jsonOut), &record); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, jsonOut)
	}
	if record.Asked || record.Flagged || len(record.Claims) != 1 {
		t.Fatalf("json record = %#v, want one code-settled unverifiable path claim", record)
	}
	claim := record.Claims[0]
	if claim.Kind != "path" || claim.Text != "out/1.txt" || claim.Source != "code" || claim.Choice != "unverifiable" {
		t.Fatalf("claim = %#v, want an unverifiable path claim, never contradicted", claim)
	}
}

func TestCapabilitiesListsJudge(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Commands, "judge") {
		t.Fatalf("capabilities.commands = %v, missing judge", got.Commands)
	}
	if !strings.Contains(usage, "batuta judge") {
		t.Fatal("usage is missing batuta judge")
	}
}
