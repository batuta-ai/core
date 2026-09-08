// Package worktree gives the loop its git operations on a file host: the
// user's checkout as the integration root, one worktree per task attempt
// under `.batuta/worktrees/`, and the small set of read and write
// operations the runner needs around them. Every command is argv through
// the publication runner — no shell.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/batuta-ai/core/publication"
)

// Dir is where task worktrees live, relative to the repository root.
const Dir = ".batuta/worktrees"

// Excluded lists the runtime directories the loop keeps out of the user's
// index through `.git/info/exclude` (never `.gitignore`, which is theirs).
var Excluded = []string{".batuta/worktrees/", ".batuta/journal/", ".batuta/runs/", ".batuta/asks/", ".batuta/scout/", ".batuta/handoff.md"}

// ManagedPaths are the tracked files the conductor owns; a dirty one never
// fails a clean-tree check (references/state.md).
var ManagedPaths = []string{"WORK.md", ".batuta/"}

var (
	ErrNotRepository = errors.New("worktree: not a git repository")
	ErrDirty         = errors.New("worktree: working tree is dirty")

	gitSHA     = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)
	branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// GitProvider runs git against one repository root.
type GitProvider struct {
	Git    string
	Runner publication.CommandRunner
	Root   string
}

// New resolves git on PATH and the canonical repository toplevel of root.
func New(ctx context.Context, root string) (GitProvider, error) {
	git, err := publication.ExecutableResolver{}.Resolve("git")
	if err != nil {
		return GitProvider{}, errors.New("worktree: git is not on PATH")
	}
	provider := GitProvider{Git: git, Runner: publication.ExecRunner{}, Root: root}
	top, err := provider.run(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitProvider{}, ErrNotRepository
	}
	provider.Root = strings.TrimSpace(string(top.Stdout))
	if resolved, err := filepath.EvalSymlinks(provider.Root); err == nil {
		provider.Root = resolved
	}
	return provider, nil
}

// Head returns the commit a directory's HEAD points at.
func (p GitProvider) Head(ctx context.Context, dir string) (string, error) {
	out, err := p.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out.Stdout))
	if !gitSHA.MatchString(sha) {
		return "", errors.New("worktree: HEAD is not a commit")
	}
	return sha, nil
}

// Branch returns the branch checked out at the root, or an error when HEAD
// is detached: the loop integrates onto a branch, whatever its name.
func (p GitProvider) Branch(ctx context.Context) (string, error) {
	out, err := p.run(ctx, p.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", errors.New("worktree: HEAD is detached; check out the branch to integrate onto")
	}
	return strings.TrimSpace(string(out.Stdout)), nil
}

// Status lists the porcelain entries of a directory. With ignoreManaged the
// conductor's own files (WORK.md, .batuta/) are dropped from the list.
func (p GitProvider) Status(ctx context.Context, dir string, ignoreManaged bool) ([]string, error) {
	out, err := p.run(ctx, dir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	entries := make([]string, 0)
	for _, line := range strings.Split(string(out.Stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if ignoreManaged && len(line) > 3 && IsManaged(strings.TrimSpace(line[3:])) {
			continue
		}
		entries = append(entries, line)
	}
	return entries, nil
}

// IsManaged says whether a path is conductor-owned state.
func IsManaged(path string) bool {
	path = filepath.ToSlash(path)
	for _, managed := range ManagedPaths {
		if path == managed || strings.HasPrefix(path, managed) {
			return true
		}
	}
	return false
}

// EnsureExcluded appends the runtime patterns to `.git/info/exclude` once.
func (p GitProvider) EnsureExcluded(ctx context.Context) error {
	out, err := p.run(ctx, p.Root, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	common := strings.TrimSpace(string(out.Stdout))
	if !filepath.IsAbs(common) {
		common = filepath.Join(p.Root, common)
	}
	infoDir := filepath.Join(common, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return fmt.Errorf("worktree: create info dir: %w", err)
	}
	path := filepath.Join(infoDir, "exclude")
	existing, _ := os.ReadFile(path)
	present := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, pattern := range Excluded {
		if !present[pattern] {
			missing = append(missing, pattern)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("worktree: open exclude: %w", err)
	}
	defer handle.Close()
	prefix := ""
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		prefix = "\n"
	}
	_, err = handle.WriteString(prefix + "# batuta loop runtime state\n" + strings.Join(missing, "\n") + "\n")
	return err
}

// Add creates `.batuta/worktrees/<name>` on a new branch at base. A leftover
// worktree or branch of the same name from an aborted run is removed first.
func (p GitProvider) Add(ctx context.Context, name, branch, baseSHA string) (string, error) {
	if !safeName(name) || !branchName.MatchString(branch) || !gitSHA.MatchString(baseSHA) {
		return "", errors.New("worktree: invalid worktree name, branch or base")
	}
	path := filepath.Join(p.Root, filepath.FromSlash(Dir), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("worktree: create %s: %w", Dir, err)
	}
	_ = p.Remove(ctx, path, branch)
	if _, err := p.run(ctx, p.Root, "worktree", "add", "-q", "-b", branch, path, baseSHA); err != nil {
		return "", fmt.Errorf("worktree: add %s: %w", name, err)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return path, nil
}

// Remove drops a worktree and its branch. Missing pieces are not errors: an
// interrupted run may have left either half behind.
func (p GitProvider) Remove(ctx context.Context, path, branch string) error {
	var first error
	if path != "" {
		if _, err := os.Lstat(path); err == nil {
			if _, err := p.run(ctx, p.Root, "worktree", "remove", "--force", path); err != nil {
				first = err
			}
		}
		_, _ = p.run(ctx, p.Root, "worktree", "prune")
	}
	if branch != "" && branchName.MatchString(branch) {
		if _, err := p.run(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			if _, err := p.run(ctx, p.Root, "branch", "-D", "-q", branch); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// Park saves the working tree under ref and distinct staged contents under
// ref+"-index" without changing HEAD, the real index, or any files. A tree
// already protected by a recovery ref or integration history needs no new snapshot.
func (p GitProvider) Park(ctx context.Context, root, ref, message string) (string, error) {
	if !strings.HasPrefix(ref, "refs/batuta/parked/") || strings.TrimSpace(message) == "" {
		return "", errors.New("worktree: invalid park request")
	}
	if _, err := p.run(ctx, p.Root, "check-ref-format", ref); err != nil {
		return "", err
	}
	head, err := p.Head(ctx, root)
	if err != nil {
		return "", err
	}
	scratch, err := os.MkdirTemp("", "batuta-park-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}
	if _, err := p.runEnvironment(ctx, root, nil, env, "read-tree", head); err != nil {
		return "", err
	}
	if _, err := p.runEnvironment(ctx, root, nil, env, "add", "-A", "--", ".", ":(top,exclude).batuta"); err != nil {
		return "", err
	}
	treeResult, err := p.runEnvironment(ctx, root, nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(treeResult.Stdout))
	indexPath, err := p.run(ctx, root, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(indexPath.Stdout))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	index, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil {
		// write-tree updates the index cache; use a copy to keep the real index intact.
		stagedPath := filepath.Join(scratch, "staged-index")
		if err := os.WriteFile(stagedPath, index, 0o600); err != nil {
			return "", err
		}
		staged, err := p.runEnvironment(ctx, root, nil, []string{"GIT_INDEX_FILE=" + stagedPath}, "write-tree")
		if err != nil {
			return "", err
		}
		if stagedTree := strings.TrimSpace(string(staged.Stdout)); stagedTree != tree {
			if _, err := p.parkTree(ctx, root, ref+"-index", message+" (index)", head, stagedTree); err != nil {
				return "", err
			}
		}
	}
	return p.parkTree(ctx, root, ref, message, head, tree)
}

func (p GitProvider) parkTree(ctx context.Context, root, ref, message, head, tree string) (string, error) {
	old, err := p.run(ctx, p.Root, "show-ref", "--verify", "--hash", "--", ref)
	if err == nil {
		previous := strings.TrimSpace(string(old.Stdout))
		previousTree, err := p.run(ctx, p.Root, "rev-parse", previous+"^{tree}")
		if err != nil {
			return "", err
		}
		if tree == strings.TrimSpace(string(previousTree.Stdout)) {
			preserved, err := p.IsAncestor(ctx, head, previous)
			if err != nil {
				return "", err
			}
			if preserved {
				return "", nil
			}
		}
	} else if old.ExitCode != 1 && old.ExitCode != 128 {
		return "", err
	}
	headTree, err := p.run(ctx, root, "rev-parse", head+"^{tree}")
	if err != nil {
		return "", err
	}
	if tree == strings.TrimSpace(string(headTree.Stdout)) {
		integrationHead, err := p.Head(ctx, p.Root)
		if err != nil {
			return "", err
		}
		integrated, err := p.IsAncestor(ctx, head, integrationHead)
		if err != nil {
			return "", err
		}
		if integrated {
			return "", nil
		}
		if _, err := p.run(ctx, p.Root, "update-ref", ref, head); err != nil {
			return "", err
		}
		return head, nil
	}
	var env []string
	for _, field := range []struct{ key, author, committer string }{
		{"user.name", "GIT_AUTHOR_NAME=", "GIT_COMMITTER_NAME="},
		{"user.email", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_EMAIL="},
	} {
		result, err := p.run(ctx, root, "config", "--get", field.key)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(string(result.Stdout))
		env = append(env, field.author+value, field.committer+value)
	}
	env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false")
	commit, err := p.runEnvironment(ctx, root, nil, env, "commit-tree", tree, "-p", head, "-m", message)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(commit.Stdout))
	if !gitSHA.MatchString(sha) {
		return "", errors.New("worktree: invalid parked commit")
	}
	if _, err := p.run(ctx, p.Root, "update-ref", ref, sha); err != nil {
		return "", err
	}
	return sha, nil
}

type ParkedRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// Parked lists the delivery's refs, including snapshots from removed worktrees.
func (p GitProvider) Parked(ctx context.Context, slug string) ([]ParkedRef, error) {
	if !safeName(slug) {
		return nil, errors.New("worktree: invalid parked slug")
	}
	result, err := p.run(ctx, p.Root, "for-each-ref", "--format=%(refname) %(objectname)", "refs/batuta/parked/"+slug+"/")
	if err != nil {
		return nil, err
	}
	var refs []ParkedRef
	for _, line := range nonempty(string(result.Stdout)) {
		parts := strings.Fields(line)
		if len(parts) != 2 || !gitSHA.MatchString(parts[1]) {
			return nil, errors.New("worktree: invalid parked ref")
		}
		refs = append(refs, ParkedRef{Ref: parts[0], SHA: parts[1]})
	}
	return refs, nil
}

// CleanParked deletes only snapshots whose complete tree is already in the
// integration branch's history. Squashed commits need not share ancestry.
func (p GitProvider) CleanParked(ctx context.Context, slug, branch string) ([]ParkedRef, error) {
	refs, err := p.Parked(ctx, slug)
	if err != nil || len(refs) == 0 {
		return refs, err
	}
	refTrees := make([]string, len(refs))
	wantedTrees := make(map[string]bool, len(refs))
	for index, ref := range refs {
		commit, err := p.run(ctx, p.Root, "cat-file", "-p", ref.SHA)
		if err != nil {
			return nil, err
		}
		tree := strings.TrimPrefix(firstLine(string(commit.Stdout)), "tree ")
		if !gitSHA.MatchString(tree) {
			return nil, errors.New("worktree: invalid parked tree")
		}
		refTrees[index] = tree
		wantedTrees[tree] = true
	}

	const historyPageSize = 50_000
	matchedTrees := make(map[string]bool, len(wantedTrees))
	for skip := 0; len(matchedTrees) < len(wantedTrees); skip += historyPageSize {
		result, err := p.run(ctx, p.Root, "log", "--format=%T", "--max-count="+strconv.Itoa(historyPageSize), "--skip="+strconv.Itoa(skip), "refs/heads/"+branch, "--")
		if err != nil {
			return nil, err
		}
		trees := nonempty(string(result.Stdout))
		for _, tree := range trees {
			if wantedTrees[tree] {
				matchedTrees[tree] = true
			}
		}
		if len(trees) < historyPageSize {
			break
		}
	}

	var kept []ParkedRef
	for index, ref := range refs {
		if !matchedTrees[refTrees[index]] {
			kept = append(kept, ref)
			continue
		}
		if _, err := p.run(ctx, p.Root, "update-ref", "-d", ref.Ref, ref.SHA); err != nil {
			return nil, err
		}
	}
	return kept, nil
}

// ChangedPaths lists what a worktree changed against its base: committed
// paths (base...HEAD) plus anything still uncommitted or untracked.
func (p GitProvider) ChangedPaths(ctx context.Context, dir, baseSHA string) ([]string, error) {
	if !gitSHA.MatchString(baseSHA) {
		return nil, errors.New("worktree: invalid base")
	}
	committed, err := p.run(ctx, dir, "diff", "--name-only", baseSHA+"...HEAD")
	if err != nil {
		return nil, err
	}
	paths := nonempty(string(committed.Stdout))
	entries, err := p.Status(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if len(entry) > 3 {
			path := strings.TrimSpace(entry[3:])
			if from, to, renamed := strings.Cut(path, " -> "); renamed {
				paths = append(paths, from, to)
				continue
			}
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

// Squash turns everything the worktree did since base into exactly one
// commit with the given message: stage all, soft-reset to base, commit.
// The tree is unchanged by the operation, so what the gates verified is
// what gets committed.
func (p GitProvider) Squash(ctx context.Context, dir, baseSHA, message string) (string, error) {
	if !gitSHA.MatchString(baseSHA) || strings.TrimSpace(message) == "" {
		return "", errors.New("worktree: invalid squash request")
	}
	if _, err := p.run(ctx, dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := p.run(ctx, dir, "reset", "-q", "--soft", baseSHA); err != nil {
		return "", err
	}
	staged, err := p.run(ctx, dir, "diff", "--cached", "--quiet")
	if err == nil && staged.ExitCode == 0 {
		return "", errors.New("worktree: nothing to commit")
	}
	if _, err := p.runInput(ctx, dir, []byte(message), "commit", "-q", "-F", "-"); err != nil {
		return "", err
	}
	return p.Head(ctx, dir)
}

// Commit records the given paths at the root with a message; used for the
// loop's own bookkeeping (WORK.md, the plan) after a wave integrated.
func (p GitProvider) Commit(ctx context.Context, message string, paths ...string) (string, error) {
	if len(paths) == 0 || strings.TrimSpace(message) == "" {
		return "", errors.New("worktree: invalid commit request")
	}
	args := append([]string{"add", "--"}, paths...)
	if _, err := p.run(ctx, p.Root, args...); err != nil {
		return "", err
	}
	staged, err := p.run(ctx, p.Root, "diff", "--cached", "--quiet")
	if err == nil && staged.ExitCode == 0 {
		return p.Head(ctx, p.Root)
	}
	if _, err := p.runInput(ctx, p.Root, []byte(message), "commit", "-q", "-F", "-"); err != nil {
		return "", err
	}
	return p.Head(ctx, p.Root)
}

// IsAncestor says whether ancestor is reachable from descendant.
func (p GitProvider) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	result, err := p.run(ctx, p.Root, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil && result.ExitCode != 1 {
		return false, err
	}
	return result.ExitCode == 0, nil
}

// Show returns a file's content at a commit, or nil when absent.
func (p GitProvider) Show(ctx context.Context, dir, commit, path string) []byte {
	out, err := p.run(ctx, dir, "show", commit+":"+path)
	if err != nil {
		return nil
	}
	return out.Stdout
}

// Log returns `git log --oneline` between two commits, oldest first.
func (p GitProvider) Log(ctx context.Context, from, to string) ([]string, error) {
	out, err := p.run(ctx, p.Root, "log", "--reverse", "--format=%h %s", from+".."+to)
	if err != nil {
		return nil, err
	}
	return nonempty(string(out.Stdout)), nil
}

func (p GitProvider) run(ctx context.Context, dir string, args ...string) (publication.CommandResult, error) {
	return p.runInput(ctx, dir, nil, args...)
}

func (p GitProvider) runInput(ctx context.Context, dir string, stdin []byte, args ...string) (publication.CommandResult, error) {
	return p.runEnvironment(ctx, dir, stdin, nil, args...)
}

func (p GitProvider) runEnvironment(ctx context.Context, dir string, stdin []byte, environment []string, args ...string) (publication.CommandResult, error) {
	result, err := p.Runner.Run(ctx, publication.Command{
		Executable: p.Git, Args: args, Directory: dir, Stdin: stdin,
		Environment: append([]string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}, environment...),
		StdoutLimit: 16 << 20, StderrLimit: 64 << 10,
	})
	if result.StdoutTruncated || result.StderrTruncated {
		return result, errors.New("worktree: git output exceeded bound")
	}
	if err != nil {
		detail := strings.TrimSpace(string(result.Stderr))
		if detail == "" {
			return result, fmt.Errorf("git %s: %w", args[0], err)
		}
		return result, fmt.Errorf("git %s: %s", args[0], firstLine(detail))
	}
	return result, nil
}

func safeName(name string) bool {
	return name != "" && len(name) <= 128 && !strings.ContainsAny(name, "/\\ \t\n\x00") && name != "." && name != ".."
}

func nonempty(payload string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(payload, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func firstLine(value string) string {
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return value[:index]
	}
	return value
}
