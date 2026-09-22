package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// corpusGitInit initializes the fixture repository.
func corpusGitInit(t *testing.T, root string) {
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
}

// corpusGitCommits initializes the fixture repository and returns the base
// and candidate commits the journals reference: greet.go gains GreetHandler
// and greet_test.go adds one test, so the candidate diff carries an added
// identifier line and one added test function.
func corpusGitCommits(t *testing.T, root string) (base, commit string) {
	t.Helper()
	corpusGitInit(t, root)
	git := mustGit(t)
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

// corpusGitTwoCandidates initializes the fixture repository and returns the
// base commit plus two different candidate commits: one adds GreetHandler to
// greet.go, the other adds FarewellHandler to farewell.go, so the attempts
// of two deliveries carry different diffs and changed paths.
func corpusGitTwoCandidates(t *testing.T, root string) (base, greet, farewell string) {
	t.Helper()
	corpusGitInit(t, root)
	git := mustGit(t)
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
	runGit(t, git, root, "add", "greet.go")
	runGit(t, git, root, "commit", "-qm", "greeting")
	greet = strings.TrimSpace(runGit(t, git, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "farewell.go"),
		[]byte("package main\n\nfunc FarewellHandler() string { return \"bye\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, root, "add", "farewell.go")
	runGit(t, git, root, "commit", "-qm", "farewell")
	farewell = strings.TrimSpace(runGit(t, git, root, "rev-parse", "HEAD"))
	return base, greet, farewell
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
	if len(cases) != 4 {
		t.Fatalf("got %d cases, want the 4 of attempt e1:\n%s", len(cases), strings.Join(lines, "\n"))
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
		delivery + "/task_1/e1/true_behaviour",
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
		alpha + "/task_1/e1/true_behaviour",
		alpha + "/task_1/e1/wrong_count",
		alpha + "/task_1/e1/wrong_diff",
		beta + "/task_1/e1/behaviour_absent",
		beta + "/task_1/e1/clean",
		beta + "/task_1/e1/fabricated_reference",
		beta + "/task_1/e1/true_behaviour",
		beta + "/task_1/e1/wrong_count",
		beta + "/task_1/e1/wrong_diff",
	}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("case ids = %v, want %v", ids, wantIDs)
	}
	wantLines := map[string][]string{
		alpha: {
			"Updated `greet.go` so that greet twice.",
			"Added `GreetHandlerChecked` to `greet.go`.",
			"Updated `greet.go` so that greet once.",
			"Added 4 new tests in `greet_test.go`.",
			"Updated `greet.go` so that greet once.",
		},
		beta: {
			"Updated `greet.go` so that greet once.",
			"Added `GreetHandlerChecked` to `greet.go`.",
			"Updated `greet.go` so that greet twice.",
			"Added 4 new tests in `greet_test.go`.",
			"Updated `greet.go` so that greet twice.",
		},
	}
	variantLabels := []string{"behaviour_absent", "fabricated_reference", "true_behaviour", "wrong_count", "wrong_diff"}
	for _, delivery := range []string{alpha, beta} {
		clean := byID[delivery+"/task_1/e1/clean"]
		if clean.Report != corpusTestReport {
			t.Fatalf("clean report = %q", clean.Report)
		}
		for _, label := range variantLabels {
			line := wantLines[delivery][slices.Index(variantLabels, label)]
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

func TestCorpusBuildBehaviourVariants(t *testing.T) {
	root := t.TempDir()
	base, greet, farewell := corpusGitTwoCandidates(t, root)
	alpha := "alpha-20260901-010101"
	beta := "beta-20260902-020202"
	journalAlpha := corpusJournalFixture(t, root, alpha, "alpha", "task_1", "Greet once",
		[]map[string]any{corpusCandidateAttempt(1, base, greet, corpusTestReport)})
	journalBeta := corpusJournalFixture(t, root, beta, "beta", "task_1", "Greet twice",
		[]map[string]any{corpusCandidateAttempt(1, base, farewell, corpusTestReport)})
	out := filepath.Join(root, "corpus.jsonl")
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "corpus", "build", "--journal", journalAlpha, "--journal", journalBeta,
		"--out", out, "--workspace", root}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge corpus build = %v\nstderr: %s", err, stderr.String())
	}
	cases, _ := readCorpusCases(t, out)
	byID := map[string]corpusCase{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	alphaClean := byID[alpha+"/task_1/e1/clean"]
	betaClean := byID[beta+"/task_1/e1/clean"]
	if alphaClean.Diff == betaClean.Diff || slices.Equal(alphaClean.ChangedPaths, betaClean.ChangedPaths) {
		t.Fatalf("the fixture's attempts share one diff: alpha %#v, beta %#v", alphaClean.ChangedPaths, betaClean.ChangedPaths)
	}
	for _, want := range []struct {
		id       string
		line     string
		reportOf string
		diffOf   string
	}{
		{alpha + "/task_1/e1/true_behaviour", "Updated `greet.go` so that greet once.", alphaClean.ID, alphaClean.ID},
		{alpha + "/task_1/e1/wrong_diff", "Updated `farewell.go` so that greet once.", alphaClean.ID, betaClean.ID},
		{beta + "/task_1/e1/true_behaviour", "Updated `farewell.go` so that greet twice.", betaClean.ID, betaClean.ID},
		{beta + "/task_1/e1/wrong_diff", "Updated `greet.go` so that greet twice.", betaClean.ID, alphaClean.ID},
	} {
		variant, ok := byID[want.id]
		if !ok {
			t.Fatalf("case %s missing, got ids %v", want.id, byID)
		}
		reportOf, diffOf := byID[want.reportOf], byID[want.diffOf]
		if variant.Report != reportOf.Report+"\n"+want.line {
			t.Fatalf("%s report = %q, want the report of %s plus %q", want.id, variant.Report, want.reportOf, want.line)
		}
		if variant.Diff != diffOf.Diff || variant.DiffSHA256 != diffOf.DiffSHA256 {
			t.Fatalf("%s diff = %q, want the diff of %s", want.id, variant.Diff, want.diffOf)
		}
		if !slices.Equal(variant.ChangedPaths, diffOf.ChangedPaths) {
			t.Fatalf("%s changed_paths = %#v, want the changed paths of %s (%#v)",
				want.id, variant.ChangedPaths, want.diffOf, diffOf.ChangedPaths)
		}
	}
}

func TestCorpusBuildSplit(t *testing.T) {
	root := t.TempDir()
	base, commit := corpusGitCommits(t, root)
	alpha := "alpha-20260901-010101"
	beta := "beta-20260902-020202"
	journalAlpha := corpusJournalFixture(t, root, alpha, "alpha", "task_1", "Greet once", []map[string]any{
		corpusCandidateAttempt(1, base, commit, corpusTestReport),
		corpusCandidateAttempt(3, base, commit, corpusTestReport),
		corpusCandidateAttempt(5, base, commit, corpusTestReport),
	})
	journalBeta := corpusJournalFixture(t, root, beta, "beta", "task_1", "Greet twice",
		[]map[string]any{corpusCandidateAttempt(1, base, commit, corpusTestReport)})
	out := filepath.Join(root, "corpus.jsonl")
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "corpus", "build", "--journal", journalAlpha, "--journal", journalBeta,
		"--out", out, "--workspace", root}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge corpus build = %v\nstderr: %s", err, stderr.String())
	}
	cases, _ := readCorpusCases(t, out)
	splitByAttempt := map[string]string{}
	for _, c := range cases {
		attemptID := c.ID[:strings.LastIndex(c.ID, "/")]
		sum := sha256.Sum256([]byte(attemptID))
		wantSplit := "test"
		if sum[0]%2 == 0 {
			wantSplit = "calibrate"
		}
		if c.Split != wantSplit {
			t.Fatalf("%s split = %q, want %q from the first sha256 byte %d", c.ID, c.Split, wantSplit, sum[0])
		}
		if got, seen := splitByAttempt[attemptID]; seen && got != c.Split {
			t.Fatalf("%s split = %q, want the split %q the attempt's other cases carry", c.ID, c.Split, got)
		}
		splitByAttempt[attemptID] = c.Split
	}
	if len(splitByAttempt) != 4 {
		t.Fatalf("splits cover %d attempts, want the 4 fixture attempts: %v", len(splitByAttempt), splitByAttempt)
	}
	var calibrate, test int
	for _, split := range splitByAttempt {
		switch split {
		case "calibrate":
			calibrate++
		case "test":
			test++
		}
	}
	if calibrate == 0 || test == 0 {
		t.Fatalf("splits = %v, want both a calibrate and a test attempt", splitByAttempt)
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

// corpusRunDiff is the candidate diff the run fixtures carry: one added
// identifier line and one added test function, so an added-test count
// claimed above one is wrong and GreetHandlerChecked is absent from it.
const corpusRunDiff = `diff --git a/greet.go b/greet.go
index 111..222 100644
--- a/greet.go
+++ b/greet.go
@@ -1,2 +1,4 @@
 package main
+
+func GreetHandler() string { return "hi" }
diff --git a/greet_test.go b/greet_test.go
index 333..444 100644
--- a/greet_test.go
+++ b/greet_test.go
@@ -1,2 +1,4 @@
 package main
+
+func TestGreet(t *testing.T) {}
`

var corpusRunProofs = []gates.Verdict{
	{Name: "proof 1", Pass: true, Signal: "a greeting exists — `test -f greet.go` passed"},
}

// corpusRunAnswers routes one task's fake judge: the HTTP status, the choice
// every relation question gets, its confidence, the material noul and the
// input tokens the usage reports; a negative inputTokens omits the usage.
type corpusRunAnswers struct {
	status      int
	choice      string
	confidence  float64
	material    float64
	inputTokens int
}

type corpusRunRecord struct {
	calls  map[string]int
	bodies map[string]map[string]any
}

// corpusRunServer answers every relation question of a routed task with the
// route's choice and every material question with its noul; an unrouted task
// is supported. It records the calls and the last request body per task.
func corpusRunServer(t *testing.T, routes map[string]corpusRunAnswers) (*httptest.Server, *corpusRunRecord) {
	t.Helper()
	record := &corpusRunRecord{calls: map[string]int{}, bodies: map[string]map[string]any{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		taskID := ""
		if state, ok := body["state"].(map[string]any); ok {
			if task, ok := state["task"].(map[string]any); ok {
				taskID, _ = task["id"].(string)
			}
		}
		record.calls[taskID]++
		record.bodies[taskID] = body
		route, ok := routes[taskID]
		if !ok {
			route = corpusRunAnswers{status: http.StatusOK, choice: "supported", confidence: 0.95, material: 0.95, inputTokens: 12}
		}
		if route.status == 0 {
			route.status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		if route.status != http.StatusOK {
			w.WriteHeader(route.status)
			if _, err := w.Write([]byte(`{"error":"no"}`)); err != nil {
				t.Errorf("write response: %v", err)
			}
			return
		}
		answers := map[string]any{}
		if questions, ok := body["questions"].(map[string]any); ok {
			for key, question := range questions {
				kind := ""
				if typed, ok := question.(map[string]any); ok {
					kind, _ = typed["type"].(string)
				}
				switch {
				case strings.HasSuffix(key, "_relation") && kind == "choice":
					if route.choice == "supported" {
						answers[key] = map[string]any{"type": "choice", "choice": "supported", "confidence": route.confidence,
							"probabilities": map[string]any{"supported": route.confidence, "unverifiable": 1 - route.confidence}}
					} else {
						answers[key] = map[string]any{"type": "choice", "choice": route.choice, "confidence": route.confidence,
							"probabilities": map[string]any{route.choice: 0.93, "supported": 0.02, "unverifiable": 0.05}}
					}
				case strings.HasSuffix(key, "_material") && kind == "noul":
					answers[key] = map[string]any{"type": "noul", "noul": route.material}
				}
			}
		}
		response := map[string]any{"model": "jev-1.13.0", "answers": answers}
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

// corpusRunFixture writes one JSON line per case and returns the corpus path.
func corpusRunFixture(t *testing.T, root string, cases []corpusCase) string {
	t.Helper()
	path := filepath.Join(root, "corpus.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, c := range cases {
		if err := encoder.Encode(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func corpusRunVerdict(done bool) *gates.Verdict {
	verdict := &gates.Verdict{Name: "verifier", Pass: true, Signal: "1/1 DONE"}
	if done {
		verdict.Detail = "TASK 1: DONE"
	}
	return verdict
}

func TestCorpusRunCases(t *testing.T) {
	server, record := corpusRunServer(t, map[string]corpusRunAnswers{
		"task_b": {choice: "behaviour_absent", confidence: 0.93, material: 0.93, inputTokens: 12},
		"task_d": {status: http.StatusInternalServerError},
	})
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	corpus := corpusRunFixture(t, root, []corpusCase{
		{ID: "d1/task_a/e1/clean", Delivery: "d1", Task: "task_a", Execution: 1, Label: "clean",
			Report: "Wrote `greet.go`.\nBATUTA-PROGRESS 1 DONE", Diff: corpusRunDiff,
			ChangedPaths: []string{"greet.go"}, Proofs: corpusRunProofs, Verifier: corpusRunVerdict(true)},
		{ID: "d1/task_b/e1/behaviour_absent", Delivery: "d1", Task: "task_b", Execution: 1, Label: "behaviour_absent",
			Report: "Updated `greet.go` so that it greets twice.", Diff: corpusRunDiff,
			ChangedPaths: []string{"greet.go"}, Proofs: corpusRunProofs, Verifier: corpusRunVerdict(false)},
		{ID: "d1/task_c/e1/fabricated_reference", Delivery: "d1", Task: "task_c", Execution: 1, Label: "fabricated_reference",
			Report: "Added `GreetHandlerChecked` to `greet.go`.", Diff: corpusRunDiff,
			ChangedPaths: []string{"greet.go"}, Proofs: corpusRunProofs, Verifier: corpusRunVerdict(false)},
		{ID: "d1/task_d/e1/clean", Delivery: "d1", Task: "task_d", Execution: 1, Label: "clean",
			Report: "Wrote `greet.go`.\nBATUTA-PROGRESS 1 DONE", Diff: corpusRunDiff,
			ChangedPaths: []string{"greet.go"}, Proofs: corpusRunProofs, Verifier: corpusRunVerdict(false)},
	})
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "corpus", "run", "--corpus", corpus, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge corpus run = %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	wantLines := []string{
		"threshold=0.9",
		"d1/task_a/e1/clean label=clean flagged=false settled_by=none max_contradicted=0.00 asked=false",
		"d1/task_b/e1/behaviour_absent label=behaviour_absent flagged=true settled_by=judge max_contradicted=0.93 asked=true",
		"d1/task_c/e1/fabricated_reference label=fabricated_reference flagged=true settled_by=code max_contradicted=0.00 asked=false",
		"d1/task_d/e1/clean label=clean flagged=false settled_by=none max_contradicted=0.00 asked=true unavailable=server_error",
		"label cases flagged_by_code flagged_by_judge uncertain missed false_flags unavailable",
		"behaviour_absent 1 0 1 0 0 0 0",
		"clean 2 0 0 0 0 0 1",
		"fabricated_reference 1 1 0 0 0 0 0",
		"judge calls=2 input_tokens=12 unavailable=1",
	}
	if !slices.Equal(lines, wantLines) {
		t.Fatalf("stdout lines =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(wantLines, "\n"))
	}
	if record.calls["task_a"] != 0 || record.calls["task_c"] != 0 {
		t.Fatalf("judge calls = %v, want none for the cases with settled claims", record.calls)
	}
	questions, ok := record.bodies["task_b"]["questions"].(map[string]any)
	if !ok || len(questions) != 2 {
		t.Fatalf("task_b questions = %#v, want one relation and one material", record.bodies["task_b"]["questions"])
	}
	state, ok := record.bodies["task_b"]["state"].(map[string]any)
	if !ok {
		t.Fatalf("task_b state = %#v", record.bodies["task_b"]["state"])
	}
	if diff, ok := state["diff"].(string); !ok || !strings.Contains(diff, "+func GreetHandler") {
		t.Fatalf("task_b state diff = %#v, want the bounded diff slice", state["diff"])
	}

	var stdoutJSON, stderrJSON strings.Builder
	err = run([]string{"judge", "corpus", "run", "--corpus", corpus, "--workspace", root, "--base-url", server.URL, "--json"}, &stdoutJSON, &stderrJSON)
	if err != nil {
		t.Fatalf("judge corpus run --json = %v\nstdout: %s\nstderr: %s", err, stdoutJSON.String(), stderrJSON.String())
	}
	jsonLines := strings.Split(strings.TrimSuffix(stdoutJSON.String(), "\n"), "\n")
	if len(jsonLines) != 5 {
		t.Fatalf("--json printed %d lines, want 4 cases plus the summary:\n%s", len(jsonLines), stdoutJSON.String())
	}
	wantCaseJSON := []map[string]any{
		{"id": "d1/task_a/e1/clean", "label": "clean", "flagged": false, "settled_by": "none", "max_contradicted": 0.0, "asked": false, "uncertain": 0},
		{"id": "d1/task_b/e1/behaviour_absent", "label": "behaviour_absent", "flagged": true, "settled_by": "judge", "max_contradicted": 0.93, "asked": true, "uncertain": 0},
		{"id": "d1/task_c/e1/fabricated_reference", "label": "fabricated_reference", "flagged": true, "settled_by": "code", "max_contradicted": 0.0, "asked": false, "uncertain": 0},
		{"id": "d1/task_d/e1/clean", "label": "clean", "flagged": false, "settled_by": "none", "max_contradicted": 0.0, "asked": true, "uncertain": 0, "unavailable": "server_error"},
	}
	for index, want := range wantCaseJSON {
		var object map[string]any
		if err := json.Unmarshal([]byte(jsonLines[index]), &object); err != nil {
			t.Fatalf("case line %d is not JSON: %v\n%s", index+1, err, jsonLines[index])
		}
		for key, want := range want {
			got, ok := object[key]
			if !ok {
				t.Fatalf("case %d is missing %q:\n%s", index+1, key, jsonLines[index])
			}
			if number, isNumber := want.(int); isNumber {
				decoded, isFloat := got.(float64)
				if !isFloat || decoded != float64(number) {
					t.Fatalf("case %d %s = %#v, want %d:\n%s", index+1, key, got, number, jsonLines[index])
				}
				continue
			}
			if got != want {
				t.Fatalf("case %d %s = %#v, want %#v:\n%s", index+1, key, got, want, jsonLines[index])
			}
		}
	}
	var summary struct {
		Summary struct {
			Threshold float64 `json:"threshold"`
			Labels    []struct {
				Label        string `json:"label"`
				Cases        int    `json:"cases"`
				FlaggedCode  int    `json:"flagged_by_code"`
				FlaggedJudge int    `json:"flagged_by_judge"`
				Unavailable  int    `json:"unavailable"`
			} `json:"labels"`
			JudgeCalls  int  `json:"judge_calls"`
			InputTokens *int `json:"input_tokens"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(jsonLines[4]), &summary); err != nil {
		t.Fatalf("summary line is not JSON: %v\n%s", err, jsonLines[4])
	}
	if summary.Summary.Threshold != 0.9 || summary.Summary.JudgeCalls != 2 || summary.Summary.InputTokens == nil || *summary.Summary.InputTokens != 12 {
		t.Fatalf("summary = %+v", summary.Summary)
	}
	if len(summary.Summary.Labels) != 3 {
		t.Fatalf("summary labels = %#v, want one row per label", summary.Summary.Labels)
	}
}

func TestCorpusRunSummary(t *testing.T) {
	server, record := corpusRunServer(t, map[string]corpusRunAnswers{
		"task_f": {choice: "verifier_incomplete", confidence: 0.93, material: 0.93, inputTokens: 12},
		"task_u": {choice: "behaviour_absent", confidence: 0.5, material: 0.9, inputTokens: 12},
	})
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	corpus := corpusRunFixture(t, root, []corpusCase{
		{ID: "d2/task_f/e1/clean", Delivery: "d2", Task: "task_f", Execution: 1, Label: "clean",
			Report: "Wrote `greet.go`.\nBATUTA-PROGRESS 1 DONE", Diff: corpusRunDiff,
			ChangedPaths: []string{"greet.go"}, Proofs: corpusRunProofs, Verifier: corpusRunVerdict(false)},
		{ID: "d2/task_u/e1/behaviour_absent", Delivery: "d2", Task: "task_u", Execution: 1, Label: "behaviour_absent",
			Report: "Updated `greet.go` so that it greets twice.", Diff: corpusRunDiff,
			ChangedPaths: []string{"greet.go"}, Proofs: corpusRunProofs, Verifier: corpusRunVerdict(false)},
		{ID: "d2/task_k/e1/wrong_count", Delivery: "d2", Task: "task_k", Execution: 1, Label: "wrong_count",
			Report: "Added 4 new tests in `greet_test.go`.", Diff: corpusRunDiff,
			ChangedPaths: []string{"greet_test.go"}, Proofs: corpusRunProofs, Verifier: corpusRunVerdict(false)},
	})
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "corpus", "run", "--corpus", corpus, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge corpus run = %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	wantTail := []string{
		"behaviour_absent 1 0 1 0 0 0 0",
		"clean 1 0 1 0 0 1 0",
		"wrong_count 1 1 0 0 0 0 0",
		"judge calls=2 input_tokens=24",
	}
	if !slices.Equal(lines[len(lines)-4:], wantTail) {
		t.Fatalf("summary =\n%s\nwant\n%s", strings.Join(lines[len(lines)-4:], "\n"), strings.Join(wantTail, "\n"))
	}
	if record.calls["task_k"] != 0 {
		t.Fatalf("judge calls = %v, want none for the code-settled case", record.calls)
	}

	unknown := corpusRunFixture(t, root, []corpusCase{
		{ID: "d3/task_f/e1/clean", Delivery: "d3", Task: "task_f", Execution: 1, Label: "clean",
			Report: "Wrote `greet.go`.\nBATUTA-PROGRESS 1 DONE", Diff: corpusRunDiff,
			ChangedPaths: []string{"greet.go"}, Proofs: corpusRunProofs, Verifier: corpusRunVerdict(false)},
	})
	routes := map[string]corpusRunAnswers{
		"task_f": {choice: "verifier_incomplete", confidence: 0.93, material: 0.93, inputTokens: -1},
	}
	serverUnknown, _ := corpusRunServer(t, routes)
	var stdoutNoUsage, stderrNoUsage strings.Builder
	err = run([]string{"judge", "corpus", "run", "--corpus", unknown, "--workspace", root, "--base-url", serverUnknown.URL}, &stdoutNoUsage, &stderrNoUsage)
	if err != nil {
		t.Fatalf("judge corpus run = %v\nstdout: %s", err, stdoutNoUsage.String())
	}
	if !strings.HasSuffix(strings.TrimSuffix(stdoutNoUsage.String(), "\n"), "judge calls=1 input_tokens=unknown") {
		t.Fatalf("footer = %q, want input_tokens=unknown", strings.TrimSpace(stdoutNoUsage.String()))
	}
}

func TestCorpusRunThreshold(t *testing.T) {
	server, _ := corpusRunServer(t, nil)
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY","decisions":{"claim_evidence":{"mode":"shadow","threshold":0.8}}}`)
	corpus := corpusRunFixture(t, root, []corpusCase{
		{ID: "d4/task_a/e1/clean", Delivery: "d4", Task: "task_a", Execution: 1, Label: "clean",
			Report: "Wrote `greet.go`.\nBATUTA-PROGRESS 1 DONE", Diff: corpusRunDiff,
			ChangedPaths: []string{"greet.go"}, Proofs: corpusRunProofs, Verifier: corpusRunVerdict(false)},
	})
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "corpus", "run", "--corpus", corpus, "--workspace", root, "--base-url", server.URL}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("judge corpus run = %v\nstdout: %s", err, stdout.String())
	}
	if first := strings.SplitN(stdout.String(), "\n", 2)[0]; first != "threshold=0.8" {
		t.Fatalf("first line = %q, want threshold=0.8 from the judge config", first)
	}
	var stdoutFlag, stderrFlag strings.Builder
	if err := run([]string{"judge", "corpus", "run", "--corpus", corpus, "--workspace", root, "--base-url", server.URL, "--threshold", "0.5"}, &stdoutFlag, &stderrFlag); err == nil {
		t.Fatalf("judge corpus run accepted a --threshold flag, want an error: %s", stdoutFlag.String())
	}
}

func TestCorpusRunRejectsInvalidArguments(t *testing.T) {
	root := judgeTestWorkspace(t, `{"provider":"typesafe","model":"jev-test","key_env":"JUDGE_TEST_KEY"}`)
	broken := filepath.Join(root, "broken.jsonl")
	if err := os.WriteFile(broken, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing corpus flag", []string{"judge", "corpus", "run", "--workspace", root}},
		{"missing corpus file", []string{"judge", "corpus", "run", "--corpus", filepath.Join(root, "nope.jsonl"), "--workspace", root}},
		{"invalid corpus json", []string{"judge", "corpus", "run", "--corpus", broken, "--workspace", root}},
		{"positional argument", []string{"judge", "corpus", "run", "--corpus", broken, "--workspace", root, "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if err := run(tc.args, &stdout, &stderr); err == nil {
				t.Fatalf("judge corpus run = nil, want an error")
			}
		})
	}
}
