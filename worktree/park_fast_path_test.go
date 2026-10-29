package worktree

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/batuta-ai/core/publication"
)

func TestParkCleanFastPathSafety(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{"clean", "assume_unchanged", "skip_worktree", "staged_only", "staged_during_probe", "ignored_untracked", "managed_staged", "unintegrated_head", "sparse_index"} {
		t.Run(shape, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			p, base := initRepo(t)
			git := func(dir string, args ...string) string {
				t.Helper()
				result, err := (publication.ExecRunner{}).Run(ctx, publication.Command{Executable: p.Git, Directory: dir, Args: args})
				if err != nil || result.ExitCode != 0 {
					t.Fatalf("fixture git %q: exit=%d err=%v stderr=%s", args, result.ExitCode, err, result.Stderr)
				}
				return strings.TrimSpace(string(result.Stdout))
			}
			write := func(dir, name, body string) {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if shape == "sparse_index" {
				write(p.Root, "inside/keep.txt", "inside\n")
				write(p.Root, "outside/keep.txt", "outside\n")
				git(p.Root, "add", "inside", "outside")
				git(p.Root, "commit", "-qm", "test: sparse tree")
				base = git(p.Root, "rev-parse", "HEAD")
			}
			root, err := p.Add(ctx, "fast", "batuta/demo/fast", base)
			if err != nil {
				t.Fatal(err)
			}
			const ref = "refs/batuta/parked/demo/fast-e1"
			wantFile := "# demo\n"
			wantRefs := map[string]map[string]string{}
			stage := func(path, body string) {
				t.Helper()
				result, err := p.runInput(ctx, root, []byte(body), "hash-object", "-w", "--stdin")
				if err != nil {
					t.Fatal(err)
				}
				git(root, "update-index", "--add", "--cacheinfo", "100644", strings.TrimSpace(string(result.Stdout)), path)
			}
			switch shape {
			case "assume_unchanged", "skip_worktree":
				flag := "--assume-unchanged"
				if shape == "skip_worktree" {
					flag = "--skip-worktree"
				}
				git(root, "update-index", flag, "README.md")
				wantFile = "hidden working data\n"
				write(root, "README.md", wantFile)
				wantRefs[ref] = map[string]string{"README.md": wantFile}
			case "staged_only":
				stage("README.md", "staged only\n")
				wantRefs[ref+"-index"] = map[string]string{"README.md": "staged only\n"}
			case "staged_during_probe":
				wantRefs[ref+"-index"] = map[string]string{"README.md": "late staged data\n"}
			case "ignored_untracked":
				info := git(root, "rev-parse", "--git-path", "info/exclude")
				if !filepath.IsAbs(info) {
					info = filepath.Join(root, info)
				}
				f, err := os.OpenFile(info, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := f.WriteString("\nignored.tmp\n")
				if err := f.Close(); err != nil || writeErr != nil {
					t.Fatalf("exclude: %v, %v", writeErr, err)
				}
				write(root, "ignored.tmp", "ignored data\n")
			case "managed_staged":
				stage(".batuta/staged.txt", "managed staged data\n")
				wantRefs[ref+"-index"] = map[string]string{"README.md": wantFile, ".batuta/staged.txt": "managed staged data\n"}
			case "unintegrated_head":
				git(root, "commit", "--allow-empty", "-qm", "test: unmerged identity")
				wantRefs[ref] = map[string]string{"README.md": wantFile}
			case "sparse_index":
				git(root, "sparse-checkout", "init", "--cone", "--sparse-index")
				git(root, "sparse-checkout", "set", "inside")
			}
			head := git(root, "rev-parse", "HEAD")
			indexPath := git(root, "rev-parse", "--git-path", "index")
			if !filepath.IsAbs(indexPath) {
				indexPath = filepath.Join(root, indexPath)
			}
			index := parkSequenceRead(t, indexPath)
			var calls [][]string
			changed := false
			p.Runner = observingGitRunner(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
				result, err := (publication.ExecRunner{}).Run(ctx, command)
				args := parkSequenceArgs(command.Args)
				calls = append(calls, slices.Clone(args))
				if shape == "staged_during_probe" && !changed && len(args) > 0 && args[0] == "read-tree" && err == nil && result.ExitCode == 0 {
					changed = true
					stage("README.md", "late staged data\n")
					index = parkSequenceRead(t, indexPath)
				}
				return result, err
			})
			sha, err := p.Park(ctx, root, ref, "wip: fast proof")
			p.Runner = publication.ExecRunner{}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(parkSequenceRead(t, indexPath), index) || string(parkSequenceRead(t, filepath.Join(root, "README.md"))) != wantFile || git(root, "rev-parse", "HEAD") != head || git(p.Root, "rev-parse", "HEAD") != base {
				t.Fatal("Park changed real index, files or HEAD")
			}
			if shape == "ignored_untracked" && string(parkSequenceRead(t, filepath.Join(root, "ignored.tmp"))) != "ignored data\n" {
				t.Fatal("Park changed ignored data")
			}
			if shape == "staged_during_probe" && !changed {
				t.Fatal("staged mutation boundary not reached")
			}
			refs, err := p.Parked(ctx, "demo")
			if err != nil {
				t.Fatal(err)
			}
			if shape == "sparse_index" {
				if _, err := os.Stat(filepath.Join(root, "outside/keep.txt")); !os.IsNotExist(err) {
					t.Fatalf("Park materialized sparse data: %v", err)
				}
				if !slices.ContainsFunc(calls, func(args []string) bool { return args[0] == "add" }) {
					t.Fatal("sparse missing files bypassed the complete snapshot")
				}
			} else {
				if len(refs) != len(wantRefs) {
					t.Fatalf("recovery refs=%v, want %d; Park=%q calls=%q", refs, len(wantRefs), sha, calls)
				}
				for _, saved := range refs {
					files, found := wantRefs[saved.Ref]
					if !found {
						t.Fatalf("unexpected ref %s", saved.Ref)
					}
					for path, body := range files {
						if got := string(p.Show(ctx, root, saved.Ref, path)); got != body {
							t.Errorf("lost %s:%s: got %q want %q", saved.Ref, path, got, body)
						}
					}
					if protected, err := p.IsAncestor(ctx, head, saved.SHA); err != nil || !protected {
						t.Fatalf("unprotected HEAD at %s: %t, %v", saved.Ref, protected, err)
					}
				}
			}
			if shape == "clean" || shape == "ignored_untracked" {
				if sha != "" || len(calls) > 6 {
					t.Fatalf("clean Park budget: sha=%q calls=%d, want empty SHA and at most 6; argv=%q", sha, len(calls), calls)
				}
			}
			t.Logf("shape=%s Git calls=%d, refs=%d, real index/files/HEAD preserved", shape, len(calls), len(refs))
		})
	}
}

func TestParkCleanProbeBounds(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"stdout_bound", "stdout_bound_with_runner_error", "stderr_bound", "nonzero_status"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			p, base := initRepo(t)
			root, err := p.Add(ctx, "probe-bound", "batuta/demo/probe-bound", base)
			if err != nil {
				t.Fatal(err)
			}
			const body = "pending bounded-probe data\n"
			if err := os.WriteFile(filepath.Join(root, "pending.txt"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			path, err := p.run(ctx, root, "rev-parse", "--git-path", "index")
			if err != nil {
				t.Fatal(err)
			}
			indexPath := strings.TrimSpace(string(path.Stdout))
			if !filepath.IsAbs(indexPath) {
				indexPath = filepath.Join(root, indexPath)
			}
			before := parkSequenceRead(t, indexPath)
			injected := errors.New("clean status observer failed")
			seen, fallback := false, false
			var status publication.CommandResult
			p.Runner = observingGitRunner(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
				args := parkSequenceArgs(command.Args)
				isStatus := len(args) > 0 && args[0] == "status"
				if isStatus {
					seen = true
					command.StdoutLimit = 1
					if mode == "stderr_bound" {
						command.StdoutLimit, command.StderrLimit = 16<<20, 1
						command.Environment = append(command.Environment, "GIT_TRACE=1")
					} else if mode == "nonzero_status" {
						command.Args = []string{"status", "--invalid-clean-probe-option"}
					}
				} else if len(args) > 0 && args[0] == "add" {
					fallback = true
				}
				result, err := (publication.ExecRunner{}).Run(ctx, command)
				if isStatus {
					status = result
					if mode == "stdout_bound_with_runner_error" {
						err = errors.Join(err, injected)
					}
				}
				return result, err
			})
			const ref = "refs/batuta/parked/demo/probe-bound-e1"
			sha, parkErr := p.Park(ctx, root, ref, "wip: bounded proof")
			p.Runner = publication.ExecRunner{}
			if !seen || !bytes.Equal(parkSequenceRead(t, indexPath), before) || string(parkSequenceRead(t, filepath.Join(root, "pending.txt"))) != body {
				t.Fatal("status not observed or real index/data changed")
			}
			if head, err := p.Head(ctx, root); err != nil || head != base {
				t.Fatalf("worktree HEAD changed: %s, %v", head, err)
			}
			if mode == "stdout_bound" {
				if status.ExitCode != 0 || !status.StdoutTruncated || status.StderrTruncated {
					t.Fatalf("not a successful bounded Git status: %+v", status)
				}
				if parkErr != nil || sha == "" || !fallback || string(p.Show(ctx, root, ref, "pending.txt")) != body {
					t.Fatalf("bounded dirty status prevented recovery fallback: sha=%q fallback=%t err=%v", sha, fallback, parkErr)
				}
			} else {
				if parkErr == nil || sha != "" || fallback {
					t.Fatalf("genuine probe failure was swallowed: mode=%s sha=%q fallback=%t err=%v", mode, sha, fallback, parkErr)
				}
				if mode == "stdout_bound_with_runner_error" && (!status.StdoutTruncated || !errors.Is(parkErr, injected)) {
					t.Fatalf("bounded output hid genuine runner error: %v", parkErr)
				}
				if mode == "stderr_bound" && !status.StderrTruncated {
					t.Fatal("stderr did not exceed its real bound")
				}
				if mode == "nonzero_status" && status.ExitCode == 0 {
					t.Fatal("invalid status unexpectedly succeeded")
				}
				if refs, err := p.Parked(ctx, "demo"); err != nil || len(refs) != 0 {
					t.Fatalf("probe failure created refs: %v, %v", refs, err)
				}
			}
		})
	}
}

func parkSequenceArgs(args []string) []string {
	for len(args) >= 2 && args[0] == "-c" {
		args = args[2:]
	}
	return args
}
