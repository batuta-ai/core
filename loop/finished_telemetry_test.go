package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/journal"
)

// scriptFunc is a scripted executor backend: every invocation runs the same
// function, the way unavailableBackend and the ACP fixtures stand in for a
// real executor in the other loop tests.
type scriptFunc func(executor.Execution) (executor.Result, error)

func (f scriptFunc) Execute(_ context.Context, e executor.Execution) (executor.Result, error) {
	return f(e)
}

// telemetryPlan swaps the default plan for a single low task, so scripted
// results drive exactly one attempt lineage.
func telemetryPlan(t *testing.T, f fixture) {
	t.Helper()
	plan := "# Plan — Greetings\n\n**Goal:** Greeting.\n**Created:** 2026-09-06 · **Status:** approved\n\n## Tasks\n- [ ] 1. Add greeting one — backend/low\n      Scope: out/1.txt\n      Accept: greeting exists → test -f out/1.txt\n"
	if err := os.WriteFile(filepath.Join(f.root, ".batuta", "plans", "greetings.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", "-A")
	f.run(t, "commit", "-q", "-m", "test: one telemetry task")
}

func runTelemetry(t *testing.T, f fixture, backend executor.Backend) (*Runner, string) {
	t.Helper()
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatal(err)
	}
	r.backend = backend
	state, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v\n%s", err, &out)
	}
	return r, state
}

func recordDetails(t *testing.T, records []journal.Record, kind journal.Kind) []map[string]any {
	t.Helper()
	var details []map[string]any
	for _, record := range records {
		if record.Kind != kind {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	return details
}

func executionDetail(t *testing.T, records []journal.Record, kind journal.Kind, execution int) map[string]any {
	t.Helper()
	for _, detail := range recordDetails(t, records, kind) {
		if int(detail["execution"].(float64)) == execution {
			return detail
		}
	}
	t.Fatalf("no %s record for execution %d", kind, execution)
	return nil
}

func outputTail(t *testing.T, detail map[string]any) (stdout, stderr string, present bool) {
	t.Helper()
	raw, ok := detail["output_tail"].(map[string]any)
	if !ok {
		return "", "", false
	}
	text, ok := raw["stdout"].(string)
	if !ok {
		t.Fatal("output_tail.stdout missing")
	}
	errors, ok := raw["stderr"].(string)
	if !ok {
		t.Fatal("output_tail.stderr missing")
	}
	return text, errors, true
}

func TestFinishedRecordsTailOnUncleanSession(t *testing.T) {
	t.Parallel()
	f := setup(t)
	telemetryPlan(t, f)
	var stdout strings.Builder
	for line := 1; line <= 50; line++ {
		fmt.Fprintf(&stdout, "line-%04d\n", line)
	}
	// One 5000-byte line: the 4096-byte cut must land inside the multibyte
	// rune at offset 903 and keep the tail valid UTF-8 ending on the last
	// bytes of the stream.
	stderr := strings.Repeat("a", 903) + "€" + strings.Repeat("b", 4094)
	backend := scriptFunc(func(executor.Execution) (executor.Result, error) {
		return executor.Result{ExitCode: 1, Finished: true, Stdout: []byte(stdout.String()), Stderr: []byte(stderr)}, nil
	})
	r, state := runTelemetry(t, f, backend)
	if state != StateBlocked {
		t.Fatalf("Run() = %s, want blocked", state)
	}
	detail := executionDetail(t, readJournal(t, f, r.delivery), KindFinished, 1)
	if code, ok := detail["exit_code"].(float64); !ok || code != 1 {
		t.Fatalf("exit_code = %v, want 1", detail["exit_code"])
	}
	gotStdout, gotStderr, present := outputTail(t, detail)
	if !present {
		t.Fatal("unclean session lost its output_tail")
	}
	var wantStdout []string
	for line := 11; line <= 50; line++ {
		wantStdout = append(wantStdout, fmt.Sprintf("line-%04d", line))
	}
	if gotStdout != strings.Join(wantStdout, "\n") {
		t.Fatalf("output_tail.stdout = %q", gotStdout)
	}
	if !utf8.ValidString(gotStderr) || gotStderr != strings.Repeat("b", 4094) {
		t.Fatalf("output_tail.stderr cut %d bytes, not the last 4096 on a UTF-8 boundary: %q…", len(gotStderr), gotStderr[:0])
	}
	if len(gotStderr) > 4096 || !strings.HasSuffix(gotStderr, strings.Repeat("b", 100)) {
		t.Fatalf("output_tail.stderr does not keep the end of the stream: %d bytes", len(gotStderr))
	}
}

func TestLimitWaitRecordsTail(t *testing.T) {
	t.Parallel()
	f := setup(t)
	telemetryPlan(t, f)
	calls := 0
	backend := scriptFunc(func(e executor.Execution) (executor.Result, error) {
		calls++
		if calls == 1 {
			return executor.Result{ExitCode: 1, Finished: true, RateLimited: true,
				Stdout: []byte("crunching tokens\n"), Stderr: []byte("Rate limit reached\n")}, nil
		}
		if err := os.MkdirAll(filepath.Join(e.Request.Cwd, "out"), 0o755); err != nil {
			return executor.Result{}, err
		}
		if err := os.WriteFile(filepath.Join(e.Request.Cwd, "out", "1.txt"), []byte("ok\n"), 0o644); err != nil {
			return executor.Result{}, err
		}
		return executor.Result{ExitCode: 0, Finished: true, Stdout: []byte("done\n")}, nil
	})
	r, state := runTelemetry(t, f, backend)
	if state != StateDone {
		t.Fatalf("Run() = %s, want done", state)
	}
	records := readJournal(t, f, r.delivery)
	waits := recordDetails(t, records, KindLimitWait)
	if len(waits) != 1 {
		t.Fatalf("limit_wait records = %d, want 1", len(waits))
	}
	if waits[0]["execution"].(float64) != 1 || int(waits[0]["wait"].(float64)) != 1 {
		t.Fatalf("limit_wait detail = %v", waits[0])
	}
	stdout, stderr, present := outputTail(t, waits[0])
	if !present {
		t.Fatal("limit_wait lost the output_tail of the rate-limited invocation")
	}
	if stdout != "crunching tokens" || stderr != "Rate limit reached" {
		t.Fatalf("limit_wait output_tail = %q / %q", stdout, stderr)
	}
	if _, present := executionDetail(t, records, KindFinished, 1)["output_tail"]; present {
		t.Fatal("clean finish after the wait carries an output_tail")
	}
}

func TestFinishedOmitsTailOnCleanSession(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	finished := recordDetails(t, readJournal(t, f, r.delivery), KindFinished)
	if len(finished) != 3 {
		t.Fatalf("executor_finished records = %d, want 3", len(finished))
	}
	for _, detail := range finished {
		if code, ok := detail["exit_code"].(float64); !ok || code != 0 {
			t.Fatalf("exit_code = %v, want a clean session", detail["exit_code"])
		}
		if _, present := detail["output_tail"]; present {
			t.Fatalf("clean session carries an output_tail: %v", detail)
		}
	}
}

func TestFinishedRecordsUsage(t *testing.T) {
	t.Parallel()
	run := func(withUsage bool) map[string]any {
		f := setup(t)
		telemetryPlan(t, f)
		backend := scriptFunc(func(e executor.Execution) (executor.Result, error) {
			if err := os.MkdirAll(filepath.Join(e.Request.Cwd, "out"), 0o755); err != nil {
				return executor.Result{}, err
			}
			if err := os.WriteFile(filepath.Join(e.Request.Cwd, "out", "1.txt"), []byte("ok\n"), 0o644); err != nil {
				return executor.Result{}, err
			}
			result := executor.Result{ExitCode: 0, Finished: true, Stdout: []byte("done\n")}
			if withUsage {
				input, output := int64(1200), int64(345)
				result.Usage = &executor.Usage{InputTokens: &input, OutputTokens: &output, Provenance: "cli/usage_regex"}
			}
			return result, nil
		})
		r, state := runTelemetry(t, f, backend)
		if state != StateDone {
			t.Fatalf("Run() = %s, want done", state)
		}
		return executionDetail(t, readJournal(t, f, r.delivery), KindFinished, 1)
	}

	detail := run(true)
	usage, ok := detail["usage"].(map[string]any)
	if !ok {
		t.Fatalf("executor_finished lost the usage: %v", detail)
	}
	if usage["provenance"] != "cli/usage_regex" {
		t.Fatalf("usage provenance = %v", usage["provenance"])
	}
	if input, ok := usage["input_tokens"].(float64); !ok || input != 1200 {
		t.Fatalf("usage input_tokens = %v, want 1200", usage["input_tokens"])
	}
	if output, ok := usage["output_tokens"].(float64); !ok || output != 345 {
		t.Fatalf("usage output_tokens = %v, want 345", usage["output_tokens"])
	}
	if _, unknown := detail["usage_unknown"]; unknown {
		t.Fatal("usage_unknown set while usage was reported")
	}

	detail = run(false)
	if _, reported := detail["usage"]; reported {
		t.Fatalf("usage reported without a Usage: %v", detail["usage"])
	}
	if unknown, ok := detail["usage_unknown"].(bool); !ok || !unknown {
		t.Fatalf("usage_unknown = %v, want true", detail["usage_unknown"])
	}
}

func TestFinishedTailRedacted(t *testing.T) {
	t.Parallel()
	f := setup(t)
	telemetryPlan(t, f)
	backend := scriptFunc(func(executor.Execution) (executor.Result, error) {
		return executor.Result{ExitCode: 1, Finished: true,
			Stdout: []byte("wrote " + filepath.Join(f.root, "out", "1.txt") + "\nAPI_KEY=super-secret\nkept line\n"),
			Stderr: []byte("boom\n")}, nil
	})
	r, state := runTelemetry(t, f, backend)
	if state != StateBlocked {
		t.Fatalf("Run() = %s, want blocked", state)
	}
	detail := executionDetail(t, readJournal(t, f, r.delivery), KindFinished, 1)
	stdout, stderr, present := outputTail(t, detail)
	if !present {
		t.Fatal("unclean session lost its output_tail")
	}
	if stdout != "wrote out/1.txt\nkept line" {
		t.Fatalf("output_tail.stdout = %q, want the workspace path redacted and the secret line dropped", stdout)
	}
	if stderr != "boom" {
		t.Fatalf("output_tail.stderr = %q", stderr)
	}
	if strings.Contains(stdout+stderr, f.root) || strings.Contains(stdout+stderr, "super-secret") {
		t.Fatalf("output_tail leaked workspace or secret: %q / %q", stdout, stderr)
	}
}
