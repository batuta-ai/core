package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/council"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/publication"
)

type councilTestRunner func(context.Context, publication.Command) (publication.CommandResult, error)

func (f councilTestRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	return f(ctx, command)
}

const councilPlan = "# Plan\n\n1. Build the thing\n"

func councilRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root, err := filepath.EvalSymlinks(tempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_AUTHOR_NAME=Council Test", "GIT_AUTHOR_EMAIL=council@example.test",
			"GIT_COMMITTER_NAME=Council Test", "GIT_COMMITTER_EMAIL=council@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	files := map[string]string{
		".gitignore":         ".batuta/councils/\nout/\n",
		".batuta/profile.md": "Stack: Go\nMethodology: TDD\nTest: go test ./...\nTemplate: templates/generic.md\n",
		".batuta/routing.md": "| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| high | * | chair | chair-model |\n\n| Role | Lane | Executor | Model |\n|---|---|---|---|\n| council | — | alpha | alpha-model |\n| council | — | beta | beta-model |\n| council | — | gamma | gamma-model |\n",
		"plan-demo.md":       councilPlan,
	}
	for name, payload := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("-c", "commit.gpgsign=false", "commit", "-qm", "base")
	return root
}

func stubCouncilSessions(t *testing.T, runner councilTestRunner) {
	t.Helper()
	skills := tempDir(t)
	files := map[string]string{"templates/generic.md": "## Conventions for briefs\nKeep changes scoped.\n"}
	for _, name := range []string{"alpha", "beta", "gamma", "chair"} {
		files["adapters/"+name+".md"] = fmt.Sprintf("---\nname: %s\nrun: fake --write {brief}\nreadonly: fake --read-only {model_flags} --effort {effort} {prompt}\nmodel_flags: --model {model}\navailable: fake --version\nmodels: fake models\nfinished: exit_code\n---\n", name)
	}
	for name, payload := range files {
		path := filepath.Join(skills, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := councilRunOptions
	councilRunOptions = func(root string, parallel int, timeout time.Duration) council.RunOptions {
		return council.RunOptions{
			Root: root, Skills: skills, Parallel: parallel, Timeout: timeout,
			Subprocess: &executor.Subprocess{Runner: runner, Lookup: func(string) (string, error) { return filepath.Join(skills, "fake"), nil }},
		}
	}
	previousNow := reviewNow
	reviewNow = func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { councilRunOptions = previous; reviewNow = previousNow })
}

// councilScript answers each stage with the given critique verdict; models in
// breakModels return an unparsable critique.
func councilScript(verdict string, breakModels ...string) councilTestRunner {
	return func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		model, prompt := command.Args[2], command.Args[5]
		switch {
		case strings.HasPrefix(prompt, "Review the other counsellors"):
			var ids, labels []string
			for _, line := range strings.Split(prompt, "\n") {
				if label, ok := strings.CutPrefix(line, "Critique "); ok {
					labels = append(labels, label)
				} else if id, _, ok := strings.Cut(line, ": task "); ok {
					ids = append(ids, id)
				}
			}
			var b strings.Builder
			for _, id := range ids {
				b.WriteString(id + ": AGREE\n")
			}
			b.WriteString("FINAL RANKING:\n")
			for i, label := range labels {
				fmt.Fprintf(&b, "%d. %s\n", i+1, label)
			}
			return publication.CommandResult{Stdout: []byte(b.String())}, nil
		case strings.HasPrefix(prompt, "Synthesize this council"):
			return publication.CommandResult{Stdout: []byte("Chairman: fix task 1 first.")}, nil
		}
		for _, broken := range breakModels {
			if model == broken {
				return publication.CommandResult{Stdout: []byte("no block here")}, nil
			}
		}
		if verdict == "APPROVE" {
			return publication.CommandResult{Stdout: []byte("<<<COUNCIL\nCOUNCIL>>>\nVERDICT: APPROVE\n")}, nil
		}
		answer := fmt.Sprintf("<<<COUNCIL\n{\"task\":1,\"severity\":\"blocker\",\"claim\":\"Proof is vacuous\",\"fix\":\"Name a real proof\"}\nCOUNCIL>>>\nVERDICT: %s\n", verdict)
		return publication.CommandResult{Stdout: []byte(answer)}, nil
	}
}

func TestCouncilCommand(t *testing.T) {
	root := councilRepo(t)
	t.Chdir(root)
	stubCouncilSessions(t, councilScript("REVISE"))
	var stdout, stderr bytes.Buffer
	err := run([]string{"council", "--plan", "plan-demo.md"}, &stdout, &stderr)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("council error = %#v, want exit 2\n%s", err, &stderr)
	}
	directory := filepath.Join(root, ".batuta", "councils", "2026-10-08-demo")
	printed, err := os.ReadFile(filepath.Join(directory, "council.md"))
	if err != nil || string(printed) != stdout.String() {
		t.Fatalf("council.md does not match stdout: %v\nfile=%q\nout=%q", err, printed, stdout.String())
	}
	for _, want := range []string{"Recommendation: REVISE", "never approves", "Chairman: fix task 1 first.", "Proof is vacuous", "support 3/3", "| 1 |", "## Failures"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("council.md is missing %q:\n%s", want, &stdout)
		}
	}
	payload, err := os.ReadFile(filepath.Join(directory, "council.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		PlanDigest   string                       `json:"plan_digest"`
		Critiques    []council.Critique           `json:"critiques"`
		CrossReviews []council.CrossReview        `json:"cross_reviews"`
		Aggregate    council.AggregateResult      `json:"aggregate"`
		Failures     []council.CouncilFailure     `json:"failures"`
		Labels       map[string]map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("council.json: %v\n%s", err, payload)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(councilPlan)))
	if got.PlanDigest != digest || len(got.Critiques) != 3 || len(got.CrossReviews) != 3 || got.Aggregate.Recommendation != council.Revise || got.Failures == nil || got.Labels["A"]["model"] != "alpha-model" || got.Labels["chairman"]["model"] != "chair-model" {
		t.Errorf("council.json = %+v (want digest %s)\n%s", got, digest, payload)
	}
}

func TestCouncilCommandOut(t *testing.T) {
	root := councilRepo(t)
	t.Chdir(root)
	stubCouncilSessions(t, councilScript("APPROVE"))
	var stdout, stderr bytes.Buffer
	if err := run([]string{"council", "--plan", "plan-demo.md", "--parallel", "2", "--timeout", "30s", "--out", "out"}, &stdout, &stderr); err != nil {
		t.Fatalf("council = %v\n%s", err, &stderr)
	}
	for _, name := range []string{"council.json", "council.md"} {
		if _, err := os.Stat(filepath.Join(root, "out", name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !strings.Contains(stdout.String(), "Recommendation: APPROVE") {
		t.Errorf("council.md = %s", &stdout)
	}
}

func TestCouncilExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runner councilTestRunner
		want   int
	}{
		{name: "approve", runner: councilScript("APPROVE"), want: 0},
		{name: "revise", runner: councilScript("REVISE"), want: 2},
		{name: "fewer than two critiques", runner: councilScript("REVISE", "alpha-model", "beta-model"), want: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := councilRepo(t)
			t.Chdir(root)
			stubCouncilSessions(t, tc.runner)
			var stdout, stderr bytes.Buffer
			err := run([]string{"council", "--plan", "plan-demo.md"}, &stdout, &stderr)
			if tc.want == 0 {
				if err != nil {
					t.Fatalf("council = %v", err)
				}
				return
			}
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != tc.want {
				t.Fatalf("council error = %#v, want exit %d", err, tc.want)
			}
			if tc.want == 4 {
				if _, statErr := os.Stat(filepath.Join(root, ".batuta", "councils", "2026-10-08-demo", "council.json")); statErr != nil {
					t.Errorf("exit 4 must still write artefacts: %v", statErr)
				}
				if !strings.Contains(stdout.String(), "INCOMPLETE") || !strings.Contains(stdout.String(), "unparsable answer") {
					t.Errorf("council.md = %s", &stdout)
				}
			}
		})
	}
}

func TestCouncilErrorsExitOne(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		setup func(t *testing.T, root string)
	}{
		{name: "no plan flag", args: []string{"council"}},
		{name: "missing plan file", args: []string{"council", "--plan", "absent.md"}},
		{name: "negative parallel", args: []string{"council", "--plan", "plan-demo.md", "--parallel", "-1"}},
		{name: "positional argument", args: []string{"council", "--plan", "plan-demo.md", "extra"}},
		{name: "missing routing", args: []string{"council", "--plan", "plan-demo.md"}, setup: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, ".batuta", "routing.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "artefact over tracked file", args: []string{"council", "--plan", "plan-demo.md", "--out", "."}, setup: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "council.md"), []byte("tracked\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("git", "-C", root, "add", "council.md").CombinedOutput(); err != nil {
				t.Fatalf("git add: %v\n%s", err, out)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := councilRepo(t)
			t.Chdir(root)
			stubCouncilSessions(t, councilScript("APPROVE"))
			if tc.setup != nil {
				tc.setup(t, root)
			}
			var stdout, stderr bytes.Buffer
			err := run(tc.args, &stdout, &stderr)
			var exit *ExitError
			if err == nil || errors.As(err, &exit) {
				t.Fatalf("council error = %#v, want a plain error (exit 1)", err)
			}
			if stdout.Len() != 0 {
				t.Errorf("no report expected, got %q", &stdout)
			}
		})
	}
}

func TestCouncilCapability(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range got.Commands {
		found = found || name == "council"
	}
	if !found {
		t.Fatalf("capabilities.commands = %v, missing council", got.Commands)
	}
	for _, want := range []string{"council   --plan <file>", "0 APPROVE", "2 REVISE", "4 incomplete", "council.md"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
}
