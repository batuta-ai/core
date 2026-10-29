package worktree

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/batuta-ai/core/publication"
)

type parkCommandObservation struct {
	command publication.Command
	result  publication.CommandResult
	err     error
	stream  []byte
}

func TestParkCommandSequenceProtectionCases(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{"clean_integrated", "dirty", "distinct_staged_working", "preserved_ref", "matching_tree_different_ancestry", "index_conflicts", "git_error"} {
		t.Run(shape, func(t *testing.T) {
			t.Parallel()
			p, base := initRepo(t)
			ctx := context.Background()
			root, err := p.Add(ctx, "sequence", "batuta/demo/sequence", base)
			if err != nil {
				t.Fatal(err)
			}
			git := func(args ...string) string {
				t.Helper()
				result, err := (publication.ExecRunner{}).Run(ctx, publication.Command{Executable: p.Git, Directory: root, Args: args})
				if err != nil {
					t.Fatalf("fixture git %q: %v; %s", args, err, result.Stderr)
				}
				return strings.TrimSpace(string(result.Stdout))
			}
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			const ref = "refs/batuta/parked/demo/sequence-e1"
			previous := ""
			wantFiles := map[string]string{"README.md": "# demo\n"}
			wantRefs := map[string]map[string]string{}
			switch shape {
			case "dirty", "preserved_ref", "matching_tree_different_ancestry":
				write("work.txt", "working\n")
				wantFiles["work.txt"] = "working\n"
				wantRefs[ref] = wantFiles
				if shape != "dirty" {
					previous, err = p.Park(ctx, root, ref, "wip: previous")
					if err != nil || previous == "" {
						t.Fatalf("prepare ref = %q, %v", previous, err)
					}
				}
				if shape == "matching_tree_different_ancestry" {
					git("add", "work.txt")
					git("commit", "-qm", "wip: independent executor commit")
					if git("rev-parse", "HEAD^{tree}") != git("rev-parse", previous+"^{tree}") || git("rev-parse", "HEAD") == previous {
						t.Fatal("fixture must have matching trees and distinct commits")
					}
				}
			case "distinct_staged_working":
				write("work.txt", "staged\n")
				git("add", "work.txt")
				write("work.txt", "working\n")
				wantFiles["work.txt"] = "working\n"
				wantRefs[ref] = wantFiles
				wantRefs[ref+"-index"] = map[string]string{"README.md": "# demo\n", "work.txt": "staged\n"}
			case "index_conflicts":
				var entries strings.Builder
				for stage, body := range []string{"base\n", "ours\n", "theirs\n"} {
					blob, err := p.runInput(ctx, root, []byte(body), "hash-object", "-w", "--stdin")
					if err != nil {
						t.Fatal(err)
					}
					fmt.Fprintf(&entries, "100644 %s %d\tconflict name.txt\x00", strings.TrimSpace(string(blob.Stdout)), stage+1)
					wantRefs[fmt.Sprintf("%s-index-stage-%d", ref, stage+1)] = map[string]string{"conflict name.txt": body}
				}
				if _, err := p.runInput(ctx, root, []byte(entries.String()), "update-index", "-z", "--index-info"); err != nil {
					t.Fatal(err)
				}
				write("conflict name.txt", "unresolved\n")
				wantFiles["conflict name.txt"] = "unresolved\n"
				wantRefs[ref] = wantFiles
			}
			head := git("rev-parse", "HEAD")
			indexPath := git("rev-parse", "--git-path", "index")
			if !filepath.IsAbs(indexPath) {
				indexPath = filepath.Join(root, indexPath)
			}
			index := parkSequenceRead(t, indexPath)
			var calls []parkCommandObservation
			p.Runner = observingGitRunner(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
				if shape == "git_error" && slices.Equal(parkSequenceArgs(command.Args), []string{"read-tree", head}) {
					command.Args = []string{"read-tree", "refs/heads/missing-sequence-object"}
				}
				var streamed bytes.Buffer
				if command.Observer != nil {
					command.Observer = io.MultiWriter(command.Observer, &streamed)
				}
				result, err := (publication.ExecRunner{}).Run(ctx, command)
				command.Args = slices.Clone(command.Args)
				command.Environment = slices.Clone(command.Environment)
				command.Stdin = bytes.Clone(command.Stdin)
				calls = append(calls, parkCommandObservation{command, result, err, bytes.Clone(streamed.Bytes())})
				return result, err
			})
			sha, parkErr := p.Park(ctx, root, ref, "wip: sequence")
			p.Runner = publication.ExecRunner{}
			for i, call := range calls {
				t.Logf("Park %02d argv=%q dir=%q env=%q stdin=%q exit=%d stdout=%q stderr=%q streamed=%q truncated=%t/%t err=%v", i+1, call.command.Args, call.command.Directory, call.command.Environment, call.command.Stdin, call.result.ExitCode, call.result.Stdout, call.result.Stderr, call.stream, call.result.StdoutTruncated, call.result.StderrTruncated, call.err)
				missing := call.command.Args[0] == "show-ref" && (call.result.ExitCode == 1 || call.result.ExitCode == 128)
				notAncestor := call.command.Args[0] == "merge-base" && call.result.ExitCode == 1
				injected := shape == "git_error" && call.command.Args[0] == "read-tree"
				if call.err != nil && !missing && !notAncestor && !injected {
					t.Errorf("unexpected Git error at %d: %v", i+1, call.err)
				}
			}
			t.Logf("Park shape=%s calls=%d returned=%q err=%v", shape, len(calls), sha, parkErr)
			if shape == "git_error" {
				if len(calls) != 3 || calls[2].err == nil || parkErr == nil || !strings.Contains(parkErr.Error(), "missing-sequence-object") || sha != "" {
					t.Fatalf("missing real read-tree error: calls=%d sha=%q err=%v", len(calls), sha, parkErr)
				}
			} else if parkErr != nil {
				t.Fatal(parkErr)
			}
			if got := parkSequenceRead(t, indexPath); !bytes.Equal(got, index) {
				t.Fatal("Park changed real index bytes")
			}
			if git("rev-parse", "HEAD") != head || git("rev-parse", "refs/heads/batuta/demo/sequence") != head || git("rev-parse", "refs/heads/main") != base {
				t.Fatal("Park moved HEAD or a branch")
			}
			for name, body := range wantFiles {
				if got := string(parkSequenceRead(t, filepath.Join(root, name))); got != body {
					t.Errorf("working %s = %q, want %q", name, got, body)
				}
			}
			refs, err := p.Parked(ctx, "demo")
			if err != nil || len(refs) != len(wantRefs) {
				t.Fatalf("recovery refs = %v, %v; want %d", refs, err, len(wantRefs))
			}
			for _, saved := range refs {
				files, ok := wantRefs[saved.Ref]
				if !ok {
					t.Fatalf("unexpected recovery ref %s", saved.Ref)
				}
				if paths := strings.Split(git("ls-tree", "-r", "--name-only", saved.Ref), "\n"); len(paths) != len(files) {
					t.Errorf("%s tree paths = %q, want %d", saved.Ref, paths, len(files))
				}
				for name, body := range files {
					if got := string(p.Show(ctx, root, saved.Ref, name)); got != body {
						t.Errorf("%s:%s = %q, want %q", saved.Ref, name, got, body)
					}
				}
				if ancestor, err := p.IsAncestor(ctx, head, saved.SHA); err != nil || !ancestor {
					t.Errorf("%s does not preserve HEAD ancestry: %t, %v", saved.Ref, ancestor, err)
				}
			}
			switch shape {
			case "clean_integrated":
				if sha != "" {
					t.Fatalf("integrated Park = %s", sha)
				}
				assertCleanParkSequence(t, calls, p, root, ref, head)
			case "preserved_ref":
				if sha != "" || git("rev-parse", ref) != previous {
					t.Fatal("already protected tree was not reused")
				}
			case "matching_tree_different_ancestry":
				if sha != head || git("rev-parse", ref) != head {
					t.Fatal("matching tree hid unprotected HEAD ancestry")
				}
			case "dirty", "distinct_staged_working", "index_conflicts":
				if sha == "" || git("rev-parse", ref) != sha {
					t.Fatal("new working snapshot not returned")
				}
			}
		})
	}
}

func parkSequenceRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertCleanParkSequence(t *testing.T, calls []parkCommandObservation, p GitProvider, root, ref, head string) {
	t.Helper()
	config := []string{"-c", "core.fsmonitor=false", "-c", "core.ignorestat=false", "-c", "core.sparsecheckout=false", "-c", "index.sparse=false"}
	want := [][]string{
		{"check-ref-format", ref}, {"rev-parse", "HEAD"},
		append(slices.Clone(config), "read-tree", head),
		append(slices.Clone(config), "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none"),
		{"diff-index", "--cached", "--quiet", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", head, "--"},
		{"merge-base", "--is-ancestor", head, "HEAD"},
	}
	if len(calls) != len(want) {
		t.Fatalf("clean Park calls=%d, want %d", len(calls), len(want))
	}
	baseEnv := []string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
	probeEnv := calls[2].command.Environment
	if len(probeEnv) != 3 || !slices.Equal(probeEnv[:2], baseEnv) || !strings.HasPrefix(probeEnv[2], "GIT_INDEX_FILE=") || filepath.Base(strings.TrimPrefix(probeEnv[2], "GIT_INDEX_FILE=")) != "clean-index" {
		t.Fatalf("independent clean proof index environment=%q", probeEnv)
	}
	for i, call := range calls {
		dir, env := root, baseEnv
		switch i {
		case 0, 5:
			dir = p.Root
		case 2, 3:
			env = probeEnv
		}
		if !slices.Equal(call.command.Args, want[i]) || call.command.Directory != dir || !slices.Equal(call.command.Environment, env) || len(call.command.Stdin) != 0 {
			t.Errorf("command%d mismatch: %+v; want argv=%q dir=%q env=%q", i+1, call.command, want[i], dir, env)
		}
		if call.err != nil || call.result.ExitCode != 0 || call.result.StdoutTruncated || call.result.StderrTruncated {
			t.Errorf("command%d failed: %+v", i+1, call)
		}
	}
}
