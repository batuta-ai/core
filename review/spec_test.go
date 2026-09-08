package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	for _, want := range []string{"1. [task-1.1] Build parser: parses every record", "Proof: go test ./parser", "3. [task-2.1] Wire command: prints the verdict", "parser.go (+8 -2)"} {
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
