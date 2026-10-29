//go:build windows

package loop

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSupervisionReviewWindowsRepositoryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name        string
		unsupported bool
	}{{"supported", false}, {"long", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts, _, _ := supervisionReviewFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = opts.Workspace
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("fixture Git %q: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			records, err := readSupervisionRecords(opts)
			if err != nil {
				t.Fatal(err)
			}
			job := supervisionReviewCandidateInWorkspace(opts.Workspace, opts.Delivery, records)
			if job == nil || job.ID == "" {
				t.Fatal("actual completed review candidate missing", job)
			}
			job.Snapshot = filepath.Join(opts.Workspace, "snapshot")
			if tc.unsupported {
				minimum := filepath.Join(opts.Workspace, "nested", "source")
				padding := 248 - len(minimum) - 1
				if padding < 1 {
					t.Fatal("fixture parent too long", len(minimum))
				}
				job.Snapshot = filepath.Join(opts.Workspace, "nested", strings.Repeat("d", padding), "source")
				if len(job.Snapshot) != 248 {
					t.Fatal("actual failure length mismatch", len(job.Snapshot))
				}
			}
			job.Spec = filepath.Join(opts.Workspace, "review-spec.md")
			if err := os.MkdirAll(filepath.Dir(job.Snapshot), 0700); err != nil {
				t.Fatal(err)
			}
			branch, head := git("symbolic-ref", "HEAD"), git("rev-parse", "HEAD")
			index, err := os.ReadFile(filepath.Join(opts.Workspace, ".git", "index"))
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(opts.Workspace, "source.txt"))
			if err != nil {
				t.Fatal(err)
			}
			prepareErr := prepareSupervisionReviewSnapshot(ctx, opts, job, nil)
			if tc.unsupported {
				// The clone exists. A rejected checkout must not act on its ancestor.
				info, err := os.Stat(filepath.Join(job.Snapshot, ".git", "HEAD"))
				if err != nil || !info.Mode().IsRegular() {
					t.Fatal("actual clone HEAD missing", err)
				}
				if prepareErr == nil || !strings.Contains(prepareErr.Error(), "checkout failed") || !strings.Contains(prepareErr.Error(), "not a git repository") {
					t.Errorf("long snapshot must reject its local repository before checkout: %v", prepareErr)
				}
			} else if prepareErr != nil {
				t.Errorf("supported snapshot preparation: %v", prepareErr)
			}
			branchCmd := exec.CommandContext(ctx, "git", "symbolic-ref", "-q", "HEAD")
			branchCmd.Dir = opts.Workspace
			afterBranch, branchErr := branchCmd.CombinedOutput()
			if branchErr != nil || strings.TrimSpace(string(afterBranch)) != branch {
				t.Errorf("original branch changed: %q/%v, want %q", afterBranch, branchErr, branch)
			}
			if after := git("rev-parse", "HEAD"); after != head {
				t.Errorf("original HEAD changed: %q, want %q", after, head)
			}
			afterIndex, err := os.ReadFile(filepath.Join(opts.Workspace, ".git", "index"))
			if err != nil || sha256.Sum256(afterIndex) != sha256.Sum256(index) {
				t.Error("original index changed", err)
			}
			after, err := os.ReadFile(filepath.Join(opts.Workspace, "source.txt"))
			if err != nil || string(after) != string(before) {
				t.Error("original tracked bytes changed", err)
			}
			if !tc.unsupported {
				actual, err := os.ReadFile(filepath.Join(job.Snapshot, "source.txt"))
				if err != nil || string(actual) != "delivered\n" {
					t.Errorf("actual snapshot source unavailable: %q %v", actual, err)
				}
				top, err := supervisionReviewGit(ctx, job.Snapshot, "rev-parse", "--show-toplevel")
				if err != nil || !strings.EqualFold(filepath.Clean(filepath.FromSlash(strings.TrimSpace(string(top)))), filepath.Clean(job.Snapshot)) {
					t.Errorf("supported snapshot selected another root: %q %v", top, err)
				}
			}
		})
	}
}
