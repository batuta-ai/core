package executor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var streamDecoderNames = []string{
	"cursor-stream-json",
	"agy-stream-json",
	"codex-json",
	"claude-stream-json",
	"opencode-json",
}

func TestDecoderLookup(t *testing.T) {
	t.Parallel()
	for _, name := range streamDecoderNames {
		if LookupDecoder(name) == nil {
			t.Fatalf("LookupDecoder(%q) = nil", name)
		}
	}
}

func TestDecoderMessageBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		format  string
		message func(string) string
	}{
		{"codex", "codex-json", func(text string) string {
			return `{"type":"item.completed","item":{"type":"agent_message","text":` + quotedText(text) + `}}`
		}},
		{"claude", "claude-stream-json", func(text string) string {
			return `{"type":"assistant","message":{"content":[{"type":"text","text":` + quotedText(text) + `}]}}`
		}},
		{"cursor", "cursor-stream-json", func(text string) string {
			return `{"type":"assistant","message":{"content":[{"type":"text","text":` + quotedText(text) + `}]}}`
		}},
		{"opencode", "opencode-json", func(text string) string {
			return `{"type":"text","part":{"text":` + quotedText(text) + `}}`
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decoder := LookupDecoder(tc.format)
			for _, input := range []string{"first message", "already ended\n", ""} {
				want := input
				if input != "" && !strings.HasSuffix(input, "\n") {
					want += "\n"
				}
				if got := decoder.Decode(tc.message(input)); got != want {
					t.Errorf("Decode(%q) = %q, want %q", input, got, want)
				}
			}
			decoder = LookupDecoder(tc.format)
			got := decoder.Decode(tc.message("I will read the diff.")) + decoder.Decode(tc.message("<<<FINDINGS\n- finding"))
			if want := "I will read the diff.\n<<<FINDINGS\n- finding\n"; got != want {
				t.Errorf("consecutive messages = %q, want %q", got, want)
			}
		})
	}
}

func quotedText(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestAgyDecoderStepBoundary(t *testing.T) {
	t.Parallel()
	decoder := LookupDecoder("agy-stream-json")
	steps := []struct {
		line string
		want string
	}{
		{`{"event":"step_update","step_update":{"step_type":"agent_response","state":"ACTIVE","text_delta":"hello "}}`, "hello "},
		{`{"event":"step_update","step_update":{"step_type":"agent_response","state":"ACTIVE","text_delta":"world"}}`, "world"},
		{`{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE"}}`, "\n"},
		{`{"event":"step_update","step_update":{"step_type":"agent_response","state":"ACTIVE","text_delta":"next\n"}}`, "next\n"},
		{`{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE"}}`, ""},
		{`{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE","text_delta":"final"}}`, "final\n"},
	}
	var joined strings.Builder
	for _, step := range steps {
		got := decoder.Decode(step.line)
		if got != step.want {
			t.Errorf("Decode(%s) = %q, want %q", step.line, got, step.want)
		}
		joined.WriteString(got)
	}
	if got, want := joined.String(), "hello world\nnext\nfinal\n"; got != want {
		t.Errorf("joined steps = %q, want %q", got, want)
	}
}

func TestDecodersReplayFixtures(t *testing.T) {
	t.Parallel()
	for _, name := range streamDecoderNames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			decoder := LookupDecoder(name)
			if decoder == nil {
				t.Fatalf("LookupDecoder(%q) = nil", name)
			}
			payload, err := os.ReadFile(filepath.Join("testdata", "stream", name+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var text bytes.Buffer
			scanner := bufio.NewScanner(bytes.NewReader(payload))
			for scanner.Scan() {
				text.WriteString(decoder.Decode(scanner.Text()))
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			wantPayload, err := os.ReadFile(filepath.Join("testdata", "stream", name+".want.json"))
			if err != nil {
				t.Fatal(err)
			}
			var want struct {
				Text  string `json:"text"`
				Usage *Usage `json:"usage"`
			}
			if err := json.Unmarshal(wantPayload, &want); err != nil {
				t.Fatal(err)
			}
			if text.String() != want.Text {
				t.Fatalf("text = %q, want %q", text.String(), want.Text)
			}
			if !reflect.DeepEqual(decoder.Usage(), want.Usage) {
				t.Fatalf("usage = %#v, want %#v", decoder.Usage(), want.Usage)
			}
		})
	}
}

func TestDecodersReplayErrorFixtures(t *testing.T) {
	t.Parallel()
	fixtures := map[string]string{
		"agy-invalid-model":      "agy-stream-json",
		"claude-invalid-model":   "claude-stream-json",
		"claude-ok-haiku":        "claude-stream-json",
		"codex-invalid-model":    "codex-json",
		"opencode-invalid-model": "opencode-json",
		"opencode-zen-glm":       "opencode-json",
	}
	for fixture, format := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			decoder := LookupDecoder(format)
			payload, err := os.ReadFile(filepath.Join("testdata", "stream", "errors", fixture+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var text bytes.Buffer
			scanner := bufio.NewScanner(bytes.NewReader(payload))
			for scanner.Scan() {
				text.WriteString(decoder.Decode(scanner.Text()))
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			wantPayload, err := os.ReadFile(filepath.Join("testdata", "stream", "errors", fixture+".want.json"))
			if err != nil {
				t.Fatal(err)
			}
			var want struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(wantPayload, &want); err != nil {
				t.Fatal(err)
			}
			if text.String() != want.Text {
				t.Fatalf("text = %q, want %q", text.String(), want.Text)
			}
		})
	}
}

func TestClaudeDecoderErrorsAndLimits(t *testing.T) {
	decoder := LookupDecoder("claude-stream-json")
	if got := decoder.Decode(`{"type":"result","is_error":true,"api_error_status":503,"result":"service unavailable"}`); got != "provider error: api_error_status 503: service unavailable\n" {
		t.Fatalf("error = %q", got)
	}
	payload, err := os.ReadFile(filepath.Join("testdata", "stream", "errors", "claude-ok-haiku.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var allowed map[string]json.RawMessage
	for _, line := range bytes.Split(payload, []byte("\n")) {
		if bytes.Contains(line, []byte(`"type":"rate_limit_event"`)) {
			if err := json.Unmarshal(line, &allowed); err != nil {
				t.Fatal(err)
			}
			if got := decoder.Decode(string(line)); got != "" {
				t.Fatalf("allowed limit = %q", got)
			}
			break
		}
	}
	if allowed == nil {
		t.Fatal("missing real rate_limit_event")
	}
	var info map[string]json.RawMessage
	if err := json.Unmarshal(allowed["rate_limit_info"], &info); err != nil {
		t.Fatal(err)
	}
	// No rejected event was captured; change only status in the real allowed event.
	info["status"] = json.RawMessage(`"rejected"`)
	allowed["rate_limit_info"], err = json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := json.Marshal(allowed)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoder.Decode(string(rejected)); got != "provider limit: five_hour rejected resetsAt 1790348400\n" {
		t.Fatalf("rejected limit = %q", got)
	}
}

func TestCodexDecoderErrors(t *testing.T) {
	decoder := LookupDecoder("codex-json")
	cases := []struct{ line, want string }{
		{`{"type":"error","message":"server failed"}`, "provider error: server failed\n"},
		{`{"type":"turn.failed","error":{"message":"request failed"}}`, "provider error: request failed\n"},
		{`{"type":"item.completed","item":{"type":"error","message":"fallback metadata"}}`, "provider notice: fallback metadata\n"},
	}
	for _, tc := range cases {
		if got := decoder.Decode(tc.line); got != tc.want {
			t.Fatalf("Decode(%s) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestOpencodeDecoderErrors(t *testing.T) {
	decoder := LookupDecoder("opencode-json")
	cases := []struct{ line, want string }{
		{`{"type":"error","error":{"name":"APIError","data":{"statusCode":402,"message":"insufficient funds"}}}`, "provider error: APIError 402: insufficient funds\n"},
		{`{"type":"error","error":{"name":"UnknownError","data":{"message":"server failed"}}}`, "provider error: UnknownError: server failed\n"},
	}
	for _, tc := range cases {
		if got := decoder.Decode(tc.line); got != tc.want {
			t.Fatalf("Decode(%s) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestAgyDecoderErrors(t *testing.T) {
	decoder := LookupDecoder("agy-stream-json")
	if got := decoder.Decode(`{"event":"result","result":{"status":"ERROR","error":"bad model\nchoose another"}}`); got != "provider error: bad model choose another\n" {
		t.Fatalf("error = %q", got)
	}
	if got := decoder.Decode(`{"event":"result","result":{"status":"SUCCESS","error":""}}`); got != "" {
		t.Fatalf("successful result = %q", got)
	}
}

func TestDecoderUnknownLines(t *testing.T) {
	t.Parallel()
	for _, name := range streamDecoderNames {
		decoder := LookupDecoder(name)
		if decoder == nil {
			t.Fatalf("LookupDecoder(%q) = nil", name)
		}
		if got := decoder.Decode("not json"); got != "not json" {
			t.Fatalf("%s passthrough = %q, want %q", name, got, "not json")
		}
		if got := decoder.Decode(`{"type":"not-an-event","event":"nope"}`); got != "" {
			t.Fatalf("%s unknown event = %q, want empty", name, got)
		}
	}
}
