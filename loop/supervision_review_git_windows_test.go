//go:build windows

package loop

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/worktree"
)

func TestSupervisionReviewWindowsRepositoryBoundary(t *testing.T) {
	for _, name := range []string{"supported", "long", "missing"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var opts SupervisionOptions
			var spec string
			if name == "long" {
				base := tempDir(t)
				padding := 148 - len(base) - 1
				if padding < 1 {
					t.Fatal("fixture parent too long", len(base))
				}
				root := filepath.Join(base, strings.Repeat("w", padding))
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Fatal(err)
				}
				store, err := journal.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				opts = SupervisionOptions{Workspace: root, Delivery: "delivery-one", CursorPath: filepath.Join(root, "observer.json"), Now: time.Now}
				opts, _, spec = initializeSupervisionReviewFixture(t, store, opts)
			} else {
				opts, _, spec = supervisionReviewFixture(t)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			git := func(root string, args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = root
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("Git at %s %q: %v %s", root, args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			read := func(path string) []byte {
				t.Helper()
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			records, err := readSupervisionRecords(opts)
			if err != nil {
				t.Fatal(err)
			}
			job := supervisionReviewCandidateInWorkspace(opts.Workspace, opts.Delivery, records)
			if job == nil || job.ID == "" {
				t.Fatal("actual completed review candidate missing", job)
			}
			directory := supervisionReviewDirectory(opts, *job)
			job.Snapshot = filepath.Join(directory, "source")
			identity, err := hex.DecodeString(job.ID)
			if err != nil || len(identity) != 32 {
				t.Fatal("invalid fixture job identity", job.ID, err)
			}
			metadataName := "git-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(identity))
			metadata := filepath.Join(opts.Workspace, ".batuta", "reviews", "supervision", metadataName)
			if name == "long" {
				if len(opts.Workspace) != 148 || len(job.Snapshot) != 248 || len(metadata) != 233 || len(filepath.Join(metadata, "objects", "info", "alternates")) != 257 {
					t.Fatal("production bootstrap boundary differs", len(opts.Workspace), len(job.Snapshot), len(metadata))
				}
				t.Log("production boundary: workspace148 source248 metadata233 alternates257")
			}
			job.Spec = filepath.Join(directory, job.Slug+".md")
			if err := os.MkdirAll(filepath.Dir(job.Snapshot), 0700); err != nil {
				t.Fatal(err)
			}
			provider, err := worktree.New(ctx, opts.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			if err := provider.EnsureExcluded(ctx); err != nil {
				t.Fatal(err)
			}
			legacyExclude := string(read(filepath.Join(opts.Workspace, ".git", "info", "exclude")))
			if !strings.Contains(legacyExclude, ".batuta/reviews/supervision/\n") || strings.Contains(legacyExclude, ".batuta/reviews/git/") {
				t.Fatal("fixture must retain the published55.1 exclusion set", legacyExclude)
			}
			status := git(opts.Workspace, "status", "--porcelain=v1", "--untracked-files=all")
			if status != "" {
				t.Fatal("fixture original workspace is not clean", status)
			}
			branch := git(opts.Workspace, "symbolic-ref", "HEAD")
			head := git(opts.Workspace, "rev-parse", "HEAD")
			originals := map[string][]byte{}
			for _, path := range []string{".git/index", ".git/config", ".git/info/exclude", "source.txt", ".batuta/plans/done/delivery.md"} {
				originals[path] = read(filepath.Join(opts.Workspace, path))
			}
			defer func() {
				if after := git(opts.Workspace, "status", "--porcelain=v1", "--untracked-files=all"); after != status {
					t.Error("review preparation dirtied original workspace", after)
				}
				if after := git(opts.Workspace, "symbolic-ref", "HEAD"); after != branch {
					t.Error("original branch changed", after, branch)
				}
				if after := git(opts.Workspace, "rev-parse", "HEAD"); after != head {
					t.Error("original HEAD changed", after, head)
				}
				for path, before := range originals {
					if !bytes.Equal(before, read(filepath.Join(opts.Workspace, path))) {
						t.Error("original file changed", path)
					}
				}
			}()
			if name == "missing" {
				if err := os.MkdirAll(job.Snapshot, 0700); err != nil {
					t.Fatal(err)
				}
				_, err := supervisionReviewGit(ctx, job.Snapshot, "checkout", "--detach", job.FinalCommit, "--")
				if err == nil || !strings.Contains(err.Error(), "not a git repository") {
					t.Fatal("missing local repository must reject checkout before ancestor mutation", err)
				}
				return
			}
			state := filepath.Join(job.Snapshot, ".batuta", "reviews", "state", "probe.json")
			for attempt := 0; attempt < 2; attempt++ {
				if err := prepareSupervisionReviewSnapshot(ctx, opts, job, nil); err != nil {
					t.Fatal("snapshot preparation", attempt, err)
				}
				if info, err := os.Lstat(filepath.Join(job.Snapshot, ".git")); err != nil || !info.Mode().IsRegular() {
					t.Fatal("Windows snapshot must use its private metadata gitfile", err)
				}
				actualMetadata := git(job.Snapshot, "rev-parse", "--absolute-git-dir")
				if !strings.EqualFold(filepath.Clean(filepath.FromSlash(actualMetadata)), filepath.Clean(metadata)) {
					t.Fatal("metadata escaped private review directory", actualMetadata, metadata)
				}
				top := git(job.Snapshot, "rev-parse", "--show-toplevel")
				if !strings.EqualFold(filepath.Clean(filepath.FromSlash(top)), filepath.Clean(job.Snapshot)) {
					t.Fatal("snapshot selected another repository", top)
				}
				if got := git(job.Snapshot, "rev-parse", "HEAD"); got != job.FinalCommit {
					t.Fatal("snapshot HEAD changed", got)
				}
				if got := git(job.Snapshot, "config", "--local", "--get", "core.longpaths"); got != "true" {
					t.Fatal("ordinary review Git lacks clone-local long paths", got)
				}
				if got := read(filepath.Join(job.Snapshot, "source.txt")); string(got) != "delivered\n" {
					t.Fatal("actual delivered source missing", string(got))
				}
				if got := read(filepath.Join(job.Snapshot, ".batuta/plans/done/delivery.md")); string(got) != spec {
					t.Fatal("nested delivered plan differs", string(got))
				}
				if got := read(job.Spec); string(got) != spec {
					t.Fatal("original review spec differs", string(got))
				}
				if got := git(job.Snapshot, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
					t.Fatal("ordinary Git cannot verify the clean delivered snapshot", got)
				}
				git(job.Snapshot, "diff", "--exit-code", "--")
				if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("prelaunch rebuild reused incremental review state", attempt, err)
				}
				if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(state, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if got := git(job.Snapshot, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
					t.Fatal("engine state exclusion not in actual metadata", got)
				}
				if err := validateSupervisionReviewSnapshot(ctx, job); err != nil {
					t.Fatal("snapshot validation", err)
				}
			}
		})
	}
}
