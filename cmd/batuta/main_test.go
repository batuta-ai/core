package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/review"
	"github.com/batuta-ai/core/routing"
)

func TestRunRequiresASubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err == nil {
		t.Fatal("run() with no arguments should fail")
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func TestRunRejectsUnknownSubcommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"conduct"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "conduct") {
		t.Fatalf("run(conduct) error = %v, want unknown subcommand", err)
	}
}

func TestRunPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(version) error = %v", err)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("version printed nothing")
	}
}

func TestRunPrintsCapabilities(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(capabilities) error = %v", err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("capabilities is not JSON: %v\n%s", err, stdout.String())
	}
	if got.Version == "" {
		t.Fatal("capabilities.version is empty")
	}
	for _, want := range []string{"capabilities", "doctor", "gate", "inventory", "loop", "roadmap", "trail", "version"} {
		if !slices.Contains(got.Commands, want) {
			t.Fatalf("capabilities.commands = %v, missing %q", got.Commands, want)
		}
	}
}

func TestCapabilitiesListsRoadmap(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(capabilities) error = %v", err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("capabilities is not JSON: %v\n%s", err, stdout.String())
	}
	if !slices.Contains(got.Commands, "roadmap") {
		t.Fatalf("capabilities.commands = %v, missing %q", got.Commands, "roadmap")
	}
}

func TestCapabilitiesListsWatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Commands, "watch") {
		t.Fatalf("capabilities.commands = %v, missing watch", got.Commands)
	}
	if !strings.Contains(usage, "batuta watch") {
		t.Fatal("usage is missing batuta watch")
	}
}

func TestUsageListsEveryGateForm(t *testing.T) {
	for _, want := range []string{
		"batuta gate tree",
		"batuta gate tests",
		"batuta gate scope",
		"batuta gate proofs",
		"batuta gate verifier",
	} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
}

func TestUsageListsWatchFlagsAndKeys(t *testing.T) {
	for _, want := range []string{"--interval", "--once", "--lang", "--ascii", "Keys: Up/Down and PgUp/PgDn scroll", "f follows", "r opens", "R shows", "d opens", "ctrl+enter", "alt+enter", "ctrl+s", "o opens", "l changes", "? shows", "q quits", "mouse wheel"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage is missing watch option or key %q", want)
		}
	}
}

func TestCapabilitiesListsReview(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Commands, "review") {
		t.Fatalf("capabilities.commands = %v, missing review", got.Commands)
	}
}

func TestUsage(t *testing.T) {
	for _, want := range []string{"batuta review", "--base", "--worktree", "--spec", "--cohort-files", "--parallel", "--reviewer", "--full", "--out"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
}

func TestReviewWritesArtefacts(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	out := filepath.Join(root, "artifacts")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"review", "--base", base, "--out", out}, &stdout, &stderr); err != nil {
		t.Fatalf("review = %v\n%s", err, &stderr)
	}
	for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	printed, err := os.ReadFile(filepath.Join(out, "review.md"))
	if err != nil || string(printed) != stdout.String() {
		t.Fatalf("review.md does not match stdout: %v\nfile=%q\nout=%q", err, printed, stdout.String())
	}
}

func TestReviewExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		finding  *review.Finding
		wantCode int
	}{
		{name: "ship", wantCode: 0},
		{name: "fix before ship", finding: &review.Finding{Severity: review.Major, Kind: review.Defect, File: "change.go", Line: 3, Premise: "Wrong result", Path: "Call path", Verdict: "Fails", Fix: "Correct it"}, wantCode: 2},
		{name: "rework", finding: &review.Finding{Severity: review.Blocker, Kind: review.Defect, File: "change.go", Line: 3, Premise: "Build breaks", Path: "Compile", Verdict: "Fails", Fix: "Restore it"}, wantCode: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, base := reviewCommandRepo(t)
			t.Chdir(root)
			var findings []review.Finding
			if tc.finding != nil {
				findings = []review.Finding{*tc.finding}
			}
			restore := stubReviewSessions(t, findings, nil)
			defer restore()
			var stdout, stderr bytes.Buffer
			err := run([]string{"review", "--base", base, "--out", filepath.Join(root, "out")}, &stdout, &stderr)
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("review = %v", err)
				}
				return
			}
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != tc.wantCode {
				t.Fatalf("review error = %#v, want exit %d", err, tc.wantCode)
			}
		})
	}
}

func TestReviewPrintsReport(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	finding := review.Finding{Severity: review.Minor, Kind: review.Advisory, File: "change.go", Line: 3, Premise: "Name is vague", Path: "Rename", Fix: "Use a precise name"}
	restore := stubReviewSessions(t, []review.Finding{finding}, nil)
	defer restore()
	var stdout, stderr bytes.Buffer
	if err := run([]string{"review", "--base", base, "--out", filepath.Join(root, "out")}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Files:", "Cohorts:", "Coverage:", "minor · change.go:3 · Name is vague · Use a precise name", "Suppressed overlaps: 0", "Verdict: SHIP"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("report is missing %q:\n%s", want, &stdout)
		}
	}
}

func TestReviewRefusesTreeChange(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	restore := stubReviewRunner(t, func(_ context.Context, _ publication.Command) (publication.CommandResult, error) {
		if err := os.WriteFile(filepath.Join(root, "tracked.go"), []byte("package changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
	})
	defer restore()
	var stdout, stderr bytes.Buffer
	err := run([]string{"review", "--base", base, "--out", filepath.Join(root, "out")}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "tracked.go") || strings.Contains(err.Error(), "change.go") {
		t.Fatalf("review error = %v, want changed tracked path", err)
	}
}

func reviewCommandRepo(t *testing.T) (string, string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Review Test", "GIT_AUTHOR_EMAIL=review@example.test", "GIT_COMMITTER_NAME=Review Test", "GIT_COMMITTER_EMAIL=review@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-q")
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".gitignore":         ".batuta/reviews/\nout/\nartifacts/\n",
		".batuta/profile.md": "Stack: Go\nMethodology: TDD\nTest: go test ./...\nTemplate: templates/generic.md\n",
		".batuta/routing.md": "| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| high | * | codex | review-model |\n",
		"tracked.go":         "package tracked\n",
		"change.go":          "package changed\n",
	}
	for name, payload := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit("add", ".")
	runGit("-c", "commit.gpgsign=false", "commit", "-qm", "base")
	base := runGit("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "change.go"), []byte("package changed\n\nvar Changed = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, base
}

func stubReviewSessions(t *testing.T, findings []review.Finding, sweep *review.SpecSweep) func() {
	t.Helper()
	return stubReviewRunner(t, func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		if len(command.Args) == 0 || command.Args[0] != "--read-only" || command.Directory == "" {
			t.Fatalf("review invocation = %+v", command)
		}
		if sweep != nil && strings.Contains(command.Args[len(command.Args)-1], "<<<CRITERIA") {
			var lines strings.Builder
			lines.WriteString("<<<CRITERIA\n")
			for _, result := range sweep.Results {
				payload, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				lines.Write(payload)
				lines.WriteByte('\n')
			}
			lines.WriteString("CRITERIA>>>\n")
			return publication.CommandResult{Stdout: []byte(lines.String())}, nil
		}
		var output strings.Builder
		output.WriteString("<<<FINDINGS\n")
		for _, finding := range findings {
			payload, err := json.Marshal(finding)
			if err != nil {
				t.Fatal(err)
			}
			output.Write(payload)
			output.WriteByte('\n')
		}
		output.WriteString("FINDINGS>>>\n")
		return publication.CommandResult{Stdout: []byte(output.String())}, nil
	})
}

type mainReviewRunner func(context.Context, publication.Command) (publication.CommandResult, error)

func (f mainReviewRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	return f(ctx, command)
}

func stubReviewRunner(t *testing.T, runner mainReviewRunner) func() {
	t.Helper()
	skills := t.TempDir()
	for name, payload := range map[string]string{
		"adapters/codex.md":    "---\nname: codex\nrun: fake-reviewer --write {brief}\nreadonly: fake-reviewer --read-only {model_flags} {prompt}\nmodel_flags: --model {model}\navailable: fake-reviewer --version\nmodels: fake-reviewer models\nfinished: exit_code\n---\n",
		"templates/generic.md": "## Conventions for briefs\nKeep changes scoped.\n",
	} {
		path := filepath.Join(skills, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := reviewSessionOptions
	reviewSessionOptions = func(root string, parallel int) review.SessionOptions {
		return review.SessionOptions{
			Root: root, Skills: skills, Parallel: parallel,
			Subprocess: &executor.Subprocess{Runner: runner, Lookup: func(string) (string, error) { return filepath.Join(skills, "fake-reviewer"), nil }},
		}
	}
	return func() { reviewSessionOptions = previous }
}

func TestWatchHelpListsFlagsAndKeys(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"watch", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("watch --help = %v", err)
	}
	for _, want := range []string{"batuta watch", "--interval", "--once", "--lang", "--ascii", "Keys: Up/Down and PgUp/PgDn scroll", "f follows", "r opens", "R shows", "d opens", "ctrl+enter", "alt+enter", "ctrl+s", "o opens", "l changes", "? shows", "q quits", "mouse wheel"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("watch --help is missing %q\n%s", want, &stderr)
		}
	}
}

func TestInspectGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if top, state, clean := inspectGit(ctx, git, root); top != "" || state != "" || clean != nil {
		t.Fatalf("inspectGit(non-repo) = %q, %q, %v; want empty", top, state, clean)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
		{"config", "gc.autoDetach", "false"},
		{"config", "maintenance.auto", "false"},
	} {
		if out, err := exec.Command(git, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	top, state, clean := inspectGit(ctx, git, root)
	if top != root || state != "clean" || clean == nil || !*clean {
		t.Fatalf("inspectGit(fresh repo) = %q, %q, %v; want %q, clean, true", top, state, clean, root)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	top, state, clean = inspectGit(ctx, git, nested)
	if top != root || state != "dirty" || clean == nil || *clean {
		t.Fatalf("inspectGit(nested, dirty) = %q, %q, %v; want toplevel %q, dirty, false", top, state, clean, root)
	}
}

func TestInspectGitTellsManagedStateApart(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
		{"config", "gc.autoDetach", "false"},
		{"config", "maintenance.auto", "false"},
	} {
		if out, err := exec.Command(git, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "WORK.md"), []byte("managed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	managedDir := filepath.Join(root, ".batuta")
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managedDir, "state.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	top, state, clean := inspectGit(context.Background(), git, root)
	if top != root || state != "managed" || clean == nil || *clean {
		t.Fatalf("inspectGit(managed) = %q, %q, %v; want %q, managed, false", top, state, clean, root)
	}
	var output bytes.Buffer
	printDoctor(&output, doctorReport{
		GitRepository: true,
		GitToplevel:   root,
		GitState:      state,
		GitClean:      clean,
	})
	if !strings.Contains(output.String(), "managed state only (WORK.md, .batuta/) — fine for /batuta, commit before batuta loop") {
		t.Fatalf("printDoctor(managed) = %q, want managed-state guidance", output.String())
	}
}

func TestInspectGitWithSpentContext(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if out, err := exec.Command(git, "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
		{"config", "gc.autoDetach", "false"},
		{"config", "maintenance.auto", "false"},
	} {
		if out, err := exec.Command(git, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	spent, cancel := context.WithCancel(context.Background())
	cancel()
	if top, _, _ := inspectGit(spent, git, root); top != "" {
		t.Fatalf("inspectGit with a cancelled context = %q; a spent context must not report a repository, doctor must give git its own", top)
	}
}

func TestDoctorPrintsCleanAndDirtyGitStates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state string
		clean bool
		want  string
	}{
		{name: "clean", state: "clean", clean: true, want: "clean tree"},
		{name: "dirty", state: "dirty", clean: false, want: "dirty tree — commit or stash before delegating"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			printDoctor(&output, doctorReport{
				GitRepository: true,
				GitToplevel:   "/repo",
				GitState:      tc.state,
				GitClean:      &tc.clean,
			})
			if !strings.Contains(output.String(), tc.want) {
				t.Fatalf("printDoctor(%s) = %q, want %q", tc.state, output.String(), tc.want)
			}
		})
	}
}

func TestDoctorNotesSlowProbe(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	printDoctor(&output, doctorReport{ProbeDurations: []doctorProbeDuration{
		{Executor: "cursor-agent", Probe: "models", Duration: 7200 * time.Millisecond},
		{Executor: "codex", Probe: "--version", Duration: 5 * time.Second},
		{Executor: "opencode", Probe: "status", Duration: 4900 * time.Millisecond},
	}})
	if got := strings.Count(output.String(), "note:"); got != 1 {
		t.Fatalf("slow-probe notes = %d, want 1:\n%s", got, output.String())
	}
	if !strings.Contains(output.String(), "note: cursor-agent models took 7.2s (budget 5s)") {
		t.Fatalf("printDoctor() = %q, want slow-probe note", output.String())
	}
}

func TestVersionPrefersTheBuildVersion(t *testing.T) {
	previous := buildVersion
	t.Cleanup(func() { buildVersion = previous })
	buildVersion = "v9.9.9-beta.1"
	if got := version(); got != "v9.9.9-beta.1" {
		t.Fatalf("version() = %q, want the ldflags value", got)
	}
	buildVersion = ""
	if got := version(); got == "" || got == "v9.9.9-beta.1" {
		t.Fatalf("version() without ldflags = %q", got)
	}
}

func TestLoopSubcommandRefusesToRunOutsideAPreparedWorkspace(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := run([]string{"loop", "--workspace", root, "--dry-run"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("loop in a non-repository error = %v", err)
	}
	if err := run([]string{"trail", "--workspace", root}, &stdout, &stderr); err == nil {
		t.Fatal("trail without journals should fail")
	}
	if err := run([]string{"loop", "--workspace", root, "--dashboard"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "no open deliveries") {
		t.Fatalf("dashboard without journals = %v\n%s", err, stdout.String())
	}
}

func TestLoopRoadmapDryRunPrintsTheChain(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta", "plans", "done"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"roadmap.md":             "# Roadmap — Delivery\n\n- [x] 1. Finished → plans/finished.md\n- [ ] 2. Ready → plans/ready.md\n- [ ] 3. Draft → plans/draft.md\n- [ ] 4. Missing → plans/missing.md\n- [ ] 5. Unplanned\n",
		"plans/done/finished.md": "archived",
	}
	for slug, status := range map[string]string{"ready": "approved", "draft": "proposed"} {
		files["plans/"+slug+".md"] = "# Plan — " + slug + "\n\n**Goal:** Deliver.\n**Created:** 2026-09-06 · **Status:** " + status + "\n\n## Tasks\n- [ ] 1. Build — backend/low\n      Scope: out.txt\n      Accept: exists → test -f out.txt\n"
	}
	for name, payload := range files {
		if err := os.WriteFile(filepath.Join(root, ".batuta", name), []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"loop", "--workspace", root, "--dry-run", "--roadmap"}, &stdout, &stderr); err != nil {
		t.Fatalf("roadmap dry run = %v\n%s", err, &stderr)
	}
	previous := -1
	for _, want := range []string{
		"roadmap Delivery",
		"1. Finished → plans/finished.md: done",
		"2. Ready → plans/ready.md: approved",
		"3. Draft → plans/draft.md: waiting_plan (proposed)",
		"4. Missing → plans/missing.md: waiting_plan (missing)",
		"5. Unplanned → (no plan): waiting_plan (missing)",
	} {
		index := strings.Index(stdout.String(), want)
		if index <= previous {
			t.Fatalf("missing or out of order %q:\n%s", want, &stdout)
		}
		previous = index
	}
	if err := run([]string{"loop", "--workspace", root, "--dry-run", "--roadmap", "--answer", "1", "hello"}, &stdout, &stderr); err != nil {
		t.Fatalf("roadmap dry run must not record an answer: %v", err)
	}
	for name, want := range files {
		if got, err := os.ReadFile(filepath.Join(root, ".batuta", name)); err != nil || string(got) != want {
			t.Errorf("dry run changed %s: %q, %v", name, got, err)
		}
	}
	for _, name := range []string{journal.Dir, ".batuta/worktrees", "WORK.md", "out.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("dry run created %s: %v", name, err)
		}
	}
}

func TestLoopRoadmapWaitingPlanExitCode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".batuta", "roadmap.md"), []byte("# Roadmap — Delivery\n\n- [ ] 1. Missing → plans/missing.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{"loop", "--workspace", root, "--roadmap"}, &stdout, &stderr)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 4 || exit.State != loop.StateWaitingPlan {
		t.Fatalf("waiting plan exit = %v, want code 4, waiting_plan", err)
	}
	if !strings.Contains(stdout.String(), "waiting_plan") {
		t.Fatalf("waiting_plan was not printed: %s", &stdout)
	}
	if _, err := os.Stat(filepath.Join(root, journal.Dir)); !os.IsNotExist(err) {
		t.Fatalf("waiting plan opened a journal: %v", err)
	}
}

func watchDelivery(t *testing.T, root, delivery string) (*journal.Store, json.RawMessage) {
	t.Helper()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := json.Marshal(routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskPending}}})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(map[string]string{"slug": delivery, "workspace": root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(delivery, journal.Record{Kind: loop.KindOpened, Detail: detail, Graph: graph}); err != nil {
		t.Fatal(err)
	}
	return store, graph
}

type watchCompletionWriter struct {
	buffer   bytes.Buffer
	complete func() error
}

func (w *watchCompletionWriter) Write(p []byte) (int, error) {
	if w.complete != nil {
		complete := w.complete
		w.complete = nil
		if err := complete(); err != nil {
			return 0, err
		}
	}
	return w.buffer.Write(p)
}

func (w *watchCompletionWriter) String() string { return w.buffer.String() }

func TestWatchOpensTheLiveDashboard(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"latest open delivery", []string{"watch", "--interval", "1ms"}},
		{"delivery before flags", []string{"watch", "demo", "--interval", "1ms"}},
		{"delivery after flags", []string{"watch", "--interval", "1ms", "demo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", "en")
			root := t.TempDir()
			t.Chdir(root)
			store, graph := watchDelivery(t, root, "older")
			old := time.Unix(1, 0)
			if err := os.Chtimes(store.Path("older"), old, old); err != nil {
				t.Fatal(err)
			}
			watchDelivery(t, root, "demo")
			watchDelivery(t, root, "closed")
			if _, err := store.Append("closed", journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`), Graph: graph}); err != nil {
				t.Fatal(err)
			}
			stdout := &watchCompletionWriter{complete: func() error {
				_, err := store.Append("demo", journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`), Graph: graph})
				return err
			}}
			var stderr bytes.Buffer
			if err := run(tc.args, stdout, &stderr); err != nil {
				t.Fatalf("watch = %v\nstderr: %s", err, &stderr)
			}
			if got := stdout.String(); strings.Count(got, "batuta watch") != 2 || strings.Contains(got, "\x1b") || !strings.Contains(got, "batuta watch · demo") || strings.Contains(got, "batuta watch · older") || strings.Contains(got, "batuta watch · closed") {
				t.Fatalf("watch must redraw the selected delivery through completion:\n%s", got)
			}
		})
	}
}

func TestWatchOncePrintsASnapshot(t *testing.T) {
	for _, args := range [][]string{{"watch", "--once"}, {"watch", "demo", "--once"}, {"watch", "--once", "demo"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			watchDelivery(t, root, "demo")
			var stdout, stderr bytes.Buffer
			if err := run(args, &stdout, &stderr); err != nil {
				t.Fatalf("watch --once = %v\nstderr: %s", err, &stderr)
			}
			if got := stdout.String(); strings.Count(got, "batuta watch") != 1 || !strings.Contains(got, "task_1") || !strings.Contains(got, "demo") || strings.Contains(got, "\x1b[") {
				t.Fatalf("want one panel snapshot, got %q", got)
			}
		})
	}
}

func TestWatchLanguageAndASCII(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		want   string
		border string
	}{
		{"Portuguese ASCII", []string{"--lang", "pt", "--ascii"}, "Progresso", "+--"},
		{"English Unicode", []string{"--lang", "en"}, "Progress", "┌─"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", "pt")
			t.Setenv("LC_ALL", "en_US.UTF-8")
			root := t.TempDir()
			t.Chdir(root)
			store, graph := watchDelivery(t, root, "demo")
			if _, err := store.Append("demo", journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`), Graph: graph}); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if err := run(append([]string{"watch", "demo"}, tc.args...), &stdout, &stderr); err != nil {
				t.Fatalf("watch = %v\nstderr: %s", err, &stderr)
			}
			if got := stdout.String(); !strings.Contains(got, tc.want) || !strings.Contains(got, tc.border) {
				t.Fatalf("missing %q or %q in panel:\n%s", tc.want, tc.border, got)
			}
		})
	}
}

func TestWatchRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"watch", "--lang", "es"},
		{"watch", "--lang", ""},
		{"watch", "demo", "--interval", "invalid"},
		{"watch", "demo", "extra"},
		{"watch", "demo", "--unknown"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(args, &stdout, &stderr); err == nil {
			t.Errorf("run(%q) accepted invalid arguments", args)
		}
	}
}

func TestLoopDashboardStillWorks(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	graph := routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskIntegrated}}}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord := func(kind journal.Kind, detail string) {
		t.Helper()
		if _, err := store.Append("demo", journal.Record{Kind: kind, Detail: json.RawMessage(detail), Graph: graphJSON}); err != nil {
			t.Fatal(err)
		}
	}
	appendRecord(loop.KindOpened, `{"slug":"demo"}`)
	appendRecord(loop.KindTerminal, `{"state":"done"}`)

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"dashboard remains TSV", []string{"loop", "--workspace", root, "--dashboard", "demo"}, "delivery  state"},
		{"watch renders panel", []string{"loop", "--workspace", root, "--dashboard", "--watch", "--interval", time.Millisecond.String(), "demo"}, "batuta watch · demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if err := run(tc.args, &stdout, &stderr); err != nil {
				t.Fatalf("run() error = %v\nstderr: %s", err, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tc.want)
			}
		})
	}
}

func TestDryRunListsLimitFallbacks(t *testing.T) {
	var out bytes.Buffer
	loop.PrintPreview(&out, loop.Preview{Waves: []loop.PreviewWave{{Number: 1, Tasks: []loop.PreviewTask{
		{ID: "task_1", Executor: "codex", Model: "small", Fallbacks: []string{"claude/large", "self/session"}},
		{ID: "task_2", Executor: "codex", Model: "large"},
	}}}})
	for _, want := range []string{"limit fallback: claude/large", "limit fallback: none"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry run missing %q:\n%s", want, &out)
		}
	}
	var stderr bytes.Buffer
	err := run([]string{"loop", "--workspace", t.TempDir(), "--dry-run", "--limit-horizon", "45m"}, &out, &stderr)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("limit-horizon flag: %v\n%s", err, &stderr)
	}
}

func reviewGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Review Test", "GIT_AUTHOR_EMAIL=review@example.test", "GIT_COMMITTER_NAME=Review Test", "GIT_COMMITTER_EMAIL=review@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func readReviewJSON(t *testing.T, name string, value any) {
	t.Helper()
	payload, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, value); err != nil {
		t.Fatal(err)
	}
}

func onlyReviewStatePath(t *testing.T, root string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, ".batuta", "reviews", "state", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("review state paths=%v err=%v", paths, err)
	}
	return paths[0]
}

func TestReviewStateKeyDistinguishesBranches(t *testing.T) {
	first := reviewStateKey("feature-a", "feature/a", "", "")
	second := reviewStateKey("feature-a", "feature-a", "", "")
	if first == second {
		t.Fatalf("colliding branch keys: %q", first)
	}
	if !strings.HasPrefix(first, "feature-a-") || !strings.HasPrefix(second, "feature-a-") {
		t.Fatalf("keys do not retain sanitized branch name: %q, %q", first, second)
	}

	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	for index, branch := range []string{"feature/a", "feature-a"} {
		if index > 0 {
			reviewGit(t, root, "checkout", "-q", "--detach", base)
		}
		reviewGit(t, root, "checkout", "-qb", branch)
		if err := os.WriteFile(filepath.Join(root, "change.go"), []byte(fmt.Sprintf("package changed\n\nvar Changed = %d\n", index)), 0o644); err != nil {
			t.Fatal(err)
		}
		reviewGit(t, root, "commit", "-qam", branch)
		if err := run([]string{"review", "--base", base, "--out", fmt.Sprintf("out/%d", index)}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("review %s: %v", branch, err)
		}
	}
	paths, err := filepath.Glob(filepath.Join(root, ".batuta", "reviews", "state", "feature-a-*.json"))
	if err != nil || len(paths) != 2 {
		t.Fatalf("branch state paths=%v err=%v", paths, err)
	}
}

func TestReviewStateKeyDistinguishesSpecs(t *testing.T) {
	first := reviewStateKey("feature", "feature", "delivery", "/repo/plans/delivery.md")
	second := reviewStateKey("feature", "feature", "delivery", "/repo/archive/delivery.md")
	if first == second {
		t.Fatalf("colliding spec keys: %q", first)
	}
	if !strings.HasPrefix(first, "feature-delivery-") || !strings.HasPrefix(second, "feature-delivery-") {
		t.Fatalf("keys do not retain sanitized branch and spec names: %q, %q", first, second)
	}

	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "feature")
	reviewGit(t, root, "commit", "-qam", "change")
	plan := "# Plan — Spec\n**Goal:** Review\n**Status:** approved\n## Tasks\n- [ ] 1. Check — docs/low\n      Accept: delivery is reviewed\n"
	var specs []string
	for _, directory := range []string{"one", "two"} {
		name := filepath.Join(root, ".batuta", "plans", directory, "delivery.md")
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(plan), 0o644); err != nil {
			t.Fatal(err)
		}
		specs = append(specs, name)
	}
	restore := stubReviewSessions(t, nil, &review.SpecSweep{Results: []review.SpecResult{{ID: "task-1.1", Status: review.CriterionSatisfied, Path: "change.go:3"}}})
	defer restore()
	for index, spec := range specs {
		if err := run([]string{"review", "--base", base, "--spec", spec, "--out", fmt.Sprintf("out/%d", index)}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("review %s: %v", spec, err)
		}
	}
	paths, err := filepath.Glob(filepath.Join(root, ".batuta", "reviews", "state", "feature-delivery-*.json"))
	if err != nil || len(paths) != 2 {
		t.Fatalf("spec state paths=%v err=%v", paths, err)
	}
}

func TestReviewStateSurvivesDatedDirectories(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "feature/review")
	reviewGit(t, root, "commit", "-qam", "change")
	head := reviewGit(t, root, "rev-parse", "HEAD")
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	previous := reviewNow
	defer func() { reviewNow = previous }()
	for day := 8; day <= 9; day++ {
		reviewNow = func() time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
		if err := run([]string{"review", "--base", base}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	var manifest review.Manifest
	readReviewJSON(t, filepath.Join(root, ".batuta/reviews/2026-09-09-feature-review/manifest.json"), &manifest)
	if manifest.Base != head || len(manifest.Cohorts) != 0 {
		t.Fatalf("second round = %+v, want empty diff from %s", manifest, head)
	}
	var state review.ReviewState
	readReviewJSON(t, onlyReviewStatePath(t, root), &state)
	if state.Head != head {
		t.Fatalf("state = %+v", state)
	}
}

func TestReviewKeepsCheckpointWhenUncovered(t *testing.T) {
	for _, previousRound := range []bool{false, true} {
		t.Run(fmt.Sprint(previousRound), func(t *testing.T) {
			root, base := reviewCommandRepo(t)
			t.Chdir(root)
			reviewGit(t, root, "checkout", "-qb", "pending")
			if previousRound {
				restore := stubReviewSessions(t, nil, nil)
				if err := run([]string{"review", "--base", base, "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
				restore()
			}
			reviewGit(t, root, "commit", "-qam", "change")
			restore := stubReviewRunner(t, func(context.Context, publication.Command) (publication.CommandResult, error) {
				return publication.CommandResult{ExitCode: 1}, nil
			})
			defer restore()
			err := run([]string{"review", "--base", base, "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{})
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 3 {
				t.Fatalf("review = %v", err)
			}
			var state struct {
				Head    string
				Pending []struct{ Files []review.File }
			}
			readReviewJSON(t, onlyReviewStatePath(t, root), &state)
			if state.Head != base || len(state.Pending) != 1 || len(state.Pending[0].Files) != 1 || len(state.Pending[0].Files[0].Hunks) == 0 {
				t.Fatalf("state lost checkpoint or pending hunks: %+v", state)
			}
		})
	}
}

func TestReviewCarriesPendingCohorts(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "pending")
	reviewGit(t, root, "commit", "-qam", "change")
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := stubReviewRunner(t, func(context.Context, publication.Command) (publication.CommandResult, error) {
		return publication.CommandResult{ExitCode: 1}, nil
	})
	err := run([]string{"review", "--base", base, "--worktree", "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{})
	restore()
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("review = %v", err)
	}
	var prompts []string
	restore = stubReviewRunner(t, func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		prompts = append(prompts, command.Args[len(command.Args)-1])
		return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
	})
	defer restore()
	if err := run([]string{"review", "--base", base, "--out", "artifacts"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(prompts, "\n"), "new.go") || !strings.Contains(strings.Join(prompts, "\n"), "change.go") {
		t.Fatalf("pending files not reviewed: %v", prompts)
	}
	var state struct {
		Head    string
		Pending []json.RawMessage
	}
	readReviewJSON(t, onlyReviewStatePath(t, root), &state)
	if state.Head != reviewGit(t, root, "rev-parse", "HEAD") || len(state.Pending) != 0 {
		t.Fatalf("state = %+v", state)
	}
}

func TestReviewWorktreeKeepsBranchBase(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "feature/worktree")
	reviewGit(t, root, "commit", "-qam", "committed change")
	if err := os.WriteFile(filepath.Join(root, "untracked.go"), []byte("package untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	if err := run([]string{"review", "--worktree", "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var manifest review.Manifest
	readReviewJSON(t, filepath.Join(root, "out", "manifest.json"), &manifest)
	if manifest.Base != base {
		t.Fatalf("base=%q, want branch point %q", manifest.Base, base)
	}
	paths := make(map[string]bool)
	for _, file := range manifest.Files {
		paths[file.Path] = true
	}
	if !paths["change.go"] || !paths["untracked.go"] {
		t.Fatalf("manifest paths=%v, want committed and untracked changes", paths)
	}
}

func TestReviewRefusesOutOverTrackedFiles(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
			t.Run(fmt.Sprintf("%s/symlink=%t", name, symlink), func(t *testing.T) {
				root, base := reviewCommandRepo(t)
				t.Chdir(root)
				if err := os.Mkdir(filepath.Join(root, "reports"), 0o755); err != nil {
					t.Fatal(err)
				}
				tracked := filepath.Join(root, "reports", name)
				if err := os.WriteFile(tracked, []byte("preserve source\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				reviewGit(t, root, "add", "reports")
				reviewGit(t, root, "commit", "-qm", "tracked report")
				out := "reports"
				if symlink {
					out = filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(filepath.Join(root, "reports"), out); err != nil {
						t.Fatal(err)
					}
				}
				restore := stubReviewSessions(t, nil, nil)
				defer restore()
				err := run([]string{"review", "--base", base, "--out", out}, &bytes.Buffer{}, &bytes.Buffer{})
				if err == nil || !strings.Contains(err.Error(), "tracked") {
					t.Fatalf("review = %v, want refusal", err)
				}
				payload, readErr := os.ReadFile(tracked)
				if readErr != nil || string(payload) != "preserve source\n" {
					t.Fatalf("tracked source overwritten: %q, %v", payload, readErr)
				}
				entries, err := os.ReadDir(filepath.Join(root, "reports"))
				if err != nil || len(entries) != 1 {
					t.Fatalf("wrote artifacts before refusing: %v, %v", entries, err)
				}
			})
		}
	}
}

func TestReviewChecksTreeAfterArtefacts(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ -f '" + root + "/out/review.md' ]; then\n  echo 'package tampered' > '" + root + "/tracked.go'\nfi\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	err = run([]string{"review", "--base", base, "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "source tree changed") {
		t.Fatalf("review = %v, want post-publication tree check", err)
	}
	states, err := filepath.Glob(filepath.Join(root, ".batuta/reviews/state/*.json"))
	if err != nil || len(states) != 0 {
		t.Fatalf("checkpoint published after mutation: %v, %v", states, err)
	}
}

func TestReviewSpecPath(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "feature/spec")
	for _, location := range []string{"plans", "plans/done"} {
		dir := filepath.Join(root, ".batuta", location)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		plan := "# Plan — Spec\n**Goal:** Review\n**Status:** approved\n## Tasks\n- [ ] 1. Check — docs/low\n      Accept: criteria from " + location + "\n"
		if err := os.WriteFile(filepath.Join(dir, "delivery.md"), []byte(plan), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sawSpec := false
	restore := stubReviewRunner(t, func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		prompt := command.Args[len(command.Args)-1]
		if strings.Contains(prompt, "<<<CRITERIA") {
			sawSpec = true
			if !strings.Contains(prompt, "criteria from plans/done") {
				t.Errorf("wrong spec: %s", prompt)
			}
			return publication.CommandResult{Stdout: []byte("<<<CRITERIA\n{\"id\":\"task-1.1\",\"status\":\"satisfied\",\"path\":\"change.go:3\"}\nCRITERIA>>>\n")}, nil
		}
		return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
	})
	defer restore()
	if err := run([]string{"review", "--base", base, "--spec", ".batuta/plans/done/delivery.md", "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !sawSpec {
		t.Fatal("no spec sweep")
	}
	onlyReviewStatePath(t, root)
}

func TestReviewAllowsUntrackedArtifactDirectory(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	if err := run([]string{"review", "--base", base, "--out", "reports/new"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "reports/new/review.md")); err != nil {
		t.Fatal(err)
	}
}

func TestResumeDryRunLeavesNoLock(t *testing.T) {
	root, _ := reviewCommandRepo(t)
	skills := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skills, "adapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".batuta", "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	planText := "# Plan — Preview\n\n**Goal:** Preview a resumed delivery.\n**Status:** approved\n\n## Tasks\n- [x] 1. Build — backend/high\n      Scope: change.go\n      Accept: file exists → test -f change.go\n"
	if err := os.WriteFile(filepath.Join(root, routing.PlanPath("preview")), []byte(planText), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, root, "add", ".")
	reviewGit(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "prepare preview")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".batuta/journal/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loader, err := routing.NewPlanLoader(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := loader.LoadPlan("preview")
	if err != nil {
		t.Fatal(err)
	}
	head := reviewGit(t, root, "rev-parse", "HEAD")
	detail, err := json.Marshal(map[string]any{
		"slug": "preview", "plan_digest": plan.Set.Digest,
		"branch": reviewGit(t, root, "branch", "--show-current"), "head": head, "parallel": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := json.Marshal(routing.DeliveryGraph{
		Waves: []routing.DeliveryWave{}, Integrations: []routing.IntegrationOperation{}, Pauses: []routing.HumanPause{},
		Tasks: []routing.GraphTask{{
			TaskID: "task_1", Domain: routing.DomainBackend, Complexity: routing.ComplexityHigh,
			Dependencies: []string{}, Attempts: []routing.GraphTaskAttempt{},
			State: routing.GraphTaskIntegrated, IntegratedCommitSHA: head,
		}}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("preview", journal.Record{Kind: loop.KindOpened, Detail: detail, Graph: graph}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var stdout, stderr bytes.Buffer
		if err := run([]string{"loop", "--workspace", root, "--skills", skills, "--resume", "preview", "--dry-run"}, &stdout, &stderr); err != nil {
			t.Fatalf("resume preview: %v\n%s", err, &stderr)
		}
		if !strings.Contains(stdout.String(), "delivery  preview") {
			t.Fatalf("missing preview: %s", &stdout)
		}
		if _, err := os.Stat(filepath.Join(root, journal.Dir, "preview.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preview retained ownership: %v", err)
		}
	}
}

func TestReviewInterruptedReportsCancellation(t *testing.T) {
	root, _ := reviewCommandRepo(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	git := publication.GitClient{Executable: gitPath, Runner: publication.ExecRunner{}}
	baseline, err := git.WorktreeState(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = reviewSessionError(ctx, git, root, baseline, nil, context.Canceled)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "source tree changed") {
		t.Fatalf("interrupted review = %v", err)
	}
}

func TestReviewStateErrorIsNotMutation(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	stateErr := errors.New("git unavailable")
	sessionErr := errors.New("review executor failed")
	git := publication.GitClient{Executable: gitPath, Runner: mainReviewRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
		return publication.CommandResult{}, stateErr
	})}
	for _, failure := range []error{nil, sessionErr} {
		err := reviewSessionError(context.Background(), git, t.TempDir(), publication.WorktreeState{}, nil, failure)
		if !errors.Is(err, stateErr) || (failure != nil && !errors.Is(err, failure)) || strings.Contains(err.Error(), "source tree changed") {
			t.Fatalf("state failure = %v", err)
		}
	}
}

func writeReviewProofPlan(t *testing.T, root, accept string) string {
	t.Helper()
	path := filepath.Join(root, "spec.md")
	payload := "# Plan — Spec\n**Goal:** Review\n**Status:** approved\n## Tasks\n- [ ] 1. Check — docs/low\n      Accept: " + accept + "\n"
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReviewRefusesProofThatChangesTree(t *testing.T) {
	for _, tc := range []struct{ name, proof, path string }{
		{"tracked", "printf changed > tracked.go", "tracked.go"},
		{"already dirty", "printf changed > change.go", "change.go"},
		{"untracked", "printf changed > new.go", "worktree state changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, base := reviewCommandRepo(t)
			t.Chdir(root)
			spec := writeReviewProofPlan(t, root, "safe → true; mutates → "+tc.proof)
			restore := stubReviewRunner(t, func(context.Context, publication.Command) (publication.CommandResult, error) {
				t.Fatal("reviewer started after proof changed tree")
				return publication.CommandResult{}, nil
			})
			defer restore()
			err := run([]string{"review", "--base", base, "--spec", spec, "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{})
			want := "review: proof of task-1.2 changed the source tree: " + tc.path
			if err == nil || err.Error() != want {
				t.Fatalf("error=%v, want %q", err, want)
			}
		})
	}
}

func TestReviewWithOnlyProofRulesRunsNoSweep(t *testing.T) {
	for _, tc := range []struct {
		name, accept string
		statuses     []review.CriterionStatus
	}{
		{"passing", "first → test -f tracked.go; second → true", []review.CriterionStatus{review.CriterionSatisfied, review.CriterionSatisfied}},
		{"failing", "first → false; second → true", []review.CriterionStatus{review.CriterionViolated, review.CriterionSatisfied}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, base := reviewCommandRepo(t)
			t.Chdir(root)
			spec := writeReviewProofPlan(t, root, tc.accept)
			calls := 0
			restore := stubReviewRunner(t, func(_ context.Context, cmd publication.Command) (publication.CommandResult, error) {
				calls++
				if strings.Contains(cmd.Args[len(cmd.Args)-1], "<<<CRITERIA") {
					t.Fatal("all-proof plan started a spec sweep")
				}
				return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
			})
			defer restore()
			var stdout bytes.Buffer
			err := run([]string{"review", "--base", base, "--spec", spec, "--out", "out"}, &stdout, &bytes.Buffer{})
			if tc.name == "failing" {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 3 {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("reviewer calls=%d, want one cohort only", calls)
			}
			payload, readErr := os.ReadFile(filepath.Join(root, "out", "review.md"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(payload) != stdout.String() {
				t.Fatal("saved report differs from stdout")
			}
			previous := -1
			for i, status := range tc.statuses {
				want := fmt.Sprintf("| task-1.%d | %s | proof: ", i+1, status)
				position := strings.Index(stdout.String(), want)
				if position <= previous {
					t.Fatalf("missing or out-of-order %q in %s", want, stdout.String())
				}
				previous = position
			}
		})
	}
}

func TestReviewWithMixedSpecRules(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	spec := writeReviewProofPlan(t, root, "first → true; manual criterion; last → false")
	var sessions []string
	restore := stubReviewRunner(t, func(_ context.Context, cmd publication.Command) (publication.CommandResult, error) {
		prompt := cmd.Args[len(cmd.Args)-1]
		if strings.Contains(prompt, "<<<CRITERIA") {
			sessions = append(sessions, "sweep")
			if strings.Contains(prompt, "[task-1.1]") || strings.Contains(prompt, "[task-1.3]") || !strings.Contains(prompt, "[task-1.2]") || strings.Contains(prompt, "Proof:") {
				t.Fatalf("sweep did not receive only arrow-less criterion: %s", prompt)
			}
			return publication.CommandResult{Stdout: []byte("<<<CRITERIA\n" + `{"id":"task-1.2","status":"satisfied","path":"change.go:3"}` + "\nCRITERIA>>>\n")}, nil
		}
		sessions = append(sessions, "cohort")
		return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
	})
	defer restore()
	var stdout bytes.Buffer
	err := run([]string{"review", "--base", base, "--spec", spec, "--out", "out"}, &stdout, &bytes.Buffer{})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("error=%v", err)
	}
	if !slices.Equal(sessions, []string{"cohort", "sweep"}) {
		t.Fatalf("sessions=%v", sessions)
	}
	previous := -1
	for _, want := range []string{
		"| task-1.1 | satisfied | proof: true exited 0 |",
		"| task-1.2 | satisfied | change.go:3 |",
		"| task-1.3 | violated | proof: false exited 1 |",
	} {
		position := strings.Index(stdout.String(), want)
		if position <= previous {
			t.Fatalf("missing or out-of-order %q in %s", want, stdout.String())
		}
		previous = position
	}
}
