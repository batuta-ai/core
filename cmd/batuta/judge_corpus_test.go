package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

// corpusGitCommits initializes the fixture repository and returns the base
// and candidate commits the journals reference: greet.go gains GreetHandler
// and greet_test.go adds one test, so the candidate diff carries an added
// identifier line and one added test function.
func corpusGitCommits(t *testing.T, root string) (base, commit string) {
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
	if err := os.WriteFile(filepath.Join(root, "greet.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, root, "add", "greet.go")
	runGit(t, git, root, "commit", "-qm", "base")
	base = strings.TrimSpace(runGit(t, git, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "greet.go"),
		[]byte("package main\n\nfunc GreetHandler() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "greet_test.go"),
		[]byte("package main\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, root, "add", "greet.go", "greet_test.go")
	runGit(t, git, root, "commit", "-qm", "greeting")
	commit = strings.TrimSpace(runGit(t, git, root, "rev-parse", "HEAD"))
	return base, commit
}

// corpusJournalFixture writes a delivery journal with one opened record and
// one recorded attempt per entry in attempts, plus each attempt's run log
// unless runsMissing names its execution. It returns the journal path.
func corpusJournalFixture(t *testing.T, root, delivery, slug, taskID, title string, attempts []map[string]any, runsMissing ...int) string {
	t.Helper()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := json.Marshal(map[string]any{
		"slug":  slug,
		"tasks": []map[string]any{{"task_id": taskID, "title": title}},
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
		if _, err := store.Append(delivery, journal.Record{Kind: loop.KindFinished, TaskID: taskID, Detail: finished}); err != nil {
			t.Fatal(err)
		}
		report := gates.Report{
			TaskID: taskID, Execution: execution,
			Finished: gates.Verdict{Name: "finished", Pass: true, Signal: "exit 0"},
			Tree:     gates.Verdict{Name: "tree", Pass: attempt["tree_changed"] == true, Signal: "the session wrote to the tree"},
			Tests:    gates.Verdict{Name: "tests", Pass: true, Signal: "`go test ./...` passed"},
			Scope:    gates.Verdict{Name: "scope", Pass: true, Signal: "within Scope"},
			Proofs: []gates.Verdict{
				{Name: "proof 1", Pass: true, Signal: "a greeting exists — `test -f greet.go` passed"},
			},
			Verifier: &gates.Verdict{Name: "verifier", Pass: true, Signal: "1/1 DONE"},
			Passed:   true,
		}
		detail, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(delivery, journal.Record{Kind: loop.KindGates, TaskID: taskID, Detail: detail}); err != nil {
			t.Fatal(err)
		}
		outcome, err := json.Marshal(attempt["outcome"])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(delivery, journal.Record{Kind: attempt["kind"].(journal.Kind), TaskID: taskID, Detail: outcome}); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(runsMissing, execution) {
			log := fmt.Sprintf("# exit 0 · finished true · timed out false · rate limited false · %d\n\n## stdout\n\n%s\n\n## stderr\n\n",
				index+1, attempt["log"])
			if err := os.MkdirAll(filepath.Join(root, ".batuta", "runs"), 0o755); err != nil {
				t.Fatal(err)
			}
			name := loop.ReplayRunLogName(delivery, slug, taskID, time.Time{}, execution)
			if err := os.WriteFile(filepath.Join(root, ".batuta", "runs", name), []byte(log), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return filepath.Join(root, journal.Dir, delivery+".jsonl")
}

func corpusCandidateAttempt(execution int, base, commit, log string) map[string]any {
	return map[string]any{
		"execution": execution, "tree_changed": true, "kind": loop.KindCandidate, "base_sha": base,
		"outcome": map[string]any{"execution": execution, "commit": commit, "evidence": map[string]any{"base_sha": base}},
		"log":     log,
	}
}

func readCorpusCases(t *testing.T, out string) ([]corpusCase, []string) {
	t.Helper()
	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSuffix(string(content), "\n")
	var cases []corpusCase
	var lines []string
	if text == "" {
		return cases, lines
	}
	for index, line := range strings.Split(text, "\n") {
		var c corpusCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("case line %d is not JSON: %v\n%s", index+1, err, line)
		}
		cases = append(cases, c)
		lines = append(lines, line)
	}
	return cases, lines
}

func corpusSHA256Of(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

const corpusTestReport = "Wrote `greet.go`.\nBATUTA-PROGRESS 1 DONE"

func TestCorpusBuildCases(t *testing.T) {
	root := t.TempDir()
	base, commit := corpusGitCommits(t, root)
	delivery := "alpha-20260901-010101"
	journalPath := corpusJournalFixture(t, root, delivery, "alpha", "task_1", "Greet once", []map[string]any{
		corpusCandidateAttempt(1, base, commit, corpusTestReport),
		{"execution": 2, "tree_changed": true, "kind": loop.KindFailure, "base_sha": base,
			"outcome": map[string]any{"execution": 2, "blocker": "tests_failed", "blocked": true},
			"log":     "nothing"},
		corpusCandidateAttempt(3, base, commit, "wrote nothing"),
		{"execution": 4, "tree_changed": true, "kind": loop.KindCandidate,
			"outcome": map[string]any{"execution": 4, "commit": "sha"},
			"log":     "wrote nothing"},
	}, 3)
	out := filepath.Join(root, "cases", "corpus.jsonl")
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "corpus", "build", "--journal", journalPath, "--out", out,
		"--runs", filepath.Join(root, ".batuta", "runs"), "--workspace", root}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge corpus build = %v\nstderr: %s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
	cases, lines := readCorpusCases(t, out)
	if len(cases) != 3 {
		t.Fatalf("got %d cases, want the 3 of attempt e1:\n%s", len(cases), strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		var object map[string]any
		if err := json.Unmarshal([]byte(line), &object); err != nil {
			t.Fatalf("case line is not JSON: %v\n%s", err, line)
		}
		for _, field := range []string{"id", "delivery", "task", "execution", "label", "report", "diff",
			"changed_paths", "proofs", "verifier", "report_sha256", "diff_sha256"} {
			if _, ok := object[field]; !ok {
				t.Fatalf("case line is missing %q:\n%s", field, line)
			}
		}
	}
	wantIDs := []string{
		delivery + "/task_1/e1/clean",
		delivery + "/task_1/e1/fabricated_reference",
		delivery + "/task_1/e1/wrong_count",
	}
	for index, c := range cases {
		if c.ID != wantIDs[index] {
			t.Fatalf("case %d id = %q, want %q", index, c.ID, wantIDs[index])
		}
	}
	clean := cases[0]
	if clean.Delivery != delivery || clean.Task != "task_1" || clean.Execution != 1 || clean.Label != "clean" {
		t.Fatalf("clean case = %#v", clean)
	}
	if clean.Report != corpusTestReport {
		t.Fatalf("clean report = %q, want the run-log tail", clean.Report)
	}
	if !strings.Contains(clean.Diff, "+func GreetHandler") || !strings.Contains(clean.Diff, "+func TestGreet") {
		t.Fatalf("clean diff = %q, want the candidate diff", clean.Diff)
	}
	if !slices.Equal(clean.ChangedPaths, []string{"greet.go", "greet_test.go"}) {
		t.Fatalf("clean changed_paths = %#v", clean.ChangedPaths)
	}
	if len(clean.Proofs) != 1 || clean.Proofs[0].Name != "proof 1" || !clean.Proofs[0].Pass {
		t.Fatalf("clean proofs = %#v", clean.Proofs)
	}
	if clean.Verifier == nil || !clean.Verifier.Pass {
		t.Fatalf("clean verifier = %#v", clean.Verifier)
	}
	if clean.ReportSHA256 != corpusSHA256Of(clean.Report) {
		t.Fatalf("clean report_sha256 = %q", clean.ReportSHA256)
	}
	if clean.DiffSHA256 != corpusSHA256Of(clean.Diff) {
		t.Fatalf("clean diff_sha256 = %q", clean.DiffSHA256)
	}
	for _, want := range []struct {
		execution int
		reason    string
	}{
		{2, "outcome is tests_failed"},
		{3, "run log"},
		{4, "diff unresolved"},
	} {
		if !strings.Contains(stderr.String(), fmt.Sprintf("e%d", want.execution)) ||
			!strings.Contains(stderr.String(), want.reason) {
			t.Fatalf("stderr = %q, want the e%d skip reason %q", stderr.String(), want.execution, want.reason)
		}
	}
}

func TestCorpusBuildVariants(t *testing.T) {
	root := t.TempDir()
	base, commit := corpusGitCommits(t, root)
	alpha := "alpha-20260901-010101"
	beta := "beta-20260902-020202"
	journalAlpha := corpusJournalFixture(t, root, alpha, "alpha", "task_1", "Greet once",
		[]map[string]any{corpusCandidateAttempt(1, base, commit, corpusTestReport)})
	journalBeta := corpusJournalFixture(t, root, beta, "beta", "task_1", "Greet twice",
		[]map[string]any{corpusCandidateAttempt(1, base, commit, corpusTestReport)})
	out := filepath.Join(root, "corpus.jsonl")
	build := func() string {
		var stdout, stderr strings.Builder
		err := run([]string{"judge", "corpus", "build", "--journal", journalAlpha, "--journal", journalBeta,
			"--out", out, "--workspace", root}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("judge corpus build = %v\nstderr: %s", err, stderr.String())
		}
		content, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	first := build()
	second := build()
	if first != second {
		t.Fatalf("two builds differ:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	cases, _ := readCorpusCases(t, out)
	byID := map[string]corpusCase{}
	var ids []string
	for _, c := range cases {
		byID[c.ID] = c
		ids = append(ids, c.ID)
	}
	wantIDs := []string{
		alpha + "/task_1/e1/behaviour_absent",
		alpha + "/task_1/e1/clean",
		alpha + "/task_1/e1/fabricated_reference",
		alpha + "/task_1/e1/wrong_count",
		beta + "/task_1/e1/behaviour_absent",
		beta + "/task_1/e1/clean",
		beta + "/task_1/e1/fabricated_reference",
		beta + "/task_1/e1/wrong_count",
	}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("case ids = %v, want %v", ids, wantIDs)
	}
	wantLines := map[string][]string{
		alpha: {"Updated `greet.go` so that greet twice.", "Added `GreetHandlerChecked` to `greet.go`.", "Added 4 new tests in `greet_test.go`."},
		beta:  {"Updated `greet.go` so that greet once.", "Added `GreetHandlerChecked` to `greet.go`.", "Added 4 new tests in `greet_test.go`."},
	}
	for _, delivery := range []string{alpha, beta} {
		clean := byID[delivery+"/task_1/e1/clean"]
		if clean.Report != corpusTestReport {
			t.Fatalf("clean report = %q", clean.Report)
		}
		for _, label := range []string{"behaviour_absent", "fabricated_reference", "wrong_count"} {
			line := wantLines[delivery][slices.Index([]string{"behaviour_absent", "fabricated_reference", "wrong_count"}, label)]
			variant := byID[delivery+"/task_1/e1/"+label]
			if variant.Report != clean.Report+"\n"+line {
				t.Fatalf("%s report = %q, want the clean report plus %q", label, variant.Report, line)
			}
			if variant.Diff != clean.Diff || variant.DiffSHA256 != clean.DiffSHA256 {
				t.Fatalf("%s diff differs from the clean case", label)
			}
			if !slices.Equal(variant.ChangedPaths, clean.ChangedPaths) {
				t.Fatalf("%s changed_paths = %#v", label, variant.ChangedPaths)
			}
			if variant.ReportSHA256 != corpusSHA256Of(variant.Report) {
				t.Fatalf("%s report_sha256 = %q", label, variant.ReportSHA256)
			}
		}
	}
}

func TestCorpusBuildSkips(t *testing.T) {
	root := t.TempDir()
	base, commit := corpusGitCommits(t, root)
	delivery := "alpha-20260901-010101"
	journalPath := corpusJournalFixture(t, root, delivery, "alpha", "task_1", "Greet once", []map[string]any{
		{"execution": 2, "tree_changed": true, "kind": loop.KindFailure, "base_sha": base,
			"outcome": map[string]any{"execution": 2, "blocker": "tests_failed", "blocked": true},
			"log":     "nothing"},
		corpusCandidateAttempt(3, base, commit, "wrote nothing"),
		{"execution": 4, "tree_changed": true, "kind": loop.KindCandidate,
			"outcome": map[string]any{"execution": 4, "commit": "sha"},
			"log":     "wrote nothing"},
	}, 3)
	out := filepath.Join(root, "corpus.jsonl")
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "corpus", "build", "--journal", journalPath, "--out", out, "--workspace", root}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge corpus build = %v\nstderr: %s", err, stderr.String())
	}
	var skips []string
	for _, line := range strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			skips = append(skips, line)
		}
	}
	if len(skips) != 3 {
		t.Fatalf("stderr = %q, want one skip line per attempt", stderr.String())
	}
	for index, line := range skips {
		if !strings.HasPrefix(line, "skipped ") {
			t.Fatalf("skip line %d = %q, want a leading \"skipped \"", index+1, line)
		}
	}
	if !strings.Contains(skips[0], "e2") || !strings.Contains(skips[0], "outcome is tests_failed") {
		t.Fatalf("first skip = %q, want the non-candidate outcome", skips[0])
	}
	if !strings.Contains(skips[1], "e3") || !strings.Contains(skips[1], "run log") {
		t.Fatalf("second skip = %q, want the missing run log", skips[1])
	}
	if !strings.Contains(skips[2], "e4") || !strings.Contains(skips[2], "diff unresolved") {
		t.Fatalf("third skip = %q, want the unresolved diff", skips[2])
	}
	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(content))) != 0 {
		t.Fatalf("out = %q, want an empty corpus", content)
	}
}

func TestCorpusBuildRejectsInvalidArguments(t *testing.T) {
	root := t.TempDir()
	journalPath := filepath.Join(root, "alpha.jsonl")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing journal flag", []string{"judge", "corpus", "build", "--out", filepath.Join(root, "c.jsonl"), "--workspace", root}},
		{"missing out flag", []string{"judge", "corpus", "build", "--journal", journalPath, "--workspace", root}},
		{"positional argument", []string{"judge", "corpus", "build", "--journal", journalPath, "--out", filepath.Join(root, "c.jsonl"), "--workspace", root, "extra"}},
		{"missing journal file", []string{"judge", "corpus", "build", "--journal", journalPath, "--out", filepath.Join(root, "c.jsonl"), "--workspace", root}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if err := run(tc.args, &stdout, &stderr); err == nil {
				t.Fatalf("judge corpus build = nil, want an error")
			}
		})
	}
}
