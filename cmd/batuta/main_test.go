package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
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

func TestRunLoopDashboard(t *testing.T) {
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
		{"watch renders panel", []string{"loop", "--workspace", root, "--dashboard", "--watch", "--interval", time.Millisecond.String(), "demo"}, "\x1b[2J\x1b[Hdelivery demo"},
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
