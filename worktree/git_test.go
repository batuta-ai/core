package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
