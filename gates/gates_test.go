package gates

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
)

func TestScopeMatchesPathsPrefixesAndGlobs(t *testing.T) {
	scope := []string{"src/checkout/payment.ts", "tests/checkout/", "docs/**/*.md", "lib/*.go"}
	if err := ValidScope(scope); err != nil {
		t.Fatalf("ValidScope() error = %v", err)
	}
	for _, bad := range [][]string{{"/etc/passwd"}, {"../outside"}, {"src/../../x"}, {""}} {
		if err := ValidScope(bad); err == nil {
			t.Fatalf("ValidScope(%v) should fail", bad)
		}
	}
	verdict := Scope([]string{"src/checkout/payment.ts", "tests/checkout/a.test.ts", "docs/guide/retry.md", "lib/x.go", "WORK.md", ".batuta/runs/x.md"}, scope)
	if !verdict.Pass || !strings.Contains(verdict.Signal, "managed state also touched") {
		t.Fatalf("in-scope verdict = %#v", verdict)
	}
	verdict = Scope([]string{"src/checkout/payment.ts", "src/other.ts", "lib/nested/x.go"}, scope)
	if verdict.Pass || verdict.Detail != "lib/nested/x.go\nsrc/other.ts" && verdict.Detail != "src/other.ts\nlib/nested/x.go" {
		t.Fatalf("out-of-scope verdict = %#v", verdict)
	}
	if verdict := Scope([]string{"anything"}, nil); !verdict.Pass || !strings.Contains(verdict.Signal, "no Scope") {
		t.Fatalf("no-scope verdict = %#v", verdict)
	}
}

func TestParseCriteriaSplitsTextFromProof(t *testing.T) {
	criteria := ParseCriteria([]string{
		"a failing test reproduces the timeout → npm test -- timeout",
		"README mentions the retry -> `grep -n retry README.md`",
		"reviewed by a human",
		"",
	})
	if len(criteria) != 3 {
		t.Fatalf("criteria = %#v", criteria)
	}
	if criteria[0].Text != "a failing test reproduces the timeout" || criteria[0].Proof != "npm test -- timeout" {
		t.Fatalf("criterion 1 = %#v", criteria[0])
	}
	if criteria[1].Proof != "grep -n retry README.md" || criteria[2].Proof != "" || criteria[2].Text != "reviewed by a human" {
		t.Fatalf("criteria = %#v", criteria)
	}
}

func TestVerifierParsesOneLinePerCriterion(t *testing.T) {
	if v := Verifier("chatter\nTASK 1: DONE\nTASK 2: DONE\n", 2); !v.Pass || v.Signal != "2/2 DONE" {
		t.Fatalf("all done = %#v", v)
	}
	if v := Verifier("TASK 1: DONE\nTASK 2: INCOMPLETE — no test for 4xx\n", 2); v.Pass || !strings.Contains(v.Detail, "no test for 4xx") {
		t.Fatalf("incomplete = %#v", v)
	}
	if v := Verifier("TASK 1: DONE\n", 2); v.Pass || !strings.Contains(v.Signal, "1 of 2") {
		t.Fatalf("short = %#v", v)
	}
	if v := Verifier("TASK 1: DONE\nTASK 3: DONE\n", 2); v.Pass || !strings.Contains(v.Signal, "skipped criterion 2") {
		t.Fatalf("wrong numbers = %#v", v)
	}
	if v := Verifier("I looked and it seems fine.", 1); v.Pass || !strings.Contains(v.Signal, "no TASK") {
		t.Fatalf("no lines = %#v", v)
	}
	if !NeedsVerifier("high", false, 1) || !NeedsVerifier("low", true, 1) || !NeedsVerifier("low", false, 2) || NeedsVerifier("medium", false, 1) {
		t.Fatal("NeedsVerifier rules")
	}
	prompt := VerifierPrompt("Retry the payment", ParseCriteria([]string{"x → npm test"}), "abc")
	if !strings.Contains(prompt, "Criterion 1: x (proof: `npm test`)") || !strings.Contains(prompt, "TASK <n>: DONE") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestShellGatesRunTheUsersCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell")
	}
	shell, err := NewShellRunner(5 * time.Second)
	if err != nil {
		t.Fatalf("NewShellRunner() error = %v", err)
	}
	dir := t.TempDir()
	ctx := context.Background()
	if v := Tests(ctx, shell, dir, "echo ok && true"); !v.Pass || v.Detail != "ok" {
		t.Fatalf("passing tests = %#v", v)
	}
	if v := Tests(ctx, shell, dir, "echo boom >&2; exit 3"); v.Pass || !strings.Contains(v.Signal, "exited 3") || !strings.Contains(v.Detail, "boom") {
		t.Fatalf("failing tests = %#v", v)
	}
	slow := ShellRunner{Runner: publication.ExecRunner{}, Shell: shell.Shell, Timeout: 200 * time.Millisecond}
	started := time.Now()
	if v := Tests(ctx, slow, dir, "sleep 3"); v.Pass || !strings.Contains(v.Detail, "timed out") {
		t.Fatalf("timed out tests = %#v", v)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("timeout did not kill the process group: took %s", time.Since(started))
	}
	proofs := Proofs(ctx, shell, dir, ParseCriteria([]string{"true holds → true", "false holds → false", "human judged"}))
	if len(proofs) != 3 || !proofs[0].Pass || proofs[1].Pass || !proofs[2].Pass || !strings.Contains(proofs[2].Signal, "left to the verifier") {
		t.Fatalf("proofs = %#v", proofs)
	}
}

func TestReportDecidesSummarizesAndCanonicalizes(t *testing.T) {
	before := publication.WorktreeState{HeadSHA: "a", PorcelainSHA256: "b", ContentSHA256: "c"}
	report := Report{
		TaskID: "task_2", Execution: 1,
		Finished: Finished(true, false, false, 0, "done"),
		Tree:     Tree(before, publication.WorktreeState{HeadSHA: "a", PorcelainSHA256: "b", ContentSHA256: "d"}),
		Tests:    Verdict{Name: "tests", Pass: true, Signal: "`npm test` passed"},
		Scope:    Verdict{Name: "scope", Pass: true, Signal: "within Scope"},
		Proofs:   []Verdict{{Name: "proof 1", Pass: false, Signal: "x — `npm test` exited 1", Detail: "FAIL x"}},
	}
	report.Decide()
	if report.Passed {
		t.Fatal("a failed proof must fail the report")
	}
	failures := report.Failures()
	if len(failures) != 1 || !strings.HasPrefix(failures[0], "gate proof 1: x") || !strings.Contains(failures[0], "    FAIL x") {
		t.Fatalf("failures = %q", failures)
	}
	if got := report.Summary(); got != "0 pass · 1 pass · 2 pass · 3 scope pass, proofs 0/1, verifier n/a" {
		t.Fatalf("summary = %q", got)
	}
	silent := Tree(before, before)
	if !silent.Pass || !strings.Contains(silent.Signal, "silent") {
		t.Fatalf("silent tree = %#v", silent)
	}
	if v := Finished(false, false, true, 2, ""); v.Pass || !strings.Contains(v.Signal, "rate limit") {
		t.Fatalf("rate limited = %#v", v)
	}
	if v := Finished(false, true, false, -1, ""); v.Pass || !strings.Contains(v.Signal, "time budget") {
		t.Fatalf("timed out = %#v", v)
	}
	canonical, digest, err := report.Canonical()
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("Canonical() = %s, %v", digest, err)
	}
	var object map[string]any
	if err := json.Unmarshal(canonical, &object); err != nil || object["task_id"] != "task_2" {
		t.Fatalf("canonical = %s", canonical)
	}
	again, _ := json.Marshal(object)
	if string(again) != string(canonical) {
		t.Fatalf("canonical form is not stable:\n%s\n%s", canonical, again)
	}
}
