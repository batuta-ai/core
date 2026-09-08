package review

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

func TestIncrementalSinceLastHead(t *testing.T) {
	root := reviewRepo(t)
	writeTestFile(t, root, "tracked.go", "base\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	base := gitTest(t, root, "rev-parse", "HEAD")
	writeTestFile(t, root, "tracked.go", "round one\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "round one")
	lastHead := gitTest(t, root, "rev-parse", "HEAD")
	writeTestFile(t, root, "tracked.go", "round two\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "round two")

	statePath := filepath.Join(root, ".batuta", "reviews", "delivery", "state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(ReviewState{Head: lastHead})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	incremental, err := ResolveReviewBase(root, "delivery", base, false)
	if err != nil || incremental != lastHead {
		t.Fatalf("incremental=%q err=%v", incremental, err)
	}
	full, err := ResolveReviewBase(root, "delivery", base, true)
	if err != nil || full != base {
		t.Fatalf("full=%q err=%v", full, err)
	}
	first, err := ResolveReviewBase(root, "first-review", base, false)
	if err != nil || first != base {
		t.Fatalf("first=%q err=%v", first, err)
	}
}

func TestSpecCriteriaBecomeRules(t *testing.T) {
	plan := routing.Plan{Tasks: []routing.PlanTask{
		{Number: 1, TaskArtifact: routing.TaskArtifact{Title: "Build parser"}, Accept: []string{"parses every record → go test ./parser", "rejects malformed input"}},
		{Number: 2, TaskArtifact: routing.TaskArtifact{Title: "Wire command"}, Accept: []string{"prints the verdict -> go test ./cmd"}},
	}}
	rules := SpecCriteria(plan)
	if len(rules) != 3 {
		t.Fatalf("rules=%+v", rules)
	}
	if rules[0].ID != "task-1.1" || rules[0].Task != "Build parser" || rules[0].Text != "parses every record" || rules[0].Proof != "go test ./parser" {
		t.Fatalf("first rule=%+v", rules[0])
	}
	if rules[1].ID != "task-1.2" || rules[2].ID != "task-2.1" {
		t.Fatalf("rule identifiers=%+v", rules)
	}

	prompt := BuildSpecPrompt(Manifest{Base: "abc", Files: []File{{Path: "parser.go", Added: 8, Deleted: 2, Selected: true}}}, rules)
	if strings.Contains(prompt, "Proof:") {
		t.Fatalf("prompt contains proof command: %s", prompt)
	}
	for _, want := range []string{"1. [task-1.1] Build parser: parses every record", "3. [task-2.1] Wire command: prints the verdict", "parser.go (+8 -2)"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestSpecSweepVerdicts(t *testing.T) {
	manifest, runtime, opts := sessionFixture(t, 1)
	rules := []SpecRule{
		{ID: "task-1.1", Task: "Parser", Text: "parses input"},
		{ID: "task-2.1", Task: "Command", Text: "prints output"},
		{ID: "task-2.2", Task: "Command", Text: "supports legacy mode"},
	}
	useReviewRunner(&opts, func(_ context.Context, cmd publication.Command) (publication.CommandResult, error) {
		prompt := cmd.Args[len(cmd.Args)-1]
		if strings.Contains(prompt, "Cohort hunks") || !strings.Contains(prompt, "Diff summary") {
			t.Errorf("not a dedicated sweep prompt: %s", prompt)
		}
		return publication.CommandResult{Stdout: []byte("<<<CRITERIA\n" +
			`{"id":"task-1.1","status":"satisfied","path":"parser.go:1 exercises the parser"}` + "\n" +
			`{"id":"task-2.1","status":"violated","path":"file0.go:1 omits the output"}` + "\n" +
			`{"id":"task-2.2","status":"not-applicable","path":"the diff does not touch legacy mode"}` + "\n" +
			"CRITERIA>>>\n")}, nil
	})

	sweep, err := RunSpecSweep(t.Context(), manifest, rules, runtime, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !sweep.Covered || len(sweep.Results) != 3 || len(sweep.Attempts) != 1 {
		t.Fatalf("sweep=%+v", sweep)
	}
	if sweep.Results[1].Status != CriterionViolated || !sweep.Results[1].Violated() {
		t.Fatalf("violated result=%+v", sweep.Results[1])
	}
	criteria := sweep.VerdictCriteria()
	if len(criteria) != 3 || !criteria[1].Violated || Verdict(nil, criteria) != Rework {
		t.Fatalf("criteria=%+v verdict=%s", criteria, Verdict(nil, criteria))
	}
}

func TestLoadSpecCriteriaFromPathAndDone(t *testing.T) {
	root := t.TempDir()
	plan := func(criterion string) string {
		return "# Plan — Spec\n**Goal:** Review\n**Status:** approved\n## Tasks\n- [ ] 1. Check — docs/low\n      Accept: " + criterion + "\n"
	}
	writeTestFile(t, root, ".batuta/plans/delivery.md", plan("active criterion"))
	writeTestFile(t, root, ".batuta/plans/done/delivery.md", plan("archived criterion"))
	writeTestFile(t, root, ".batuta/plans/done/archive.md", plan("archive-only criterion"))
	writeTestFile(t, root, ".batuta/plan-legacy.md", plan("legacy criterion"))
	for _, tc := range []struct{ spec, want string }{
		{"delivery", "active criterion"},
		{"archive", "archive-only criterion"},
		{"legacy", "legacy criterion"},
		{".batuta/plans/done/delivery.md", "archived criterion"},
		{filepath.Join(root, ".batuta/plans/done/delivery.md"), "archived criterion"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			rules, err := LoadSpecCriteria(root, tc.spec)
			if err != nil || len(rules) != 1 || rules[0].Text != tc.want {
				t.Fatalf("rules=%+v err=%v, want %q", rules, err, tc.want)
			}
		})
	}
}

func TestLoadSpecCriteriaPreservesNumericSlugs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".batuta/plans/done/2026-review.md", "# Plan — Spec\n**Goal:** Review\n**Status:** approved\n## Tasks\n- [ ] 1. Check — docs/low\n      Accept: numeric slug works\n")
	rules, err := LoadSpecCriteria(root, "2026-review")
	if err != nil || len(rules) != 1 || rules[0].Text != "numeric slug works" {
		t.Fatalf("rules=%+v err=%v", rules, err)
	}
}

func TestParseSpecResultsRejectsDuplicateFields(t *testing.T) {
	for _, line := range []string{
		`{"id":"task-1.1","status":"violated","status":"satisfied","path":"evidence"}`,
		`{"id":"task-1.1","status":"violated","Status":"satisfied","path":"evidence"}`,
		`{"id":"task-1.1","status":"satisfied","path":"first","path":"second"}`,
	} {
		assertInvalidSpecOutput(t, line)
	}
}

func TestParseSpecResultsRejectsTrailingJSON(t *testing.T) {
	for _, tail := range []string{` {"status":"violated"}`, ` trailing`, ` null`} {
		assertInvalidSpecOutput(t, `{"id":"task-1.1","status":"satisfied","path":"evidence"}`+tail)
	}
}

func assertInvalidSpecOutput(t *testing.T, line string) {
	t.Helper()
	output := "<<<CRITERIA\n" + line + "\nCRITERIA>>>\n"
	rules := []SpecRule{{ID: "task-1.1", Text: "criterion"}}
	if got, err := parseSpecResults(output, rules); err == nil || len(got) != 0 {
		t.Errorf("accepted %q: %+v, %v", line, got, err)
	}
	manifest, runtime, opts := sessionFixture(t, 1)
	useReviewRunner(&opts, func(context.Context, publication.Command) (publication.CommandResult, error) {
		return publication.CommandResult{Stdout: []byte(output)}, nil
	})
	sweep, err := RunSpecSweep(t.Context(), manifest, rules, runtime, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sweep.Covered || sweep.Reason == "" || len(sweep.Results) != 0 {
		t.Fatalf("malformed output covered: %+v", sweep)
	}
	if report := BuildReport(manifest, []CohortResult{{Covered: true}}, &sweep); report.Verdict != Rework {
		t.Fatal("malformed sweep can ship")
	}
}

func TestSpecProofsRunInTree(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		code   int
		runErr error
		status CriterionStatus
		path   string
	}{
		{"pass", 0, nil, CriterionSatisfied, "proof: check input exited 0"},
		{"fail", 7, nil, CriterionViolated, "proof: check input exited 7"},
		{"cannot run", -1, errors.New("unavailable"), CriterionViolated, "proof: check input could not run: unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := reviewRepo(t)
			calls := 0
			shell := gates.ShellRunner{Shell: "sh", Runner: reviewCommandRunner(func(_ context.Context, cmd publication.Command) (publication.CommandResult, error) {
				calls++
				if cmd.Directory != root || !reflect.DeepEqual(cmd.Args, []string{"-c", "check input"}) {
					t.Fatalf("proof invocation = %+v", cmd)
				}
				return publication.CommandResult{ExitCode: tc.code}, tc.runErr
			})}
			rules := []SpecRule{{ID: "task-1.1", Text: "input", Proof: "check input"}, {ID: "task-1.2", Text: "manual"}}
			results, remaining, err := RunSpecProofs(t.Context(), root, rules, shell)
			want := []SpecResult{{ID: rules[0].ID, Status: tc.status, Path: tc.path}}
			if err != nil || calls != 1 || !reflect.DeepEqual(results, want) || !reflect.DeepEqual(remaining, rules[1:]) {
				t.Fatalf("results=%+v remaining=%+v calls=%d err=%v", results, remaining, calls, err)
			}
		})
	}
}

func TestSpecPromptOmitsProofRules(t *testing.T) {
	t.Parallel()
	rules := []SpecRule{{ID: "mechanical", Text: "mechanical check", Proof: "true"}, {ID: "manual", Text: "manual check"}}
	shell := gates.ShellRunner{Shell: "sh", Runner: reviewCommandRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
		return publication.CommandResult{}, nil
	})}
	_, remaining, err := RunSpecProofs(t.Context(), reviewRepo(t), rules, shell)
	if err != nil {
		t.Fatal(err)
	}
	prompt := BuildSpecPrompt(Manifest{}, remaining)
	if strings.Contains(prompt, "mechanical") || strings.Contains(prompt, "Proof:") || !strings.Contains(prompt, "manual check") {
		t.Fatalf("prompt = %s", prompt)
	}
}

func TestSpecSweepMergesProofAndSweepResults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, output string
		covered      bool
	}{
		{"complete", `{"id":"manual","status":"satisfied","path":"file0.go:1"}`, true},
		{"missing", "", false},
		{"extra proof result", `{"id":"manual","status":"satisfied","path":"file0.go:1"}` + "\n" + `{"id":"first","status":"satisfied","path":"file0.go:1"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			manifest, runtime, opts := sessionFixture(t, 1)
			rules := []SpecRule{{ID: "first", Proof: "true"}, {ID: "manual", Text: "manual check"}, {ID: "last", Proof: "true"}}
			shell := gates.ShellRunner{Shell: "sh", Runner: reviewCommandRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
				return publication.CommandResult{}, nil
			})}
			proofs, remaining, err := RunSpecProofs(t.Context(), opts.Root, rules, shell)
			if err != nil {
				t.Fatal(err)
			}
			useReviewRunner(&opts, func(_ context.Context, cmd publication.Command) (publication.CommandResult, error) {
				prompt := cmd.Args[len(cmd.Args)-1]
				if strings.Contains(prompt, "[first]") || strings.Contains(prompt, "[last]") || !strings.Contains(prompt, "[manual]") {
					t.Fatalf("prompt=%s", prompt)
				}
				return publication.CommandResult{Stdout: []byte("<<<CRITERIA\n" + tc.output + "\nCRITERIA>>>\n")}, nil
			})
			sweep, err := RunSpecSweep(t.Context(), manifest, remaining, runtime, opts)
			if err != nil || sweep.Covered != tc.covered {
				t.Fatalf("sweep=%+v err=%v", sweep, err)
			}
			sweep.Results = MergeSpecResults(rules, proofs, sweep.Results)
			if tc.covered {
				if len(sweep.Results) != len(rules) {
					t.Fatalf("results=%+v", sweep.Results)
				}
				for i, rule := range rules {
					if sweep.Results[i].ID != rule.ID {
						t.Fatalf("results=%+v", sweep.Results)
					}
				}
			} else if len(sweep.Results) != 2 || BuildReport(manifest, nil, &sweep).Verdict != Rework {
				t.Fatalf("uncovered sweep=%+v", sweep)
			}
		})
	}
}
