package council

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/inventory"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

type councilRunner func(context.Context, publication.Command) (publication.CommandResult, error)

func (f councilRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	return f(ctx, command)
}

func tempDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func councilGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Council Test", "GIT_AUTHOR_EMAIL=council@example.test",
		"GIT_COMMITTER_NAME=Council Test", "GIT_COMMITTER_EMAIL=council@example.test")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func councilFixture(t *testing.T, parallel int, runner councilRunner) (routing.RoutingTable, RunOptions) {
	t.Helper()
	root := tempDir(t)
	councilGit(t, root, "init", "-q")
	councilGit(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "base", "--allow-empty")
	skills := tempDir(t)
	for _, name := range []string{"alpha", "beta", "gamma", "chair"} {
		path := filepath.Join(skills, "adapters", name+".md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		adapter := fmt.Sprintf("---\nname: %s\nrun: fake --write {brief}\nreadonly: fake --read-only {model_flags} --effort {effort} {prompt}\nmodel_flags: --model {model}\navailable: fake --version\nmodels: fake models\nfinished: exit_code\n---\n", name)
		if err := os.WriteFile(path, []byte(adapter), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rows := []routing.RoutingRow{}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		rows = append(rows, routing.RoutingRow{Executor: inventory.ExecutorID(name), Model: name + "-model"})
	}
	table := routing.RoutingTable{Council: rows, Chairman: &routing.RoutingRole{Executor: "chair", Model: "chair-model"}}
	subprocess := executor.Subprocess{Runner: runner, Lookup: func(string) (string, error) { return filepath.Abs("fake") }}
	return table, RunOptions{Root: root, Skills: skills, Parallel: parallel, Timeout: time.Minute, Subprocess: &subprocess}
}

func councilCommand(command publication.Command) (model, prompt string) {
	return command.Args[2], command.Args[5]
}

func critiqueAnswer(claim string) string {
	return fmt.Sprintf("<<<COUNCIL\n{\"task\":1,\"severity\":\"major\",\"claim\":%q,\"fix\":\"Add a proof\"}\nCOUNCIL>>>\nVERDICT: REVISE\n", claim)
}

func crossReviewAnswer(label string) string {
	other := []string{}
	for _, candidate := range []string{"A", "B", "C"} {
		if candidate != label {
			other = append(other, candidate)
		}
	}
	return fmt.Sprintf("%s1: AGREE\n%s1: AGREE\nFINAL RANKING:\n1. %s\n2. %s\n", other[0], other[1], other[0], other[1])
}

func crossReviewTwoAnswer(label string) string {
	other := "A"
	if label == "A" {
		other = "B"
	}
	return fmt.Sprintf("%s1: AGREE\nFINAL RANKING:\n1. %s\n", other, other)
}

func TestRunStages(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	active, maxActive := 0, 0
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	seen := map[string]int{}
	var root string
	runner := councilRunner(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
		model, prompt := councilCommand(command)
		if command.Directory != root || command.Args[0] != "--read-only" || command.Args[1] != "--model" || command.Args[3] != "--effort" || command.Args[4] != "high" {
			t.Errorf("invalid read-only invocation: %+v", command)
		}
		stage := "critique"
		answer := critiqueAnswer(model)
		if strings.HasPrefix(prompt, "Review the other counsellors") {
			stage = "review"
			answer = crossReviewAnswer(strings.ToUpper(model[:1]))
		}
		if strings.HasPrefix(prompt, "Synthesize this council") {
			stage = "chairman"
			answer = "Synthesis for maintainer"
		}
		mu.Lock()
		if stage == "review" && seen["critique"] != 3 || stage == "chairman" && seen["review"] != 3 {
			t.Errorf("stage started before its predecessor completed: %s, %v", stage, seen)
		}
		seen[stage]++
		if stage == "critique" {
			active++
			maxActive = max(maxActive, active)
		}
		mu.Unlock()
		if stage == "critique" {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
				return publication.CommandResult{}, ctx.Err()
			}
			mu.Lock()
			active--
			mu.Unlock()
		}
		return publication.CommandResult{Stdout: []byte(answer)}, nil
	})
	table, opts := councilFixture(t, 2, runner)
	root = opts.Root
	go func() {
		<-started
		<-started
		close(release)
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := Run(ctx, "Plan body", []string{"Convention"}, table, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Critiques) != 3 || len(result.Reviews) != 3 || result.Synthesis != "Synthesis for maintainer" || len(result.Failures) != 0 {
		t.Fatalf("stages result = %+v", result)
	}
	if maxActive != 2 || seen["critique"] != 3 || seen["review"] != 3 || seen["chairman"] != 1 {
		t.Fatalf("concurrency or stage counts: max=%d seen=%v", maxActive, seen)
	}
}

func TestRunParsesRawWhenDecodedDropsLines(t *testing.T) {
	t.Parallel()
	runner := councilRunner(func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		model, prompt := councilCommand(command)
		answer := critiqueAnswer(model)
		if model == "alpha-model" {
			answer = "<<<COUNCIL\n" +
				`{"task":1,"severity":"major","claim":"first finding","fix":"Add a proof"}` + "\n" +
				`{"task":2,"severity":"minor","claim":"second finding","fix":"Clarify the plan"}` + "\n" +
				"COUNCIL>>>\nVERDICT: REVISE\n"
		}
		if strings.HasPrefix(prompt, "Review the other counsellors") {
			answer = crossReviewAnswer(strings.ToUpper(model[:1]))
			if model != "alpha-model" {
				answer = strings.Replace(answer, "A1: AGREE\n", "A1: AGREE\nA2: AGREE\n", 1)
			}
		}
		if strings.HasPrefix(prompt, "Synthesize this council") {
			answer = "Synthesis"
		}
		return publication.CommandResult{Stdout: []byte(answer)}, nil
	})
	table, opts := councilFixture(t, 1, runner)
	path := filepath.Join(opts.Skills, "adapters", "alpha.md")
	adapter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	adapter = []byte(strings.Replace(string(adapter), "finished: exit_code\n", "finished: exit_code\noutput_decoder: cursor-stream-json\n", 1))
	if err := os.WriteFile(path, adapter, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(t.Context(), "Plan body", nil, table, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 0 || len(result.Critiques) != 3 || len(result.Reviews) != 3 {
		t.Fatalf("council result = %+v", result)
	}
	if got := result.Critiques[0].Findings; len(got) != 2 || got[0].Claim != "first finding" || got[1].Claim != "second finding" {
		t.Fatalf("raw findings = %+v", got)
	}
}

func TestRunDecodedRawConflict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		decoded string
		raw     string
	}{
		{"findings", critiqueAnswer("decoded finding"), critiqueAnswer("raw finding")},
		{"verdict", critiqueAnswer("same finding"), strings.Replace(critiqueAnswer("same finding"), "VERDICT: REVISE", "VERDICT: APPROVE", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := councilOutputResult("critique", "A", executor.Result{
				Stdout: []byte(tc.decoded), RawStdout: []byte(tc.raw), Finished: true,
			})
			if result.failure == nil || !strings.Contains(result.failure.Reason, "conflict") {
				t.Fatalf("decoded/raw disagreement = %+v", result)
			}
			if result.output != "" {
				t.Fatalf("conflicting answer was used: %q", result.output)
			}
		})
	}
}

func TestRunFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		failModel  string
		failStage  string
		answer     string
		exitCode   int
		timedOut   bool
		wantReason string
	}{
		{"unparsable critique", "gamma-model", "critique", "not a critique\nOPENAI_API_KEY=secret-value\n", 0, false, "unparsable answer"},
		{"failed critique", "gamma-model", "critique", "last safe line\nOPENAI_API_KEY=secret-value\n", 7, false, "session exited with code 7"},
		{"timed out critique", "gamma-model", "critique", "", 0, true, "session timed out"},
		{"unparsable cross-review", "gamma-model", "cross-review", "not a ranking\nOPENAI_API_KEY=secret-value\n", 0, false, "unparsable answer"},
		{"failed chairman", "chair-model", "chairman", "last safe line\nOPENAI_API_KEY=secret-value\n", 9, false, "session exited with code 9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := councilRunner(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
				model, prompt := councilCommand(command)
				stage := "critique"
				answer := critiqueAnswer("same claim")
				if strings.HasPrefix(prompt, "Review the other counsellors") {
					stage = "cross-review"
					answer = crossReviewAnswer(strings.ToUpper(model[:1]))
					if tc.failStage == "critique" {
						answer = crossReviewTwoAnswer(strings.ToUpper(model[:1]))
					}
				}
				if strings.HasPrefix(prompt, "Synthesize this council") {
					stage = "chairman"
					answer = "Synthesis"
				}
				if model == tc.failModel && stage == tc.failStage {
					if tc.timedOut {
						<-ctx.Done()
						return publication.CommandResult{ExitCode: -1}, ctx.Err()
					}
					return publication.CommandResult{Stdout: []byte(tc.answer), ExitCode: tc.exitCode}, nil
				}
				return publication.CommandResult{Stdout: []byte(answer)}, nil
			})
			table, opts := councilFixture(t, 2, runner)
			if tc.timedOut {
				opts.Timeout = time.Millisecond
			}
			result, err := Run(t.Context(), "Plan body", nil, table, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Failures) != 1 || result.Failures[0].Stage != tc.failStage || result.Failures[0].Reason != tc.wantReason {
				t.Fatalf("failures = %+v", result.Failures)
			}
			wantLabel := "C"
			if tc.failStage == "chairman" {
				wantLabel = "chairman"
			}
			wantCode := tc.exitCode
			if tc.timedOut {
				wantCode = -1
			}
			if failure := result.Failures[0]; failure.Label != wantLabel || failure.ExitCode == nil || *failure.ExitCode != wantCode {
				t.Fatalf("failure identity or exit code = %+v", failure)
			}
			if strings.Contains(result.Failures[0].Tail, "secret-value") {
				t.Fatalf("secret leaked in tail: %q", result.Failures[0].Tail)
			}
			if tc.answer != "" && !strings.Contains(result.Failures[0].Tail, strings.Split(tc.answer, "\n")[0]) {
				t.Fatalf("failure lost diagnostic tail: %+v", result.Failures[0])
			}
			if tc.failStage == "critique" && (len(result.Critiques) != 2 || len(result.Reviews) != 2 || result.Synthesis == "") {
				t.Fatalf("council did not continue after one failed critique: %+v", result)
			}
			if tc.failStage == "cross-review" && (len(result.Critiques) != 3 || len(result.Reviews) != 2 || result.Synthesis == "") {
				t.Fatalf("council did not continue after failed cross-review: %+v", result)
			}
			if tc.failStage == "chairman" && result.Synthesis != "" {
				t.Fatalf("failed chairman produced synthesis: %+v", result)
			}
		})
	}
}

func TestRunFailuresNeedTwoCritiques(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	reviews := 0
	runner := councilRunner(func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		model, prompt := councilCommand(command)
		if strings.HasPrefix(prompt, "Review the other counsellors") || strings.HasPrefix(prompt, "Synthesize this council") {
			mu.Lock()
			reviews++
			mu.Unlock()
		}
		if model != "alpha-model" {
			return publication.CommandResult{Stdout: []byte("unparsable")}, nil
		}
		return publication.CommandResult{Stdout: []byte(critiqueAnswer("claim"))}, nil
	})
	table, opts := councilFixture(t, 3, runner)
	result, err := Run(t.Context(), "Plan body", nil, table, opts)
	if err == nil || len(result.Critiques) != 1 || len(result.Failures) != 2 || reviews != 0 {
		t.Fatalf("insufficient critiques: result=%+v error=%v later stages=%d", result, err, reviews)
	}
}

func TestRunReadOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(string) error
	}{
		{"status", func(root string) error {
			return os.WriteFile(filepath.Join(root, "unexpected.txt"), []byte("write"), 0o644)
		}},
		{"HEAD", func(root string) error {
			command := exec.Command("git", "-c", "commit.gpgsign=false", "commit", "-qm", "moved", "--allow-empty")
			command.Dir = root
			command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Council Test", "GIT_AUTHOR_EMAIL=council@example.test", "GIT_COMMITTER_NAME=Council Test", "GIT_COMMITTER_EMAIL=council@example.test")
			return command.Run()
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			changed := false
			runner := councilRunner(func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
				if !changed {
					changed = true
					if err := tc.change(command.Directory); err != nil {
						return publication.CommandResult{ExitCode: -1}, err
					}
				}
				return publication.CommandResult{Stdout: []byte(critiqueAnswer("claim"))}, nil
			})
			table, opts := councilFixture(t, 1, runner)
			result, err := Run(t.Context(), "Plan body", nil, table, opts)
			if err == nil || len(result.Critiques) != 0 || len(result.Labels) != 0 {
				t.Fatalf("tree change accepted: result=%+v error=%v", result, err)
			}
			if _, err := os.Stat(filepath.Join(opts.Root, "council.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("council artifact exists after tree change: %v", err)
			}
		})
	}
}

func TestRunAnonymous(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var prompts []string
	runner := councilRunner(func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		model, prompt := councilCommand(command)
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()
		answer := critiqueAnswer("missing proof")
		if strings.HasPrefix(prompt, "Review the other counsellors") {
			answer = crossReviewAnswer(strings.ToUpper(model[:1]))
		}
		if strings.HasPrefix(prompt, "Synthesize this council") {
			answer = "Synthesis"
		}
		return publication.CommandResult{Stdout: []byte(answer)}, nil
	})
	table, opts := councilFixture(t, 3, runner)
	result, err := Run(t.Context(), "Plan body", nil, table, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Labels) != 4 || result.Labels["A"].Provider != "alpha" || result.Labels["chairman"].Provider != "chair" {
		t.Fatalf("missing private label map: %+v", result.Labels)
	}
	for _, prompt := range prompts {
		for _, private := range []string{"alpha", "beta", "gamma", "chair-model"} {
			if strings.Contains(prompt, private) {
				t.Fatalf("prompt exposed route %q: %s", private, prompt)
			}
		}
	}
}
