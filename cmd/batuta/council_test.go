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
	"runtime"
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

func TestCouncilPromptHasProfileConventions(t *testing.T) {
	root := councilRepo(t)
	t.Chdir(root)
	profile := "Stack: Go\nMethodology: TDD\nTest: go test ./...\nTemplate: templates/generic.md\n\n## Conventions\nOnly-in-profile rule.\n"
	if err := os.WriteFile(filepath.Join(root, ".batuta", "profile.md"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	var critiquePrompts []string
	script := councilScript("APPROVE")
	stubCouncilSessions(t, func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
		prompt := command.Args[5]
		if !strings.HasPrefix(prompt, "Review the other counsellors") && !strings.HasPrefix(prompt, "Synthesize this council") {
			critiquePrompts = append(critiquePrompts, prompt)
		}
		return script(ctx, command)
	})
	var stdout, stderr bytes.Buffer
	if err := run([]string{"council", "--plan", "plan-demo.md"}, &stdout, &stderr); err != nil {
		t.Fatalf("council: %v\n%s", err, &stderr)
	}
	if len(critiquePrompts) != 3 {
		t.Fatalf("critique prompts = %d, want 3", len(critiquePrompts))
	}
	for _, prompt := range critiquePrompts {
		profileAt := strings.Index(prompt, "Only-in-profile rule.")
		templateAt := strings.Index(prompt, "Keep changes scoped.")
		if profileAt < 0 || templateAt < 0 || profileAt > templateAt {
			t.Errorf("prompt must carry the profile rule before the template rule (profile=%d template=%d):\n%s", profileAt, templateAt, prompt)
		}
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

func TestCouncilArtefactsAtomic(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop writes on this platform or user")
	}
	directory := tempDir(t)
	old := map[string][]byte{"council.json": []byte("old json\n"), "council.md": []byte("old md\n")}
	for name, content := range old {
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(directory, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(directory, 0o755) })
	err := writeCouncilArtifacts(directory, map[string][]byte{"council.json": []byte("new json\n"), "council.md": []byte("new md\n")})
	if err == nil {
		t.Fatal("writeCouncilArtifacts succeeded in a read-only directory")
	}
	for name, want := range old {
		got, readErr := os.ReadFile(filepath.Join(directory, name))
		if readErr != nil || !bytes.Equal(got, want) {
			t.Errorf("%s = %q (%v), want the previous %q", name, got, readErr, want)
		}
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != len(old) {
		t.Errorf("directory holds %d entries, want only the %d artefacts", len(entries), len(old))
	}
}

func TestCouncilArtefactsReplaced(t *testing.T) {
	root := councilRepo(t)
	t.Chdir(root)
	directory := filepath.Join(root, ".batuta", "councils", "2026-10-08-demo")
	var reports []string
	for _, verdict := range []string{"REVISE", "APPROVE"} {
		stubCouncilSessions(t, councilScript(verdict))
		var stdout, stderr bytes.Buffer
		run([]string{"council", "--plan", "plan-demo.md"}, &stdout, &stderr)
		reports = append(reports, stdout.String())
		written, err := os.ReadFile(filepath.Join(directory, "council.md"))
		if err != nil || string(written) != stdout.String() {
			t.Fatalf("%s run: council.md = %q (%v), want stdout %q", verdict, written, err, stdout.String())
		}
		payload, err := os.ReadFile(filepath.Join(directory, "council.json"))
		if err != nil || !strings.Contains(string(payload), `"recommendation": "`+verdict+`"`) {
			t.Fatalf("%s run: council.json = %s (%v)", verdict, payload, err)
		}
	}
	if reports[0] == reports[1] {
		t.Error("the two runs produced the same report")
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 2 {
		t.Errorf("directory holds %d entries, want council.json and council.md only", len(entries))
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

func TestCouncilIncompleteCrossReview(t *testing.T) {
	base := councilScript("APPROVE")
	runner := councilTestRunner(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
		model, prompt := command.Args[2], command.Args[5]
		if strings.HasPrefix(prompt, "Review the other counsellors") && model != "gamma-model" {
			return publication.CommandResult{Stdout: []byte("no ranking here")}, nil
		}
		return base(ctx, command)
	})
	root := councilRepo(t)
	t.Chdir(root)
	stubCouncilSessions(t, runner)
	var stdout, stderr bytes.Buffer
	err := run([]string{"council", "--plan", "plan-demo.md"}, &stdout, &stderr)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 4 {
		t.Fatalf("council error = %#v, want exit 4", err)
	}
	report := stdout.String()
	synthesis := strings.Index(report, "## Chairman synthesis")
	note := strings.Index(report, "fewer than two cross-reviews parsed")
	if !strings.Contains(report, "Recommendation: INCOMPLETE") || note < 0 || synthesis < 0 || note > synthesis {
		t.Errorf("council.md = %s", report)
	}
	written, readErr := os.ReadFile(filepath.Join(root, ".batuta", "councils", "2026-10-08-demo", "council.md"))
	if readErr != nil || string(written) != report {
		t.Errorf("council.md on disk = %q, %v", written, readErr)
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

func TestCouncilFailureTailRedacted(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr, secret string
	}{
		{name: "api_key on stdout", stdout: `{"api_key":"sk-live"}`, secret: "sk-live"},
		{name: "token on stderr", stderr: `{"token":"tok-example"}`, secret: "tok-example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := councilRepo(t)
			t.Chdir(root)
			healthy := councilScript("REVISE")
			stubCouncilSessions(t, func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
				if command.Args[2] == "alpha-model" {
					return publication.CommandResult{Stdout: []byte(tc.stdout + "\nkept line"), Stderr: []byte(tc.stderr), ExitCode: 1}, nil
				}
				return healthy(ctx, command)
			})
			var stdout, stderr bytes.Buffer
			_ = run([]string{"council", "--plan", "plan-demo.md"}, &stdout, &stderr)
			directory := filepath.Join(root, ".batuta", "councils", "2026-10-08-demo")
			for _, name := range []string{"council.json", "council.md"} {
				payload, err := os.ReadFile(filepath.Join(directory, name))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(payload), tc.secret) {
					t.Errorf("%s holds %q:\n%s", name, tc.secret, payload)
				}
				if name == "council.json" && !strings.Contains(string(payload), "kept line") {
					t.Errorf("%s lost the non-secret tail line:\n%s", name, payload)
				}
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
