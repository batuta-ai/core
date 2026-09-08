package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

type reviewCommandRunner func(context.Context, publication.Command) (publication.CommandResult, error)

func (f reviewCommandRunner) Run(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
	return f(ctx, cmd)
}

func sessionFixture(t *testing.T, count int) (Manifest, routing.RuntimeValue, SessionOptions) {
	t.Helper()
	root := reviewRepo(t)
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = resolved
	writeTestFile(t, root, ".batuta/profile.md", "Stack: Go\nTemplate: templates/go.md\n\n## Review rubric\nCheck resource ownership.\n")
	for i := 0; i < count; i++ {
		writeTestFile(t, root, fmt.Sprintf("file%d.go", i), "old\n")
	}
	gitTest(t, root, "add", ".")
	gitTest(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	base := gitTest(t, root, "rev-parse", "HEAD")
	for i := 0; i < count; i++ {
		writeTestFile(t, root, fmt.Sprintf("file%d.go", i), "new\n")
	}
	manifest, err := BuildManifest(root, base, nil, ManifestOptions{CohortFiles: 1})
	if err != nil {
		t.Fatal(err)
	}
	skills := t.TempDir()
	payload, err := os.ReadFile("testdata/reviewer.md")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, skills, "adapters/codex.md", string(payload))
	writeTestFile(t, skills, "templates/go.md", "Extends `templates/generic.md`\n## Conventions for briefs\nCheck error returns.\n")
	writeTestFile(t, skills, "templates/generic.md", "## Conventions for briefs\nKeep changes scoped.\n")
	return manifest, routing.RuntimeValue{Provider: "codex", Model: "review-model", Reasoning: "high"}, SessionOptions{Root: root, Skills: skills, Parallel: 2}
}

func useReviewRunner(opts *SessionOptions, runner reviewCommandRunner) {
	opts.Subprocess = &executor.Subprocess{Runner: runner, Lookup: func(string) (string, error) { return filepath.Abs("fake-reviewer") }}
}

func cleanReview() publication.CommandResult {
	return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\nNo defects found.\n")}
}

func TestReviewerRuntimeFromTable(t *testing.T) {
	t.Parallel()
	lanes := "| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| high | * | codex | high-model |\n| high | frontend | cursor-agent | ui-model |\n"
	for _, tc := range []struct {
		name, role, override string
		domain               routing.Domain
		provider, model      string
	}{
		{name: "high fallback", domain: routing.DomainBackend, provider: "codex", model: "high-model"},
		{name: "domain fallback", domain: routing.DomainFrontend, provider: "cursor-agent", model: "ui-model"},
		{name: "review role", role: "| Role | Executor | Model |\n|---|---|---|\n| review | opencode | vendor/review |\n", provider: "opencode", model: "vendor/review"},
		{name: "override", role: "| Role | Executor | Model |\n|---|---|---|\n| review | opencode | vendor/review |\n", override: "codex/provider/custom", provider: "codex", model: "provider/custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table, err := routing.ParseRoutingTable([]byte(lanes + "\n" + tc.role))
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := ReviewerRuntimeFromTable(table, tc.domain, tc.override)
			if err != nil || runtime.Provider != tc.provider || runtime.Model != tc.model || runtime.Reasoning != "high" {
				t.Fatalf("runtime=%+v error=%v", runtime, err)
			}
		})
	}
	for _, override := range []string{"codex", "codex/", "self/model", "../model", "codex/default"} {
		if _, err := ReviewerRuntimeFromTable(routing.RoutingTable{}, routing.DomainBackend, override); err == nil {
			t.Errorf("accepted %q", override)
		}
	}
	if _, err := ReviewerRuntimeFromTable(routing.RoutingTable{}, routing.DomainBackend, ""); err == nil {
		t.Fatal("accepted missing high lane")
	}
}

func TestRunCohortsParallel(t *testing.T) {
	t.Parallel()
	manifest, runtime, opts := sessionFixture(t, 4)
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	defer close(release)
	var mu sync.Mutex
	active, peak, calls := 0, 0, 0
	useReviewRunner(&opts, func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		if len(cmd.Args) != 4 || cmd.Args[0] != "--read-only" || cmd.Args[1] != "--model" || cmd.Args[2] != "review-model" || cmd.Directory != opts.Root {
			t.Errorf("wrong invocation: %+v", cmd)
		}
		prompt := cmd.Args[len(cmd.Args)-1]
		for _, want := range []string{"Check resource ownership.", "Check error returns.", "Keep changes scoped.", "<<<FINDINGS", "@@ -1 +1 @@\n-old\n+new"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q", want)
			}
		}
		if strings.Count(prompt, "diff --git") != 1 {
			t.Errorf("prompt not bounded to one cohort: %s", prompt)
		}
		mu.Lock()
		active++
		calls++
		peak = max(peak, active)
		mu.Unlock()
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return publication.CommandResult{ExitCode: -1}, ctx.Err()
		}
		mu.Lock()
		active--
		mu.Unlock()
		return cleanReview(), nil
	})
	type response struct {
		results []CohortResult
		err     error
	}
	done := make(chan response, 1)
	go func() {
		results, err := RunCohorts(t.Context(), manifest, runtime, opts)
		done <- response{results, err}
	}()
	for batch := 0; batch < 2; batch++ {
		<-entered
		<-entered
		release <- struct{}{}
		release <- struct{}{}
	}
	got := <-done
	if got.err != nil || len(got.results) != 4 || peak != 2 || calls != 4 {
		t.Fatalf("results=%+v err=%v peak=%d calls=%d", got.results, got.err, peak, calls)
	}
	for i, result := range got.results {
		if !result.Covered || result.Cohort != i || len(result.Attempts) != 1 {
			t.Fatalf("result: %+v", result)
		}
	}
}

func TestRunCohortsRejectsTreeChange(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"tracked", "untracked", "exit error"} {
		t.Run(change, func(t *testing.T) {
			manifest, runtime, opts := sessionFixture(t, 2)
			opts.Parallel = 1
			calls := 0
			useReviewRunner(&opts, func(context.Context, publication.Command) (publication.CommandResult, error) {
				calls++
				name := "file0.go"
				if change == "untracked" {
					name = "unexpected.go"
				}
				if err := os.WriteFile(filepath.Join(opts.Root, name), []byte("unauthorized\n"), 0644); err != nil {
					return publication.CommandResult{ExitCode: -1}, err
				}
				if change == "exit error" {
					return publication.CommandResult{ExitCode: -1}, errors.New("start failed")
				}
				return cleanReview(), nil
			})
			results, err := RunCohorts(t.Context(), manifest, runtime, opts)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("continued on changed tree: %d calls", calls)
			}
			for _, result := range results {
				if result.Covered || len(result.Findings) != 0 || !strings.Contains(result.Reason, "tree") {
					t.Fatalf("accepted changed tree: %+v", result)
				}
			}
		})
	}
}

func TestUncoveredCohortReported(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"nonzero", "limit", "recovered", "invalid output", "out of hunk", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			manifest, runtime, opts := sessionFixture(t, 1)
			calls := 0
			useReviewRunner(&opts, func(context.Context, publication.Command) (publication.CommandResult, error) {
				calls++
				switch scenario {
				case "nonzero":
					return publication.CommandResult{ExitCode: 2, Stderr: []byte("failed")}, nil
				case "limit":
					return publication.CommandResult{Stdout: []byte("usage limit reached")}, nil
				case "recovered":
					if calls == 1 {
						return publication.CommandResult{ExitCode: 2}, nil
					}
				case "invalid output":
					return publication.CommandResult{Stdout: []byte("looks good")}, nil
				case "out of hunk":
					return publication.CommandResult{Stdout: []byte("<<<FINDINGS\n{\"severity\":\"major\",\"kind\":\"defect\",\"file\":\"file0.go\",\"line\":2,\"premise\":\"bad\",\"path\":\"path\",\"verdict\":\"fails\"}\nFINDINGS>>>")}, nil
				case "truncated":
					result := cleanReview()
					result.StdoutTruncated = true
					return result, nil
				}
				return cleanReview(), nil
			})
			results, err := RunCohorts(t.Context(), manifest, runtime, opts)
			if err != nil || len(results) != 1 {
				t.Fatalf("results=%+v err=%v", results, err)
			}
			result := results[0]
			wantCalls := 1
			if scenario == "nonzero" || scenario == "limit" || scenario == "recovered" {
				wantCalls = 2
			}
			if calls != wantCalls || len(result.Attempts) != wantCalls || result.Covered != (scenario == "recovered") {
				t.Fatalf("calls=%d result=%+v", calls, result)
			}
			if !result.Covered && result.Reason == "" {
				t.Fatal("uncovered cohort has no reason")
			}
		})
	}
}

func TestRunCohortsFindingBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, file string
		line, end  int
		covered    bool
	}{
		{"accepted", "file0.go", 1, 1, true},
		{"other cohort", "file1.go", 1, 1, false},
		{"range beyond hunk", "file0.go", 1, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, runtime, opts := sessionFixture(t, 2)
			manifest.Cohorts = manifest.Cohorts[:1]
			useReviewRunner(&opts, func(context.Context, publication.Command) (publication.CommandResult, error) {
				return publication.CommandResult{Stdout: []byte(fmt.Sprintf("<<<FINDINGS\n{\"severity\":\"major\",\"kind\":\"defect\",\"file\":%q,\"line\":%d,\"end_line\":%d,\"premise\":\"changed invariant\",\"path\":\"call path\",\"verdict\":\"wrong output\"}\nFINDINGS>>>\nCoverage note.", tc.file, tc.line, tc.end))}, nil
			})
			results, err := RunCohorts(t.Context(), manifest, runtime, opts)
			if err != nil {
				t.Fatal(err)
			}
			result := results[0]
			if result.Covered != tc.covered || (len(result.Findings) == 1) != tc.covered {
				t.Fatalf("result=%+v", result)
			}
			if result.Attempts[0].Result.ExitCode != 0 || !strings.Contains(string(result.Attempts[0].Result.Stdout), "Coverage note.") {
				t.Fatal("session output not retained")
			}
		})
	}
}

func TestRunCohortsRejectsPreviouslyCompletedCohort(t *testing.T) {
	t.Parallel()
	manifest, runtime, opts := sessionFixture(t, 2)
	opts.Parallel = 1
	calls := 0
	useReviewRunner(&opts, func(context.Context, publication.Command) (publication.CommandResult, error) {
		calls++
		if calls == 2 {
			if err := os.WriteFile(filepath.Join(opts.Root, "file1.go"), []byte("unauthorized\n"), 0644); err != nil {
				return publication.CommandResult{ExitCode: -1}, err
			}
		}
		return cleanReview(), nil
	})
	results, err := RunCohorts(t.Context(), manifest, runtime, opts)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	for _, result := range results {
		if result.Covered || !strings.Contains(result.Reason, "tree") {
			t.Fatalf("result=%+v", result)
		}
	}
}
