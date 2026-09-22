package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
)

func TestSubprocessAppendsGitConfigOverride(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	directory := t.TempDir()
	executable := filepath.Join(directory, "show-git-config")
	script := `#!/bin/sh
set -e
printf 'count=%s\nkey0=%s\nvalue0=%s\nkey1=%s\nvalue1=%s\n' \
  "$GIT_CONFIG_COUNT" "$GIT_CONFIG_KEY_0" "$GIT_CONFIG_VALUE_0" \
  "$GIT_CONFIG_KEY_1" "$GIT_CONFIG_VALUE_1"
git config --show-origin --get user.name
git config --show-origin --get commit.gpgsign
`
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	subprocess := NewSubprocess()
	subprocess.Environment = []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=loop",
	}
	result, err := subprocess.Execute(context.Background(), Adapter{}, Invocation{Executable: executable, Dir: directory}, 0)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	output := string(result.Stdout)
	for _, want := range []string{
		"count=2",
		"key0=user.name",
		"value0=loop",
		"key1=commit.gpgsign",
		"value1=false",
		"command line:\tloop",
		"command line:\tfalse",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("stdout missing %q:\n%s", want, output)
		}
	}
}

func TestRunDecodesStreamOutput(t *testing.T) {
	t.Parallel()
	payload, err := os.ReadFile(filepath.Join("testdata", "stream", "cursor-stream-json.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	wantPayload, err := os.ReadFile(filepath.Join("testdata", "stream", "cursor-stream-json.want.json"))
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
	want.Usage.Provenance = "cli/cursor-stream-json"

	mid := len(payload) / 2
	var log bytes.Buffer
	result := executeDecoded(t, Adapter{OutputDecoder: "cursor-stream-json"}, splitStreamRunner{chunks: [][]byte{payload[:mid], payload[mid:]}, stdout: payload}, &log)
	if string(result.Stdout) != want.Text {
		t.Fatalf("Stdout = %q, want %q", result.Stdout, want.Text)
	}
	if !bytes.Equal(result.RawStdout, payload) {
		t.Fatalf("RawStdout = %q, want fixture bytes", result.RawStdout)
	}
	if !reflect.DeepEqual(result.Usage, want.Usage) {
		t.Fatalf("Usage = %#v, want %#v", result.Usage, want.Usage)
	}
	if log.String() != want.Text {
		t.Fatalf("run log = %q, want %q", log.String(), want.Text)
	}
	assertProgressEvents(t, result.Progress, []ProgressEvent{{Criterion: 1, State: "START"}})
}

func TestRunDecodedOutputBounded(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("x", 120)
	line := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n")
	payload := make([]byte, 0, 2*outputLimit+2*len(line))
	for len(payload) <= 2*outputLimit {
		payload = append(payload, line...)
	}
	result := executeDecoded(t, Adapter{OutputDecoder: "cursor-stream-json"}, splitStreamRunner{
		chunks: [][]byte{payload[:len(payload)/2], payload[len(payload)/2:]},
		stdout: payload,
	}, nil)
	if len(result.RawStdout) != outputLimit {
		t.Fatalf("RawStdout length = %d, want %d", len(result.RawStdout), outputLimit)
	}
	if len(result.Stdout) > outputLimit {
		t.Fatalf("Stdout length = %d, want <= %d", len(result.Stdout), outputLimit)
	}
	if !result.Truncated {
		t.Fatal("Truncated = false, want true once an output exceeds the limit")
	}
}

func TestRunDecodedProgressAndQuestion(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	directory := t.TempDir()
	release := filepath.Join(directory, "release")
	startLine, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]string{{"type": "text", "text": "BATUTA-PROGRESS 1 START\n"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	questionLine, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]string{{"type": "text", "text": "BATUTA-QUESTION: which table?"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "start.jsonl"), append(startLine, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "question.jsonl"), append(questionLine, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "stream-helper")
	script := "#!/bin/sh\ncat start.jsonl\nwhile [ ! -e \"$BATUTA_PROGRESS_RELEASE\" ]; do :; done\ncat question.jsonl\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	subprocess := NewSubprocess()
	subprocess.Environment = []string{"BATUTA_PROGRESS_RELEASE=" + release}
	subprocess.Progress = func(event ProgressEvent) {
		if event.Criterion == 1 && event.State == "START" {
			select {
			case <-started:
			default:
				close(started)
			}
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Errorf("release helper: %v", err)
			}
		}
	}
	result, err := subprocess.Execute(context.Background(), Adapter{OutputDecoder: "cursor-stream-json"}, Invocation{Executable: executable, Dir: directory}, 5*time.Second)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.TimedOut || result.ExitCode != 0 {
		t.Fatalf("Execute() result = %#v", result)
	}
	select {
	case <-started:
	default:
		t.Fatal("progress was not reported while the process was still running")
	}
	if result.Question != "which table?" {
		t.Fatalf("Question = %q, want which table?", result.Question)
	}
	assertProgressEvents(t, result.Progress, []ProgressEvent{{Criterion: 1, State: "START"}})
}

func TestRunDecodedLimitRegex(t *testing.T) {
	t.Parallel()
	first := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"quota "}]}}` + "\n")
	second := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"exceeded\ntotal tokens used: 42"}]}}` + "\n")
	raw := append(append([]byte(nil), first...), second...)
	adapter := Adapter{
		OutputDecoder: "cursor-stream-json",
		LimitRegex:    "quota exceeded",
		UsageRegex:    "total tokens used: (?P<total>[0-9]+)",
	}
	result := executeDecoded(t, adapter, splitStreamRunner{chunks: [][]byte{first, second}, stdout: raw}, nil)
	if !result.RateLimited {
		t.Fatalf("RateLimited = false, decoded stdout = %q raw = %q", result.Stdout, result.RawStdout)
	}
	if result.Usage == nil || result.Usage.ReportedTotalTokens == nil || *result.Usage.ReportedTotalTokens != 42 {
		t.Fatalf("Usage = %#v, want reported total 42", result.Usage)
	}
	if result.Usage.Provenance != "cli/usage_regex" {
		t.Fatalf("provenance = %q, want cli/usage_regex", result.Usage.Provenance)
	}
}

func TestRunWithoutDecoderUnchanged(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"BATUTA-PROGRESS 1 START\nquota exceeded\nBATUTA-QUESTION: which table?"}]}}` + "\n")
	var log bytes.Buffer
	adapter := Adapter{
		LimitRegex: "quota exceeded",
		UsageRegex: "total tokens used: (?P<total>[0-9]+)",
	}
	result := executeDecoded(t, adapter, splitStreamRunner{chunks: [][]byte{raw}, stdout: raw}, &log)
	if !bytes.Equal(result.Stdout, raw) {
		t.Fatalf("Stdout changed without a decoder: %q", result.Stdout)
	}
	if len(result.RawStdout) != 0 {
		t.Fatalf("RawStdout = %q, want empty without a decoder", result.RawStdout)
	}
	if log.String() != string(raw) {
		t.Fatalf("run log = %q, want raw stdout", log.String())
	}
	if !result.RateLimited {
		t.Fatal("limit_regex still applies to the raw stdout when no decoder is set")
	}
	if result.Question != "" {
		t.Fatalf("Question = %q, want empty without a decoder", result.Question)
	}
	if len(result.Progress) != 0 {
		t.Fatalf("Progress = %#v, want none without a decoder", result.Progress)
	}
	if result.Usage != nil {
		t.Fatalf("Usage = %#v, want nil", result.Usage)
	}
}

// failOnceWriter fails the first decoded write, then records what arrives.
type failOnceWriter struct {
	failed  bool
	written bytes.Buffer
}

func (w *failOnceWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, errors.New("destination closed")
	}
	w.written.Write(p)
	return len(p), nil
}

func TestDecodeWriterDropsLineAfterWriteError(t *testing.T) {
	t.Parallel()
	dest := &failOnceWriter{}
	w := newDecodeWriter(LookupDecoder("cursor-stream-json"), dest)
	if _, err := w.Write([]byte("alpha\nbeta\n")); err == nil {
		t.Fatal("Write() error = nil, want the destination failure")
	}
	if err := w.flush(); err != nil {
		t.Fatalf("flush() error = %v", err)
	}
	if _, err := w.Write([]byte("gamma\n")); err != nil {
		t.Fatalf("Write() after flush error = %v", err)
	}
	if got := dest.written.String(); strings.Contains(got, "alpha") {
		t.Fatalf("destination received the failed line again: %q", got)
	}
	if got := dest.written.String(); !strings.Contains(got, "beta") || !strings.Contains(got, "gamma") {
		t.Fatalf("destination received %q after the failure, want beta and gamma", got)
	}
	if decoded := string(w.bytes()); strings.Count(decoded, "alpha") != 1 {
		t.Fatalf("decoded buffer = %q, want alpha retained exactly once", decoded)
	}
}

func TestDecodeWriterBoundsPendingLine(t *testing.T) {
	t.Parallel()
	dest := &bytes.Buffer{}
	w := newDecodeWriter(LookupDecoder("cursor-stream-json"), dest)
	chunk := bytes.Repeat([]byte{'x'}, outputLimit/2+1)
	for i := 0; i < 2; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if len(w.pending) > outputLimit {
		t.Fatalf("pending length = %d, want <= %d", len(w.pending), outputLimit)
	}
	if !w.truncated {
		t.Fatal("truncated = false, want true once pending drops its excess")
	}
	if err := w.flush(); err != nil {
		t.Fatalf("flush() error = %v", err)
	}
	if got := dest.String(); len(got) > outputLimit {
		t.Fatalf("destination received %d bytes, want <= %d", len(got), outputLimit)
	}
}

func executeDecoded(t *testing.T, adapter Adapter, runner splitStreamRunner, stdout *bytes.Buffer) Result {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "worker")
	subprocess := Subprocess{
		Lookup: func(string) (string, error) { return executable, nil },
		Runner: runner,
	}
	if stdout != nil {
		subprocess.Stdout = stdout
	}
	result, err := subprocess.Execute(context.Background(), adapter, Invocation{Executable: "worker", Dir: t.TempDir()}, 0)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	return result
}

type splitStreamRunner struct {
	chunks [][]byte
	stdout []byte
}

func (r splitStreamRunner) Run(_ context.Context, command publication.Command) (publication.CommandResult, error) {
	for _, chunk := range r.chunks {
		if command.Observer != nil {
			if _, err := command.Observer.Write(chunk); err != nil {
				return publication.CommandResult{ExitCode: -1}, err
			}
		}
	}
	return publication.CommandResult{Stdout: r.stdout}, nil
}
