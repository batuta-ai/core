package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/batuta-ai/core/publication"
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
	run("config", "user.name", "t")
	run("config", "user.email", "t@example.com")
	run("config", "commit.gpgsign", "false")
	run("config", "gc.auto", "0")
	run("config", "gc.autoDetach", "false")
	run("config", "maintenance.auto", "false")
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

func TestEnsureExcludedSupervisionPreservesForeignState(t *testing.T) {
	ctx := context.Background()
	p, _ := initRepo(t)
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(p.Root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	const tracked = ".batuta/reviews/supervision/tracked.md"
	write(tracked, "original\n")
	if _, err := p.Commit(ctx, "chore: tracked review", tracked); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.EnsureExcluded(ctx); err != nil {
			t.Fatal(err)
		}
	}
	exclude, err := os.ReadFile(filepath.Join(p.Root, ".git", "info", "exclude"))
	if err != nil || strings.Count(string(exclude), ".batuta/reviews/supervision/\n") != 1 {
		t.Fatalf("supervision exclusion must appear once: %s, %v", exclude, err)
	}
	write(".batuta/reviews/supervision/job/snapshot/file.txt", "owned snapshot\n")
	write(".batuta/reviews/supervision/job/job.json", "owned job\n")
	write(".batuta/runs/supervision/delivery.json", "owned cursor\n")
	status, err := p.Status(ctx, p.Root, false)
	if err != nil || len(status) != 0 {
		t.Fatalf("runtime artifacts dirty tree: %v, %v", status, err)
	}
	write(tracked, "changed\n")
	write("README.md", "staged foreign edit\n")
	if _, err := p.run(ctx, p.Root, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	write("foreign.txt", "untracked\n")
	write(".batuta/reviews/foreign.md", "unrelated review\n")
	write(".batuta/reviews/supervision-other/file.txt", "unrelated directory\n")
	write(".batuta/foreign.txt", "unrelated state\n")
	status, err = p.Status(ctx, p.Root, false)
	want := []string{
		" M " + tracked, "M  README.md", "?? .batuta/foreign.txt",
		"?? .batuta/reviews/foreign.md", "?? .batuta/reviews/supervision-other/file.txt", "?? foreign.txt",
	}
	if err != nil || strings.Join(status, "\n") != strings.Join(want, "\n") {
		t.Fatalf("foreign state hidden: %v, %v; want %v", status, err, want)
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

func TestParkPreservesWorktreeIndexAndBranch(t *testing.T) {
	ctx := context.Background()
	p, base := initRepo(t)
	run := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command(p.Git, append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(root, path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(p.Root, ".batuta/profile.md", "original\n")
	run(p.Root, "add", "-A")
	run(p.Root, "commit", "-qm", "chore: profile")
	base = run(p.Root, "rev-parse", "HEAD")
	branch := "batuta/demo/task-1-e1"
	root, err := p.Add(ctx, "demo-task-1-e1", branch, base)
	if err != nil {
		t.Fatal(err)
	}
	write(root, "README.md", "staged\n")
	run(root, "add", "README.md")
	write(root, "README.md", "unstaged\n")
	write(root, "new.txt", "untracked\n")
	write(root, ".batuta/profile.md", "changed\n")
	write(root, ".batuta/brief.md", "private runtime state\n")
	indexPath := run(root, "rev-parse", "--git-path", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	statusBefore := run(root, "status", "--porcelain=v1", "--untracked-files=all")
	run(root, "config", "commit.gpgsign", "true")
	ref := "refs/batuta/parked/demo/task-1-e1"
	message := "wip(batuta): demo task_1 e1 parked"
	sha, err := p.Park(ctx, root, ref, message)
	if err != nil || sha == "" {
		t.Fatalf("Park() = %s, %v", sha, err)
	}
	if got := run(p.Root, "rev-parse", ref); got != sha {
		t.Fatalf("ref = %s, want %s", got, sha)
	}
	if got := run(root, "rev-parse", "HEAD"); got != base {
		t.Fatalf("HEAD moved to %s", got)
	}
	if got := run(root, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Fatalf("branch = %s", got)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || string(indexBefore) != string(indexAfter) {
		t.Fatalf("index changed: %v", err)
	}
	if got := run(root, "status", "--porcelain=v1", "--untracked-files=all"); got != statusBefore {
		t.Fatalf("status changed: %s", got)
	}
	if got := run(root, "show", "-s", "--format=%P%n%s%n%an%n%ae", sha); got != base+"\n"+message+"\nt\nt@example.com" {
		t.Fatalf("snapshot metadata: %s", got)
	}
	for path, want := range map[string]string{"README.md": "unstaged", "new.txt": "untracked", ".batuta/profile.md": "original"} {
		if got := run(root, "show", sha+":"+path); got != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
	if got := run(root, "ls-tree", "-r", "--name-only", sha); strings.Contains(got, ".batuta/brief.md") {
		t.Fatalf("runtime file snapshotted: %s", got)
	}
	if again, err := p.Park(ctx, root, ref, message); err != nil || again != "" {
		t.Fatalf("unchanged Park = %s, %v", again, err)
	}
	write(root, "new.txt", "revised\n")
	next, err := p.Park(ctx, root, ref, message)
	if err != nil || next == "" || next == sha {
		t.Fatalf("changed Park = %s, %v", next, err)
	}
	if err := p.Remove(ctx, root, branch); err != nil {
		t.Fatal(err)
	}
	if got := run(p.Root, "show", ref+":new.txt"); got != "revised" {
		t.Fatalf("lost parked work: %q", got)
	}
}

func TestParkProtectsCommittedWork(t *testing.T) {
	ctx := context.Background()
	p, base := initRepo(t)
	branch := "batuta/demo/task-1-e1"
	root, err := p.Add(ctx, "demo-task-1-e1", branch, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "committed.txt"), []byte("executor work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "committed.txt"}, {"commit", "-qm", "wip: executor work"}} {
		if out, err := exec.Command(p.Git, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head, err := p.Head(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/batuta/parked/demo/task-1-e1"
	sha, err := p.Park(ctx, root, ref, "wip: park")
	if err != nil || sha != head {
		t.Fatalf("Park = %q, %v; want HEAD %s", sha, err, head)
	}
	if err := p.Remove(ctx, root, branch); err != nil {
		t.Fatal(err)
	}
	if got := string(p.Show(ctx, p.Root, ref, "committed.txt")); got != "executor work\n" {
		t.Fatalf("lost committed work: %q", got)
	}
	if out, err := exec.Command(p.Git, "-C", p.Root, "rev-parse", ref).Output(); err != nil || strings.TrimSpace(string(out)) != head {
		t.Fatalf("recovery ref = %s, %v; want %s", out, err, head)
	}
}

func TestParkSkipsOnlyWhenAlreadyPreserved(t *testing.T) {
	ctx := context.Background()
	p, base := initRepo(t)
	root, err := p.Add(ctx, "demo-task-1-e1", "batuta/demo/task-1-e1", base)
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/batuta/parked/demo/task-1-e1"
	if sha, err := p.Park(ctx, root, ref, "wip: park"); err != nil || sha != "" {
		t.Fatalf("integrated Park = %s, %v", sha, err)
	}
	if refs, err := p.Parked(ctx, "demo"); err != nil || len(refs) != 0 {
		t.Fatalf("integrated refs = %v, %v", refs, err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := p.Park(ctx, root, ref, "wip: park")
	if err != nil || sha == "" {
		t.Fatalf("dirty Park = %s, %v", sha, err)
	}
	if again, err := p.Park(ctx, root, ref, "wip: park"); err != nil || again != "" {
		t.Fatalf("preserved Park = %s, %v", again, err)
	}
	refs, err := p.Parked(ctx, "demo")
	if err != nil || len(refs) != 1 || refs[0].SHA != sha {
		t.Fatalf("preserved refs = %v, %v", refs, err)
	}
	// The executor can commit the parked tree on a different ancestry.
	// Preserve that HEAD too, even though its tree matches the old snapshot.
	for _, args := range [][]string{{"add", "new.txt"}, {"commit", "-qm", "wip: executor commits parked tree"}} {
		if out, err := exec.Command(p.Git, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head, err := p.Head(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := p.Park(ctx, root, ref, "wip: park"); err != nil || saved != head {
		t.Fatalf("committed Park = %s, %v; want HEAD %s", saved, err, head)
	}
	if again, err := p.Park(ctx, root, ref, "wip: park"); err != nil || again != "" {
		t.Fatalf("preserved committed Park = %s, %v", again, err)
	}
}

func TestCleanParkedBoundedHistory(t *testing.T) {
	const historyCount = 500_001
	parkedTree := strings.Repeat("b", 40)
	runner := &boundedHistoryRunner{historyCount: historyCount, parkedTree: parkedTree, matchAt: historyCount - 1}
	p := GitProvider{Git: "/controlled/git", Runner: runner, Root: "/repo"}

	kept, err := p.CleanParked(context.Background(), "demo", "main")
	if err != nil {
		t.Fatalf("CleanParked() error = %v", err)
	}
	if len(kept) != 0 || !runner.deleted {
		t.Fatalf("CleanParked() kept = %v, deleted = %t; want deleted", kept, runner.deleted)
	}
	if runner.maxHistoryOutput >= 16<<20 {
		t.Fatalf("largest history output = %d, want less than 16 MiB", runner.maxHistoryOutput)
	}
}

func TestCleanParkedMatchesBeyondCommandLimit(t *testing.T) {
	const historyCount = 500_001
	parkedTree := strings.Repeat("b", 40)
	runner := &boundedHistoryRunner{historyCount: historyCount, parkedTree: parkedTree, matchAt: historyCount - 1}
	p := GitProvider{Git: "/controlled/git", Runner: runner, Root: "/repo"}

	if _, err := p.CleanParked(context.Background(), "demo", "main"); err != nil {
		t.Fatalf("CleanParked() error = %v", err)
	}
	if runner.historyBytes <= 16<<20 {
		t.Fatalf("history traversed = %d bytes, want more than 16 MiB", runner.historyBytes)
	}
	if runner.historyEntries != historyCount {
		t.Fatalf("history entries = %d, want %d", runner.historyEntries, historyCount)
	}
}

type boundedHistoryRunner struct {
	historyCount     int
	historyBytes     int
	historyEntries   int
	matchAt          int
	maxHistoryOutput int
	parkedTree       string
	deleted          bool
}

func (r *boundedHistoryRunner) Run(_ context.Context, command publication.Command) (publication.CommandResult, error) {
	if len(command.Args) == 0 {
		return publication.CommandResult{}, errors.New("missing git command")
	}
	switch command.Args[0] {
	case "for-each-ref":
		return publication.CommandResult{Stdout: []byte("refs/batuta/parked/demo/task-1-e1 " + strings.Repeat("c", 40) + "\n")}, nil
	case "cat-file":
		return publication.CommandResult{Stdout: []byte("tree " + r.parkedTree + "\n")}, nil
	case "log":
		limit, skip := 0, 0
		for _, arg := range command.Args {
			if value, found := strings.CutPrefix(arg, "--max-count="); found {
				limit, _ = strconv.Atoi(value)
			}
			if value, found := strings.CutPrefix(arg, "--skip="); found {
				skip, _ = strconv.Atoi(value)
			}
		}
		if limit == 0 {
			return publication.CommandResult{StdoutTruncated: true}, nil
		}
		end := min(skip+limit, r.historyCount)
		var output strings.Builder
		for index := skip; index < end; index++ {
			tree := strings.Repeat("a", 40)
			if index == r.matchAt {
				tree = r.parkedTree
			}
			fmt.Fprintln(&output, tree)
		}
		payload := []byte(output.String())
		r.historyBytes += len(payload)
		r.historyEntries += end - skip
		r.maxHistoryOutput = max(r.maxHistoryOutput, len(payload))
		return publication.CommandResult{Stdout: payload}, nil
	case "update-ref":
		r.deleted = true
		return publication.CommandResult{}, nil
	default:
		return publication.CommandResult{}, fmt.Errorf("unexpected git command %q", command.Args[0])
	}
}

func TestParkPreservesStagedIndex(t *testing.T) {
	ctx := context.Background()
	p, base := initRepo(t)
	branch := "batuta/demo/task-1-e1"
	root, err := p.Add(ctx, "demo-task-1-e1", branch, base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "staged.txt")
	if err := os.WriteFile(path, []byte("only in the index\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.run(ctx, root, "add", "staged.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ref := "refs/batuta/parked/demo/task-1-e1"
	if _, err := p.Park(ctx, root, ref, "wip: park"); err != nil {
		t.Fatal(err)
	}
	refs, err := p.Parked(ctx, "demo")
	if err != nil || len(refs) != 1 {
		t.Fatalf("staged recovery refs = %v, %v", refs, err)
	}
	if again, err := p.Park(ctx, root, ref, "wip: park"); err != nil || again != "" {
		t.Fatalf("repeated park = %s, %v", again, err)
	}
	if err := p.Remove(ctx, root, branch); err != nil {
		t.Fatal(err)
	}
	if got := string(p.Show(ctx, p.Root, refs[0].Ref, "staged.txt")); got != "only in the index\n" {
		t.Fatalf("lost staged work: %q", got)
	}
}

func TestParkWithUnmergedIndex(t *testing.T) {
	ctx := context.Background()
	p, base := initRepo(t)
	root, err := p.Add(ctx, "conflicted", "batuta/demo/conflicted", base)
	if err != nil {
		t.Fatal(err)
	}
	// NUL-delimited index records exercise paths which cannot be split on whitespace.
	path := "conflict name\twith newline\n.txt"
	var entries strings.Builder
	for stage, body := range []string{"base\n", "ours\n", "theirs\n"} {
		blob, err := p.runInput(ctx, root, []byte(body), "hash-object", "-w", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&entries, "100644 %s %d\t%s\x00", strings.TrimSpace(string(blob.Stdout)), stage+1, path)
	}
	if _, err := p.runInput(ctx, root, []byte(entries.String()), "update-index", "-z", "--index-info"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte("unresolved working content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := p.run(ctx, root, "rev-parse", "--git-path", "index")
	if err != nil {
		t.Fatal(err)
	}
	indexPath := strings.TrimSpace(string(idx.Stdout))
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/batuta/parked/demo/conflicted-e1"
	sha, err := p.Park(ctx, root, ref, "wip: park conflicts")
	if err != nil || sha == "" {
		t.Fatalf("Park = %q, %v", sha, err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("real index changed: %v", err)
	}
	if got := string(p.Show(ctx, root, ref, path)); got != "unresolved working content\n" {
		t.Fatalf("working content = %q", got)
	}
	for stage, body := range []string{"base\n", "ours\n", "theirs\n"} {
		if got := string(p.Show(ctx, root, fmt.Sprintf("%s-index-stage-%d", ref, stage+1), path)); got != body {
			t.Fatalf("stage %d = %q", stage+1, got)
		}
	}
	if again, err := p.Park(ctx, root, ref, "wip: park conflicts"); err != nil || again != "" {
		t.Fatalf("repeat Park = %q, %v", again, err)
	}
}

func TestParkWithUnmergedIndexStageMatchesHead(t *testing.T) {
	ctx := context.Background()
	p, base := initRepo(t)
	root, err := p.Add(ctx, "conflicted", "batuta/demo/conflicted", base)
	if err != nil {
		t.Fatal(err)
	}
	ours, err := p.run(ctx, root, "rev-parse", "HEAD:README.md")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := p.runInput(ctx, root, []byte("theirs\n"), "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	entries := fmt.Sprintf("0 %s\tREADME.md\x00100644 %s 2\tREADME.md\x00100644 %s 3\tREADME.md\x00", strings.Repeat("0", 40), strings.TrimSpace(string(ours.Stdout)), strings.TrimSpace(string(theirs.Stdout)))
	if _, err := p.runInput(ctx, root, []byte(entries), "update-index", "-z", "--index-info"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("conflicted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := "refs/batuta/parked/demo/conflicted-e1"
	if _, err := p.Park(ctx, root, ref, "wip: park"); err != nil {
		t.Fatal(err)
	}
	if got := string(p.Show(ctx, root, ref+"-index-stage-2", "README.md")); got != "# demo\n" {
		t.Fatalf("lost stage identity for integrated tree: %q", got)
	}
	if _, err := p.run(ctx, root, "show-ref", "--verify", ref+"-index-stage-1"); err == nil {
		t.Fatal("invented absent base stage")
	}
}

type observingGitRunner func(context.Context, publication.Command) (publication.CommandResult, error)

func (f observingGitRunner) Run(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
	return f(ctx, cmd)
}

func TestParkOrdinaryIndexUsesWriteTree(t *testing.T) {
	p, _ := initRepo(t)
	writes, scans := 0, 0
	p.Runner = observingGitRunner(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		if len(cmd.Args) > 0 && cmd.Args[0] == "ls-files" {
			scans++
			if !strings.Contains(strings.Join(cmd.Args, " "), "-u") {
				t.Error("enumerated ordinary index")
			}
		}
		if len(cmd.Args) > 0 && cmd.Args[0] == "write-tree" {
			writes++
		}
		return (publication.ExecRunner{}).Run(ctx, cmd)
	})
	if _, err := p.Park(context.Background(), p.Root, "refs/batuta/parked/demo/task-e1", "wip"); err != nil {
		t.Fatal(err)
	}
	if scans != 1 || writes != 2 {
		t.Fatalf("scans = %d, write-tree = %d", scans, writes)
	}
}

func TestParkWithUnmergedIndexStreamsBeyondOutputLimit(t *testing.T) {
	sha := strings.Repeat("a", 40)
	// One valid entry repeated exercises a listing larger than the command cap
	// without constructing a giant repository or buffering that listing in RAM.
	entry := "100644 " + sha + " 1\tconflict.txt\x00"
	count := (17<<20)/len(entry) + 1
	additions, removals := 0, 0
	p := GitProvider{Git: "/git", Root: t.TempDir()}
	p.Runner = observingGitRunner(func(_ context.Context, cmd publication.Command) (publication.CommandResult, error) {
		switch cmd.Args[0] {
		case "ls-files":
			if strings.Join(cmd.Args, " ") != "ls-files -u -z" || cmd.Observer == nil {
				t.Fatal("conflict listing must stream only unmerged entries")
			}
			chunk := strings.Repeat(entry, 1024)
			for n := 0; n < count; {
				size := min(1024, count-n)
				if _, err := cmd.Observer.Write([]byte(chunk[:size*len(entry)])); err != nil {
					return publication.CommandResult{}, err
				}
				n += size
			}
			return publication.CommandResult{StdoutTruncated: true}, nil
		case "update-index":
			if len(cmd.Stdin) > 65<<10 {
				t.Fatalf("unbounded batch: %d", len(cmd.Stdin))
			}
			n := bytes.Count(cmd.Stdin, []byte{0})
			if bytes.HasPrefix(cmd.Stdin, []byte("0 ")) {
				removals += n
			} else {
				additions += n
			}
		case "show-ref":
			return publication.CommandResult{ExitCode: 1}, errors.New("missing ref")
		}
		return publication.CommandResult{Stdout: []byte(sha + "\n")}, nil
	})
	if err := p.parkConflictStages(context.Background(), p.Root, "refs/batuta/parked/demo/task-e1", "wip", sha, nil); err != nil {
		t.Fatal(err)
	}
	if additions != count || removals != count {
		t.Fatalf("entries added = %d, removed = %d, want %d", additions, removals, count)
	}
}

// previewRepo builds main with a.txt/b.txt and a "feature" branch that edits
// b.txt, c.txt and .batuta/plan.md. Main then moves on with its own edit of
// b.txt when conflict is true, of a.txt otherwise.
func previewRepo(t *testing.T, conflict bool) GitProvider {
	t.Helper()
	provider, _ := initRepo(t)
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(provider.Root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "a\n")
	write("b.txt", "b\n")
	previewGit(t, provider, "add", "a.txt", "b.txt")
	previewGit(t, provider, "commit", "-q", "-m", "chore: base files")
	previewGit(t, provider, "checkout", "-q", "-b", "feature")
	write("b.txt", "feature\n")
	write("c.txt", "c\n")
	write(".batuta/plan.md", "plan\n")
	previewGit(t, provider, "add", "-A")
	previewGit(t, provider, "commit", "-q", "-m", "feat: feature")
	previewGit(t, provider, "checkout", "-q", "main")
	if conflict {
		write("b.txt", "main\n")
		previewGit(t, provider, "add", "b.txt")
	} else {
		write("a.txt", "main\n")
		previewGit(t, provider, "add", "a.txt")
	}
	previewGit(t, provider, "commit", "-q", "-m", "feat: main moves on")
	return provider
}

func previewGit(t *testing.T, provider GitProvider, args ...string) string {
	t.Helper()
	out, err := exec.Command(provider.Git, append([]string{"-C", provider.Root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func previewState(t *testing.T, provider GitProvider) string {
	t.Helper()
	var state strings.Builder
	for _, args := range [][]string{
		[]string{"status", "--porcelain"},
		[]string{"worktree", "list", "--porcelain"},
		[]string{"rev-parse", "refs/heads/main", "refs/heads/feature"},
		[]string{"ls-files", "--stage"},
	} {
		state.WriteString(previewGit(t, provider, args...))
	}
	return state.String()
}

func TestMergePreviewReport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		conflict      bool
		wantConflicts []string
	}{
		{name: "clean merge", wantConflicts: []string{}},
		{name: "conflicting merge", conflict: true, wantConflicts: []string{"b.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := previewRepo(t, tt.conflict)
			got, err := provider.MergePreview(context.Background(), "main", "feature")
			if err != nil {
				t.Fatalf("MergePreview() error = %v", err)
			}
			if want := []string{"b.txt", "c.txt"}; !slices.Equal(got.Changed, want) {
				t.Errorf("Changed = %v, want %v (.batuta excluded)", got.Changed, want)
			}
			if !slices.Equal(got.Conflicts, tt.wantConflicts) {
				t.Errorf("Conflicts = %v, want %v", got.Conflicts, tt.wantConflicts)
			}
			if got.Merged {
				t.Error("Merged = true for an unmerged ref")
			}
			if !gitSHA.MatchString(got.Base) {
				t.Errorf("Base = %q, want a commit SHA", got.Base)
			}
		})
	}
}

func TestMergePreviewLeavesWorkspaceUntouched(t *testing.T) {
	t.Parallel()
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict=%v", conflict), func(t *testing.T) {
			t.Parallel()
			provider := previewRepo(t, conflict)
			before := previewState(t, provider)
			if _, err := provider.MergePreview(context.Background(), "main", "feature"); err != nil {
				t.Fatalf("MergePreview() error = %v", err)
			}
			if after := previewState(t, provider); after != before {
				t.Errorf("workspace changed:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestMergePreviewAlreadyMerged(t *testing.T) {
	t.Parallel()
	provider := previewRepo(t, false)
	// Squashing feature onto its own base reproduces its tree without its ancestry.
	previewGit(t, provider, "checkout", "-q", "-b", "squashed", "feature~1")
	previewGit(t, provider, "merge", "-q", "--squash", "feature")
	previewGit(t, provider, "commit", "-q", "-m", "feat: squash feature")
	previewGit(t, provider, "checkout", "-q", "main")
	previewGit(t, provider, "branch", "-q", "ancestor", "main~1")

	tests := []struct{ name, branch, ref string }{
		{name: "tree in branch history", branch: "squashed", ref: "feature"},
		{name: "ancestor of branch", branch: "main", ref: "ancestor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := provider.MergePreview(context.Background(), tt.branch, tt.ref)
			if err != nil {
				t.Fatalf("MergePreview() error = %v", err)
			}
			if !got.Merged {
				t.Errorf("Merged = false for %s into %s, want true", tt.ref, tt.branch)
			}
			if len(got.Conflicts) != 0 {
				t.Errorf("Conflicts = %v, want none", got.Conflicts)
			}
		})
	}
}

func TestMergePreviewUnusualPaths(t *testing.T) {
	t.Parallel()
	provider, _ := initRepo(t)
	name := "caf\u00e9 \ttab.txt"
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(provider.Root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		previewGit(t, provider, "add", "-A")
	}
	write("base\n")
	previewGit(t, provider, "commit", "-q", "-m", "chore: base file")
	previewGit(t, provider, "checkout", "-q", "-b", "feature")
	write("feature\n")
	previewGit(t, provider, "commit", "-q", "-m", "feat: feature")
	previewGit(t, provider, "checkout", "-q", "main")
	write("main\n")
	previewGit(t, provider, "commit", "-q", "-m", "feat: main moves on")
	got, err := provider.MergePreview(context.Background(), "main", "feature")
	if err != nil {
		t.Fatalf("MergePreview() error = %v", err)
	}
	if want := []string{name}; !slices.Equal(got.Changed, want) || !slices.Equal(got.Conflicts, want) {
		t.Fatalf("Changed = %q, Conflicts = %q, want %q verbatim in both", got.Changed, got.Conflicts, want)
	}
}

func TestMergePreviewIgnoresTagNamedLikeBranch(t *testing.T) {
	t.Parallel()
	provider := previewRepo(t, true)
	// A tag named main on the feature commit makes the bare name ambiguous.
	previewGit(t, provider, "tag", "main", "feature")
	got, err := provider.MergePreview(context.Background(), "main", "feature")
	if err != nil {
		t.Fatalf("MergePreview() error = %v", err)
	}
	if got.Merged || !slices.Equal(got.Conflicts, []string{"b.txt"}) {
		t.Fatalf("MergePreview() = %+v, want the branch's conflict on b.txt, not the tag's", got)
	}
}

// truncatingRunner marks merge-tree output as cut at the runner's bound.
type truncatingRunner struct{ publication.CommandRunner }

func (r truncatingRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	result, err := r.CommandRunner.Run(ctx, command)
	if len(command.Args) > 0 && command.Args[0] == "merge-tree" {
		result.StdoutTruncated = true
	}
	return result, err
}

func TestMergePreviewRejectsTruncatedConflictReport(t *testing.T) {
	t.Parallel()
	provider := previewRepo(t, true)
	provider.Runner = truncatingRunner{provider.Runner}
	got, err := provider.MergePreview(context.Background(), "main", "feature")
	if err == nil || !strings.Contains(err.Error(), "exceeded bound") {
		t.Fatalf("MergePreview() = %+v, %v, want the output bound error", got, err)
	}
}

func TestMergePreviewErrors(t *testing.T) {
	t.Parallel()
	t.Run("unrelated histories", func(t *testing.T) {
		t.Parallel()
		provider := previewRepo(t, false)
		previewGit(t, provider, "checkout", "-q", "--orphan", "orphan")
		previewGit(t, provider, "commit", "-q", "--allow-empty", "-m", "chore: orphan root")
		previewGit(t, provider, "checkout", "-q", "main")
		_, err := provider.MergePreview(context.Background(), "main", "orphan")
		if err == nil || !strings.Contains(err.Error(), "unrelated histories") {
			t.Fatalf("MergePreview() error = %v, want unrelated histories", err)
		}
		if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "orphan") {
			t.Errorf("error %q must name both refs", err)
		}
	})
	t.Run("merge-tree failure", func(t *testing.T) {
		t.Parallel()
		err := mergePreviewError(129, "error: unknown option write-tree\nusage: git merge-tree")
		if err == nil || !strings.Contains(err.Error(), "git 2.38") || !strings.Contains(err.Error(), "usage: git merge-tree") {
			t.Fatalf("mergePreviewError() = %v, want git 2.38 and the stderr tail", err)
		}
	})
}
