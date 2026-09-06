package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	for _, want := range []string{"capabilities", "doctor", "inventory", "loop", "trail", "version"} {
		if !slices.Contains(got.Commands, want) {
			t.Fatalf("capabilities.commands = %v, missing %q", got.Commands, want)
		}
	}
	if slices.Contains(got.Commands, "gate") {
		t.Fatalf("capabilities.commands = %v advertises a subcommand run() does not implement", got.Commands)
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
	if top, clean := inspectGit(ctx, git, root); top != "" || clean != nil {
		t.Fatalf("inspectGit(non-repo) = %q, %v; want empty", top, clean)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command(git, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	top, clean := inspectGit(ctx, git, root)
	if top != root || clean == nil || !*clean {
		t.Fatalf("inspectGit(fresh repo) = %q, %v; want %q, clean", top, clean, root)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	top, clean = inspectGit(ctx, git, nested)
	if top != root || clean == nil || *clean {
		t.Fatalf("inspectGit(nested, dirty) = %q, %v; want toplevel %q and dirty", top, clean, root)
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
	spent, cancel := context.WithCancel(context.Background())
	cancel()
	if top, _ := inspectGit(spent, git, root); top != "" {
		t.Fatalf("inspectGit with a cancelled context = %q; a spent context must not report a repository, doctor must give git its own", top)
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
