package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/batuta-ai/core/judge"
)

const judgeTestAnswer = `{"model":"jev-1.13.0","answers":{"ok":{"type":"noul","noul":0.93}},"usage":{"input_tokens":12,"output_tokens":3}}`

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
