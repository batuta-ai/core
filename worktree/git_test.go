package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a repository with one commit and returns a provider.
func initRepo(t *testing.T) (GitProvider, string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(git, append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	run("config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# demo\n"), 0o644)
	run("add", "README.md")
	run("commit", "-q", "-m", "chore: init")
	provider, err := New(context.Background(), root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider, run("rev-parse", "HEAD")
}

func TestWorktreeLifecycle(t *testing.T) {
	ctx := context.Background()
	p, base := initRepo(t)
	if branch, err := p.Branch(ctx); err != nil || branch != "main" {
		t.Fatalf("Branch() = %q, %v", branch, err)
	}
	if err := p.EnsureExcluded(ctx); err != nil {
		t.Fatalf("EnsureExcluded() error = %v", err)
	}
	if err := p.EnsureExcluded(ctx); err != nil {
		t.Fatalf("EnsureExcluded() second call error = %v", err)
	}
	exclude, _ := os.ReadFile(filepath.Join(p.Root, ".git", "info", "exclude"))
	if strings.Count(string(exclude), ".batuta/worktrees/") != 1 || !strings.Contains(string(exclude), ".batuta/journal/") {
		t.Fatalf("exclude = %q", exclude)
	}

	path, err := p.Add(ctx, "demo-task-1-e1", "batuta/demo/task-1-e1", base)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if !strings.HasPrefix(path, filepath.Join(p.Root, ".batuta", "worktrees")) {
		t.Fatalf("worktree path = %q", path)
	}
	// The executor writes and commits freely inside the worktree.
	os.WriteFile(filepath.Join(path, "src.txt"), []byte("one\n"), 0o644)
	exec.Command(p.Git, "-C", path, "add", "-A").Run()
	exec.Command(p.Git, "-C", path, "commit", "-q", "-m", "wip 1").Run()
	os.WriteFile(filepath.Join(path, "src.txt"), []byte("two\n"), 0o644)
	os.WriteFile(filepath.Join(path, "WORK.md"), []byte("# WORK\n"), 0o644)

	changed, err := p.ChangedPaths(ctx, path, base)
	if err != nil || strings.Join(changed, ",") != "WORK.md,src.txt" {
		t.Fatalf("ChangedPaths() = %v, %v", changed, err)
	}
	status, err := p.Status(ctx, path, true)
	if err != nil || len(status) != 1 || !strings.Contains(status[0], "src.txt") {
		t.Fatalf("Status(ignoreManaged) = %v, %v", status, err)
	}

	sha, err := p.Squash(ctx, path, base, "feat: add src\n\nBody line.\n")
	if err != nil {
		t.Fatalf("Squash() error = %v", err)
	}
	out, _ := exec.Command(p.Git, "-C", path, "rev-list", "--count", base+"..HEAD").Output()
	if strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("commits ahead of base = %s, want 1", out)
	}
	if head, _ := p.Head(ctx, path); head != sha {
		t.Fatalf("Head() = %s, want %s", head, sha)
	}
	if content := p.Show(ctx, path, sha, "src.txt"); string(content) != "two\n" {
		t.Fatalf("Show() = %q", content)
	}
	if ancestor, err := p.IsAncestor(ctx, base, sha); err != nil || !ancestor {
		t.Fatalf("IsAncestor(base, sha) = %v, %v", ancestor, err)
	}
	if ancestor, err := p.IsAncestor(ctx, sha, base); err != nil || ancestor {
		t.Fatalf("IsAncestor(sha, base) = %v, %v", ancestor, err)
	}

	// Re-adding the same name replaces the leftover from an aborted run.
	if _, err := p.Add(ctx, "demo-task-1-e1", "batuta/demo/task-1-e1", base); err != nil {
		t.Fatalf("Add() again error = %v", err)
	}
	if err := p.Remove(ctx, path, "batuta/demo/task-1-e1"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after Remove: %v", err)
	}
	if err := exec.Command(p.Git, "-C", p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/batuta/demo/task-1-e1").Run(); err == nil {
		t.Fatal("branch still exists after Remove")
	}
	if err := p.Remove(ctx, path, "batuta/demo/task-1-e1"); err != nil {
		t.Fatalf("Remove() of nothing error = %v", err)
	}
}

func TestCommitRecordsBookkeepingAtTheRoot(t *testing.T) {
	ctx := context.Background()
	p, base := initRepo(t)
	os.WriteFile(filepath.Join(p.Root, "WORK.md"), []byte("# WORK\n"), 0o644)
	sha, err := p.Commit(ctx, "chore(batuta): record wave 1", "WORK.md")
	if err != nil || sha == base {
		t.Fatalf("Commit() = %s, %v", sha, err)
	}
	// Nothing staged → the head is returned unchanged, no empty commit.
	again, err := p.Commit(ctx, "chore(batuta): record wave 1", "WORK.md")
	if err != nil || again != sha {
		t.Fatalf("Commit() no-op = %s, %v (want %s)", again, err, sha)
	}
	log, err := p.Log(ctx, base, sha)
	if err != nil || len(log) != 1 || !strings.HasSuffix(log[0], "chore(batuta): record wave 1") {
		t.Fatalf("Log() = %v, %v", log, err)
	}
	if _, err := New(ctx, t.TempDir()); err == nil {
		t.Fatal("New(non-repo) should fail")
	}
}
