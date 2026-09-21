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
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/loop"
)

const judgeTestAnswer = `{"model":"jev-1.13.0","answers":{"ok":{"type":"noul","noul":0.93}},"usage":{"input_tokens":12,"output_tokens":3}}`

const judgeTestReplayAnswer = `{"model":"jev-1.13.0","answers":{"claim_unsupported":{"type":"noul","noul":0.93},"verifier_contradicted":{"type":"noul","noul":0.88}},"usage":{"input_tokens":12,"output_tokens":3}}`

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
	for _, key := range []string{`"model"`, `"answers"`, `"usage"`, `"input_tokens"`, `"output_tokens"`, `"type"`, `"noul"`} {
		if !strings.Contains(stdout.String(), key) {
			t.Fatalf("stdout misses %s: %s", key, stdout.String())
		}
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
		lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
		if len(lines) != 1 {
			t.Fatalf("stdout = %q, want one success line", stdout.String())
		}
		fields := strings.Fields(lines[0])
		if len(fields) != 4 || fields[0] != "provider=typesafe" || fields[1] != "model=jev-1.13.0" || fields[3] != "tokens=12/3" {
			t.Fatalf("probe line = %q, want provider, model, latency and tokens", lines[0])
		}
		if _, err := strconv.Atoi(strings.TrimPrefix(fields[2], "ms=")); err != nil {
			t.Fatalf("probe latency %q is not milliseconds: %v", fields[2], err)
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
		line := strings.TrimSuffix(stdout.String(), "\n")
		if !strings.HasPrefix(line, "provider=typesafe model=jev-1.13.0 ms=") || !strings.HasSuffix(line, " tokens=12/3") {
			t.Fatalf("stdout = %q, want the success line naming the answering provider", stdout.String())
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
		finished, err := json.Marshal(map[string]any{
			"execution": execution, "exit_code": 0, "finished": true, "tree_changed": attempt["tree_changed"],
		})
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

// judgeLatencyMS asserts the remainder of line after prefix is a millisecond
// count and returns it.
func judgeLatencyMS(t *testing.T, line, prefix string) int {
	t.Helper()
	ms, err := strconv.Atoi(strings.TrimPrefix(line, prefix))
	if err != nil {
		t.Fatalf("latency in %q is not milliseconds: %v", line, err)
	}
	return ms
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
			"log":     "wrote cmd/greet.go\nBATUTA-PROGRESS 1 START\nBATUTA-PROGRESS 1 DONE"},
	})

	stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
	if err != nil {
		t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q, want one line per attempt and a totals line", stdout)
	}
	prefix0 := "task_1 e1 outcome=already_satisfied claim_unsupported=0.93 verifier_contradicted=0.88 provider=typesafe tokens=12/3 ms="
	if !strings.HasPrefix(lines[0], prefix0) {
		t.Fatalf("first line = %q, want %q<latency>", lines[0], prefix0)
	}
	judgeLatencyMS(t, lines[0], prefix0)
	prefix1 := "task_1 e2 outcome=candidate claim_unsupported=0.93 verifier_contradicted=0.88 provider=typesafe tokens=12/3 ms="
	if !strings.HasPrefix(lines[1], prefix1) {
		t.Fatalf("second line = %q, want %q<latency>", lines[1], prefix1)
	}
	judgeLatencyMS(t, lines[1], prefix1)
	if want := "attempts=2 asked=2 skipped=0 input_tokens=24 output_tokens=6"; lines[2] != want {
		t.Fatalf("totals line = %q, want %q", lines[2], want)
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
	if state["executor_report"] == "" {
		t.Fatalf("request state carries no executor report: %#v", state)
	}
	task, _ := state["task"].(map[string]any)
	if task["id"] != "task_1" || task["title"] != "Greet once" {
		t.Fatalf("request task = %#v", state["task"])
	}
	criteria, _ := state["criteria"].([]any)
	if len(criteria) != 1 {
		t.Fatalf("request criteria = %#v, want one from the recorded proof", state["criteria"])
	}
	questions, ok := call.body["questions"].(map[string]any)
	if !ok || len(questions) != 2 {
		t.Fatalf("request questions = %#v", call.body["questions"])
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
	want := fmt.Sprintf("task_1 e1 skipped %s\nattempts=1 asked=0 skipped=1 input_tokens=0 output_tokens=0\n", filepath.Join(runs, "2026-09-06-greetings-task-1-e1.out.log"))
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if call.method != "" {
		t.Fatalf("the judge was called (%s %s) for a skipped attempt", call.method, call.path)
	}
}

func TestJudgeReplayJSON(t *testing.T) {
	server, call := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
		{"execution": 1, "tree_changed": false, "kind": loop.KindFailure,
			"outcome": map[string]any{"execution": 1, "blocker": "already_satisfied", "blocked": true},
			"log":     "wrote nothing\nTASK 1: DONE"},
		{"execution": 2, "tree_changed": true, "kind": loop.KindCandidate,
			"outcome": map[string]any{"execution": 2, "commit": "sha"},
			"log":     "wrote cmd/greet.go\nBATUTA-PROGRESS 1 DONE"},
	}, 2)

	stdout, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL, "--json")
	if err != nil {
		t.Fatalf("judge replay = %v\nstderr: %s", err, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q, want one object per attempt and the totals object", stdout)
	}
	var attempt map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &attempt); err != nil {
		t.Fatalf("first line is not JSON: %v\n%s", err, lines[0])
	}
	for _, key := range []string{"task_id", "execution", "outcome", "answers", "provider", "model", "input_tokens", "output_tokens", "latency_ms"} {
		if _, ok := attempt[key]; !ok {
			t.Fatalf("first attempt misses %q: %s", key, lines[0])
		}
	}
	if attempt["task_id"] != "task_1" || attempt["outcome"] != "already_satisfied" ||
		attempt["provider"] != "typesafe" || attempt["model"] != "jev-1.13.0" {
		t.Fatalf("first attempt = %#v", attempt)
	}
	answers, ok := attempt["answers"].(map[string]any)
	if !ok || answers["claim_unsupported"].(map[string]any)["noul"] != 0.93 {
		t.Fatalf("first attempt answers = %#v", attempt["answers"])
	}
	if attempt["input_tokens"].(float64) != 12 || attempt["output_tokens"].(float64) != 3 {
		t.Fatalf("first attempt usage = %#v", attempt)
	}
	var skipped map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &skipped); err != nil {
		t.Fatalf("second line is not JSON: %v\n%s", err, lines[1])
	}
	want := map[string]any{"task_id": "task_1", "execution": float64(2), "outcome": "skipped", "skipped": filepath.Join(runs, "2026-09-06-greetings-task-1-e2.out.log")}
	if !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped attempt = %#v, want %#v", skipped, want)
	}
	var totals struct {
		Totals struct {
			Attempts     int `json:"attempts"`
			Asked        int `json:"asked"`
			Skipped      int `json:"skipped"`
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"totals"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &totals); err != nil {
		t.Fatalf("last line is not the totals object: %v\n%s", err, lines[2])
	}
	if totals.Totals != (struct {
		Attempts     int `json:"attempts"`
		Asked        int `json:"asked"`
		Skipped      int `json:"skipped"`
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}{2, 1, 1, 12, 3}) {
		t.Fatalf("totals = %+v, want attempts=2 asked=1 skipped=1 input_tokens=12 output_tokens=3", totals.Totals)
	}
	if call.method != http.MethodPost {
		t.Fatalf("request = %s %s, want POST for the judged attempt", call.method, call.path)
	}
}

func TestJudgeReplayReadOnly(t *testing.T) {
	server, _ := judgeTestServer(t, http.StatusOK, judgeTestReplayAnswer)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	journalPath, runs := judgeReplayFixture(t, root, []map[string]any{
		{"execution": 1, "tree_changed": true, "kind": loop.KindCandidate,
			"outcome": map[string]any{"execution": 1, "commit": "sha"},
			"log":     "wrote cmd/greet.go\nBATUTA-PROGRESS 1 DONE"},
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
				"log":     "wrote nothing"},
		})
		_, stderr, err := judgeReplayRun(t, "--journal", journalPath, "--runs", runs, "--workspace", root, "--base-url", server.URL)
		var exit *ExitError
		if !errors.As(err, &exit) || exit.Code != 2 {
			t.Fatalf("judge replay = %v, want exit 2", err)
		}
		if !strings.Contains(stderr, judge.ReasonServerError) {
			t.Fatalf("stderr = %q, want reason %q", stderr, judge.ReasonServerError)
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
