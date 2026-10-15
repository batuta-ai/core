// Package worktree gives the loop its git operations on a file host: the
// user's checkout as the integration root, one worktree per task attempt
// under `.batuta/worktrees/`, and the small set of read and write
// operations the runner needs around them. Every command is argv through
// the publication runner — no shell.
package worktree

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/batuta-ai/core/publication"
)

// Dir is where task worktrees live, relative to the repository root.
const Dir = ".batuta/worktrees"

// Excluded lists the runtime directories the loop keeps out of the user's
// index through `.git/info/exclude` (never `.gitignore`, which is theirs).
var Excluded = []string{".batuta/worktrees/", ".batuta/journal/", ".batuta/runs/", ".batuta/asks/", ".batuta/scout/", ".batuta/reviews/supervision/", ".batuta/handoff.md"}

// ManagedPaths are the tracked files the conductor owns; a dirty one never
// fails a clean-tree check (references/state.md).
var ManagedPaths = []string{"WORK.md", ".batuta/"}

var (
	ErrNotRepository = errors.New("worktree: not a git repository")
	ErrDirty         = errors.New("worktree: working tree is dirty")

	gitSHA     = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)
	branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// GitProvider runs git against one repository root. Home is the directory
// that holds the task worktrees; empty means RepositoryHome(Root).
type GitProvider struct {
	Git                string
	Runner             publication.CommandRunner
	Root               string
	Home               string
	Processes          ProcessScanner
	Stopper            ProcessStopper
	OnProcessesStopped func(path string, stopped []StoppedProcess, err error)
}

// RepositoryHome is the worktree home inside the repository at root.
func RepositoryHome(root string) string {
	return filepath.Join(root, filepath.FromSlash(Dir))
}

// CacheHome is the worktree home under the user cache directory for the
// repository at root: <cache>/batuta/worktrees/<digest>, the digest being the
// first 16 hex characters of the SHA-256 of the canonical root path. The path
// is not case-folded, so spellings that differ only in case may map to two
// homes on a case-insensitive file system.
func CacheHome(root string) (string, error) {
	canonical, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("worktree: resolve root: %w", err)
	}
	if canonical, err = filepath.EvalSymlinks(canonical); err != nil {
		return "", fmt.Errorf("worktree: resolve root: %w", err)
	}
	canonical = filepath.Clean(canonical)
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("worktree: locate cache directory: %w", err)
	}
	sum := sha256.Sum256([]byte(canonical))
	return filepath.Join(cache, "batuta", "worktrees", hex.EncodeToString(sum[:])[:16]), nil
}

// Path is where the worktree called name lives. It does not touch the disk.
func (p GitProvider) Path(name string) string {
	home := p.Home
	if home == "" {
		home = RepositoryHome(p.Root)
	}
	return filepath.Join(home, name)
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

// Add creates the worktree for name under the provider's home on a new branch
// at base. A leftover worktree or branch of the same name from an aborted run
// is removed first.
func (p GitProvider) Add(ctx context.Context, name, branch, baseSHA string) (string, error) {
	return p.addAt(ctx, p.Path(name), branch, baseSHA)
}

// AddAt creates a worktree at path when it is a direct child of an allowed home.
func (p GitProvider) AddAt(ctx context.Context, path, branch, baseSHA string, homes []string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("worktree: invalid worktree path")
	}
	allowed := false
	for _, home := range homes {
		if filepath.IsAbs(home) && filepath.Clean(home) == home && filepath.Dir(path) == home {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", errors.New("worktree: worktree path is outside allowed homes")
	}
	return p.addAt(ctx, path, branch, baseSHA)
}

func (p GitProvider) addAt(ctx context.Context, path, branch, baseSHA string) (string, error) {
	name := filepath.Base(path)
	if !safeName(name) || !branchName.MatchString(branch) || !gitSHA.MatchString(baseSHA) {
		return "", errors.New("worktree: invalid worktree name, branch or base")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("worktree: create %s: %w", filepath.Dir(path), err)
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
			_, _ = p.StopInside(ctx, path)
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

// ProcessesIn lists processes in a registered worktree under an allowed home.
func (p GitProvider) ProcessesIn(ctx context.Context, path string) ([]Process, error) {
	canonical, allowed, err := p.processTarget(ctx, path)
	if err != nil || !allowed {
		return nil, err
	}
	return p.scanProcesses(ctx, canonical)
}

// StopInside stops processes in a registered worktree and reports the result.
func (p GitProvider) StopInside(ctx context.Context, path string) ([]StoppedProcess, error) {
	canonical, allowed, err := p.processTarget(ctx, path)
	if err != nil || !allowed {
		p.reportStopped(path, nil, err)
		return nil, err
	}
	procs, scanErr := p.scanProcesses(ctx, canonical)
	var partial *PartialScanError
	if scanErr != nil && !errors.As(scanErr, &partial) {
		p.reportStopped(path, nil, scanErr)
		return nil, scanErr
	}
	var stopped []StoppedProcess
	var stopErr error
	if len(procs) > 0 {
		stopper := p.Stopper
		if stopper == nil {
			stopper = platformProcessStopper()
		}
		stopped, stopErr = stopper.Stop(ctx, canonical, procs, 3*time.Second)
	}
	err = errors.Join(scanErr, stopErr)
	p.reportStopped(path, stopped, err)
	return stopped, err
}

func (p GitProvider) reportStopped(path string, stopped []StoppedProcess, err error) {
	if p.OnProcessesStopped != nil && (len(stopped) > 0 || err != nil) {
		p.OnProcessesStopped(path, stopped, err)
	}
}

func (p GitProvider) scanProcesses(ctx context.Context, path string) ([]Process, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	scanner := p.Processes
	if scanner == nil {
		scanner = platformProcessScanner()
	}
	return insideWith(ctx, insideEnv{
		scanner: scanner, home: home, callerPID: os.Getpid(), parentPID: platformParentPID,
	}, path)
}

func (p GitProvider) processTarget(ctx context.Context, path string) (string, bool, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", false, nil
	}
	canonical, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("worktree: resolve process target: %w", err)
	}
	canonical = filepath.Clean(canonical)
	homes := []string{p.Home, RepositoryHome(p.Root)}
	allowed := false
	for _, home := range homes {
		if home == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(home)
		if err == nil && filepath.Dir(canonical) == filepath.Clean(resolved) {
			allowed = true
			break
		}
	}
	if !allowed {
		cache, err := CacheHome(p.Root)
		if err != nil {
			return "", false, err
		}
		resolved, err := filepath.EvalSymlinks(cache)
		allowed = err == nil && filepath.Dir(canonical) == filepath.Clean(resolved)
	}
	if !allowed {
		return "", false, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	if err := safeProcessTarget(path, userHome); err != nil {
		return "", false, err
	}
	if err := safeProcessTarget(canonical, userHome); err != nil {
		return "", false, err
	}
	// The primary checkout is never a batuta worktree, even when a configured
	// home holds it and git lists it: its processes are the user's own.
	if pathWithin(canonical, canonicalDir(p.Root)) {
		return "", false, nil
	}
	out, err := p.run(ctx, p.Root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", false, err
	}
	for i, record := range worktreeRecords(out.Stdout) {
		if canonicalDir(record.path) != canonical {
			continue
		}
		if i == 0 {
			// git lists the main worktree first.
			return "", false, nil
		}
		return canonical, true, nil
	}
	return "", false, nil
}

// PruneRegistry drops the registration of every worktree whose directory is
// gone. It deletes no branch and no directory.
func (p GitProvider) PruneRegistry(ctx context.Context) error {
	_, err := p.run(ctx, p.Root, "worktree", "prune")
	return err
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
		stagedEnv := []string{"GIT_INDEX_FILE=" + stagedPath}
		if err := p.parkConflictStages(ctx, root, ref, message, head, stagedEnv); err != nil {
			return "", err
		}
		staged, err := p.runEnvironment(ctx, root, nil, stagedEnv, "write-tree")
		if err != nil {
			return "", err
		}
		if stagedTree := strings.TrimSpace(string(staged.Stdout)); stagedTree != tree {
			if _, err := p.parkTree(ctx, root, ref+"-index", message+" (index)", head, stagedTree, false); err != nil {
				return "", err
			}
		}
	}
	return p.parkTree(ctx, root, ref, message, head, tree, false)
}

// parkConflictStages serializes each side separately: a single Git tree cannot
// represent multiple stages or directory/file conflicts between those stages.
// All commands use the copied index; the executor's index remains untouched.
func (p GitProvider) parkConflictStages(ctx context.Context, root, ref, message, head string, env []string) error {
	// Stream only unmerged entries to disk. The observer receives all output even
	// when the runner's small diagnostic capture is truncated.
	entries, err := os.CreateTemp("", "batuta-conflicts-")
	if err != nil {
		return err
	}
	defer os.Remove(entries.Name())
	defer entries.Close()
	result, err := p.Runner.Run(ctx, publication.Command{
		Executable: p.Git, Args: []string{"ls-files", "-u", "-z"}, Directory: root,
		Environment: append([]string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}, env...),
		Observer:    entries, StdoutLimit: 1, StderrLimit: 64 << 10,
	})
	if err != nil {
		return fmt.Errorf("worktree: list unmerged entries: %w", err)
	}
	if result.ExitCode != 0 || result.StderrTruncated {
		return errors.New("worktree: list unmerged entries failed")
	}
	info, err := entries.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	stagePath := entries.Name() + "-index"
	defer os.Remove(stagePath)
	// Stage zero stays in the copied index; remove only its conflicted entries.
	// Build each conflict side in a separate scratch index in bounded batches.
	for _, stage := range []int{1, 2, 3, 0} {
		stageEnv := env
		if stage != 0 {
			stageEnv = []string{"GIT_INDEX_FILE=" + stagePath}
			if _, err := p.runEnvironment(ctx, root, nil, stageEnv, "read-tree", "--empty"); err != nil {
				return err
			}
		}
		if _, err := entries.Seek(0, io.SeekStart); err != nil {
			return err
		}
		scanner := bufio.NewScanner(entries)
		scanner.Split(splitIndexEntry)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		var batch bytes.Buffer
		found := false
		flush := func() error {
			if batch.Len() == 0 {
				return nil
			}
			_, err := p.runEnvironment(ctx, root, batch.Bytes(), stageEnv, "update-index", "-z", "--index-info")
			batch.Reset()
			return err
		}
		for scanner.Scan() {
			metadata, path, ok := bytes.Cut(scanner.Bytes(), []byte{'\t'})
			fields := strings.Fields(string(metadata))
			if !ok || len(fields) != 3 || (fields[2] != "1" && fields[2] != "2" && fields[2] != "3") {
				return errors.New("worktree: invalid unmerged entry")
			}
			if stage == 0 {
				fmt.Fprintf(&batch, "0 %s\t%s\x00", strings.Repeat("0", len(fields[1])), path)
			} else if fields[2] == strconv.Itoa(stage) {
				fmt.Fprintf(&batch, "%s %s\t%s\x00", fields[0], fields[1], path)
				found = true
			}
			if batch.Len() >= 64<<10 {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if err := flush(); err != nil {
			return err
		}
		if !found {
			continue
		}
		tree, err := p.runEnvironment(ctx, root, nil, stageEnv, "write-tree")
		if err != nil {
			return err
		}
		stageRef := fmt.Sprintf("%s-index-stage-%d", ref, stage)
		if _, err := p.parkTree(ctx, root, stageRef, message+" (conflicted index)", head, strings.TrimSpace(string(tree.Stdout)), true); err != nil {
			return err
		}
	}
	return nil
}

func splitIndexEntry(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) != 0 {
		return 0, nil, errors.New("worktree: truncated index entry")
	}
	return 0, nil, nil
}

func (p GitProvider) parkTree(ctx context.Context, root, ref, message, head, tree string, preserveStage bool) (string, error) {
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
		// Keep the stage identity even when its blobs are already integrated.
		if integrated && !preserveStage {
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
	Ref       string   `json:"ref"`
	SHA       string   `json:"sha"`
	Conflicts []string `json:"conflicted_paths,omitempty"`
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
		ref := ParkedRef{Ref: parts[0], SHA: parts[1]}
		for stage := 1; stage <= 3; stage++ {
			if strings.HasSuffix(ref.Ref, fmt.Sprintf("-index-stage-%d", stage)) {
				paths, err := p.run(ctx, p.Root, "ls-tree", "-r", "--name-only", "-z", ref.SHA)
				if err != nil {
					return nil, err
				}
				ref.Conflicts = strings.Split(strings.TrimSuffix(string(paths.Stdout), "\x00"), "\x00")
			}
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// CleanParked deletes only snapshots whose complete tree is already in the
// integration branch's history. Squashed commits need not share ancestry.
func (p GitProvider) CleanParked(ctx context.Context, slug, branch string) ([]ParkedRef, error) {
	kept, integrated, err := p.PlanParkedCleanup(ctx, slug, branch)
	if err != nil {
		return nil, err
	}
	return kept, p.DeleteParked(ctx, integrated)
}

// PlanParkedCleanup computes retention without deleting refs. Callers can
// journal the retained list before allowing any snapshots to be removed.
func (p GitProvider) PlanParkedCleanup(ctx context.Context, slug, branch string) ([]ParkedRef, []ParkedRef, error) {
	refs, err := p.Parked(ctx, slug)
	if err != nil || len(refs) == 0 {
		return refs, nil, err
	}
	refTrees := make([]string, len(refs))
	wantedTrees := make(map[string]bool, len(refs))
	for index, ref := range refs {
		commit, err := p.run(ctx, p.Root, "cat-file", "-p", ref.SHA)
		if err != nil {
			return nil, nil, err
		}
		tree := strings.TrimPrefix(firstLine(string(commit.Stdout)), "tree ")
		if !gitSHA.MatchString(tree) {
			return nil, nil, errors.New("worktree: invalid parked tree")
		}
		refTrees[index] = tree
		wantedTrees[tree] = true
	}

	const historyPageSize = 50_000
	matchedTrees := make(map[string]bool, len(wantedTrees))
	for skip := 0; len(matchedTrees) < len(wantedTrees); skip += historyPageSize {
		result, err := p.run(ctx, p.Root, "log", "--format=%T", "--max-count="+strconv.Itoa(historyPageSize), "--skip="+strconv.Itoa(skip), "refs/heads/"+branch, "--")
		if err != nil {
			return nil, nil, err
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

	var kept, integrated []ParkedRef
	for index, ref := range refs {
		if matchedTrees[refTrees[index]] {
			integrated = append(integrated, ref)
		} else {
			kept = append(kept, ref)
		}
	}
	return kept, integrated, nil
}

// DeleteParked deletes exactly the planned versions, refusing replaced refs.
func (p GitProvider) DeleteParked(ctx context.Context, refs []ParkedRef) error {
	for _, ref := range refs {
		if _, err := p.run(ctx, p.Root, "update-ref", "-d", ref.Ref, ref.SHA); err != nil {
			return err
		}
	}
	return nil
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

// MergePreviewResult describes how a ref would merge into a branch. Base is
// the merge base, Changed the paths the ref changed against it outside
// `.batuta`, Conflicts the paths a merge would leave conflicted and Merged
// whether the ref's content is already in the branch.
type MergePreviewResult struct {
	Base      string
	Changed   []string
	Conflicts []string
	Merged    bool
}

// MergePreview reports how ref merges into branch without a worktree, an
// index or a ref update: `merge-tree --write-tree` only writes loose objects.
func (p GitProvider) MergePreview(ctx context.Context, branch, ref string) (MergePreviewResult, error) {
	if !branchName.MatchString(branch) || !branchName.MatchString(ref) {
		return MergePreviewResult{}, errors.New("worktree: invalid merge preview request")
	}
	// Both names resolve once, so every command below reads the same two
	// commits. The qualified name keeps a tag that shares the branch's name
	// out of it.
	head, err := p.previewCommit(ctx, "refs/heads/"+branch)
	if err != nil {
		return MergePreviewResult{}, err
	}
	tip, err := p.previewCommit(ctx, ref)
	if err != nil {
		return MergePreviewResult{}, err
	}
	base, err := p.run(ctx, p.Root, "merge-base", head, tip)
	if err != nil {
		if base.ExitCode == 1 && len(bytes.TrimSpace(base.Stderr)) == 0 {
			return MergePreviewResult{}, fmt.Errorf("worktree: unrelated histories: %s and %s share no merge base", branch, ref)
		}
		return MergePreviewResult{}, err
	}
	result := MergePreviewResult{Base: strings.TrimSpace(string(base.Stdout))}
	diff, err := p.run(ctx, p.Root, "diff", "--name-only", "-z", result.Base, tip, "--")
	if err != nil {
		return MergePreviewResult{}, err
	}
	result.Changed = make([]string, 0)
	for _, path := range nulFields(diff.Stdout) {
		if path != ".batuta" && !strings.HasPrefix(path, ".batuta/") {
			result.Changed = append(result.Changed, path)
		}
	}
	result.Conflicts = make([]string, 0)
	merged, err := p.refIntegrated(ctx, head, tip)
	if err != nil {
		return MergePreviewResult{}, err
	}
	if merged {
		result.Merged = true
		return result, nil
	}
	tree, err := p.run(ctx, p.Root, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", head, tip)
	// Exit 1 is a conflict report only when git's output arrived whole.
	if tree.StdoutTruncated || tree.StderrTruncated {
		return MergePreviewResult{}, err
	}
	if err != nil && tree.ExitCode != 1 {
		if tree.ExitCode == 0 {
			return MergePreviewResult{}, err
		}
		return MergePreviewResult{}, mergePreviewError(tree.ExitCode, string(tree.Stderr))
	}
	if fields := nulFields(tree.Stdout); tree.ExitCode == 1 && len(fields) > 1 {
		result.Conflicts = fields[1:]
	}
	return result, nil
}

// nulFields splits `-z` output, where git writes each path verbatim and ends
// it with NUL instead of quoting unusual names.
func nulFields(payload []byte) []string {
	fields := make([]string, 0)
	for _, field := range strings.Split(string(payload), "\x00") {
		if field != "" {
			fields = append(fields, field)
		}
	}
	return fields
}

// previewCommit resolves a name to the commit it points at now.
func (p GitProvider) previewCommit(ctx context.Context, name string) (string, error) {
	resolved, err := p.run(ctx, p.Root, "rev-parse", "--verify", "--quiet", name+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("worktree: merge preview cannot resolve %s: %w", name, err)
	}
	return strings.TrimSpace(string(resolved.Stdout)), nil
}

// refIntegrated says whether the tip commit's tree appears in the head
// commit's history, the identity PlanParkedCleanup uses for squashed work,
// or tip is an ancestor of head.
func (p GitProvider) refIntegrated(ctx context.Context, head, tip string) (bool, error) {
	ancestor, err := p.IsAncestor(ctx, tip, head)
	if err != nil || ancestor {
		return ancestor, err
	}
	tree, err := p.run(ctx, p.Root, "rev-parse", "--verify", tip+"^{tree}")
	if err != nil {
		return false, err
	}
	want := strings.TrimSpace(string(tree.Stdout))
	// Pages keep a long history inside the runner's output bound.
	const historyPageSize = 50_000
	for skip := 0; ; skip += historyPageSize {
		history, err := p.run(ctx, p.Root, "log", "--format=%T", "--max-count="+strconv.Itoa(historyPageSize), "--skip="+strconv.Itoa(skip), head, "--")
		if err != nil {
			return false, err
		}
		trees := nonempty(string(history.Stdout))
		if slices.Contains(trees, want) {
			return true, nil
		}
		if len(trees) < historyPageSize {
			return false, nil
		}
	}
}

// mergePreviewError explains a `git merge-tree --write-tree` failure that is
// not a conflict report (exit 1); older git lacks the flag and exits 129.
func mergePreviewError(exitCode int, stderr string) error {
	tail := strings.TrimSpace(stderr)
	if len(tail) > 400 {
		tail = tail[len(tail)-400:]
	}
	return fmt.Errorf("worktree: git merge-tree exited %d; merge preview needs git 2.38 or newer: %s", exitCode, tail)
}

// ManagedWorktree is a registered worktree the loop made: it sits directly in
// one of the homes, on a `batuta/<slug>/task-<N>-e<K>` branch, in a directory
// named `<slug>-task-<N>-e<K>`. Missing is set when git marks it prunable or
// its directory is gone.
type ManagedWorktree struct {
	Path      string
	Branch    string
	Slug      string
	Task      int
	Execution int
	Missing   bool
}

var managedBranch = regexp.MustCompile(`^batuta/(.+)/task-([0-9]+)-e([0-9]+)$`)

// Managed lists the registered worktrees that live directly in one of homes
// and whose branch and directory name have the shape the runner generates.
func (p GitProvider) Managed(ctx context.Context, homes []string) ([]ManagedWorktree, error) {
	out, err := p.run(ctx, p.Root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	canonical := make([]string, 0, len(homes))
	for _, home := range homes {
		canonical = append(canonical, canonicalDir(home))
	}
	managed := make([]ManagedWorktree, 0)
	for _, record := range worktreeRecords(out.Stdout) {
		if record.branch == "" || !slices.Contains(canonical, canonicalDir(filepath.Dir(record.path))) {
			continue
		}
		match := managedBranch.FindStringSubmatch(record.branch)
		if match == nil {
			continue
		}
		task, taskErr := strconv.Atoi(match[2])
		execution, executionErr := strconv.Atoi(match[3])
		if taskErr != nil || executionErr != nil || filepath.Base(record.path) != fmt.Sprintf("%s-task-%d-e%d", match[1], task, execution) {
			continue
		}
		_, statErr := os.Lstat(record.path)
		managed = append(managed, ManagedWorktree{
			Path: record.path, Branch: record.branch, Slug: match[1], Task: task, Execution: execution,
			Missing: record.prunable || statErr != nil,
		})
	}
	return managed, nil
}

// canonicalDir resolves symlinks of dir, or cleans its absolute path when the
// directory does not exist, so a deleted home still matches its worktrees.
func canonicalDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return filepath.Clean(dir)
}

type worktreeRecord struct {
	path     string
	branch   string
	prunable bool
}

// worktreeRecords reads `git worktree list --porcelain -z`: NUL ends each
// field and an empty field ends the entry. Unlike nulFields it keeps that
// boundary.
func worktreeRecords(payload []byte) []worktreeRecord {
	var records []worktreeRecord
	var current worktreeRecord
	open := false
	flush := func() {
		if open {
			records = append(records, current)
		}
		current, open = worktreeRecord{}, false
	}
	for _, field := range strings.Split(string(payload), "\x00") {
		key, value, _ := strings.Cut(field, " ")
		switch {
		case field == "":
			flush()
		case key == "worktree":
			flush()
			current, open = worktreeRecord{path: value}, true
		case key == "branch":
			current.branch = strings.TrimPrefix(value, "refs/heads/")
		case key == "prunable":
			current.prunable = true
		}
	}
	flush()
	return records
}
