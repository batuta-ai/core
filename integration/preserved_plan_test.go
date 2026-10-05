package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/publication"
)

const preservedTestPlan = ".batuta/plans/demo.md"

func TestGitClientPreservedPlanCandidateAcceptsNativeGitRoot(t *testing.T) {
	t.Parallel()
	f := preservedPlanFixture(t)
	root, branch, commit := f.candidate(t, "task_01", "product.txt", "product\n")
	gitRoot := strings.TrimSpace(f.run(t, root, "rev-parse", "--show-toplevel"))
	verification := []byte(`{"status":"passed","task_id":"task_01"}`)
	request := CandidateRequest{
		TaskID: "task_01", Slug: "demo", WorktreeRoot: root, RepositoryRoot: f.root,
		ExpectedBranch: branch, BaseSHA: f.base,
		Verification: verification, VerificationDigest: integrationDigest(verification),
	}
	client := GitClient{Executable: f.git, Runner: publication.ExecRunner{}}
	evidence, err := client.Candidate(context.Background(), request)
	if err != nil || evidence.WorktreeRoot != root || evidence.CommitSHA != commit {
		t.Fatalf("native root %q, Git root %q: evidence=%+v error=%v", root, gitRoot, evidence, err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	request.WorktreeRoot = nested
	if _, err := client.Candidate(context.Background(), request); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("nested directory accepted as worktree root: %v", err)
	}
}

func preservedPlanFixture(t *testing.T) *integrationGitFixture {
	t.Helper()
	f := newIntegrationGitFixture(t)
	f.run(t, f.root, "config", "core.autocrlf", "false")
	writeIntegrationFile(t, filepath.Join(f.root, preservedTestPlan), "original plan\r\n")
	f.run(t, f.root, "add", preservedTestPlan)
	f.run(t, f.root, "commit", "-qm", "test: original plan")
	f.base = strings.TrimSpace(f.run(t, f.root, "rev-parse", "HEAD"))
	return f
}

func preservedPreflight(f *integrationGitFixture, candidate CandidateEvidence) PreflightRequest {
	return PreflightRequest{OperationID: integrationDigest([]byte("preserve")), RequestDigest: integrationDigest([]byte("preserve request")), IntegrationRoot: f.root, StartingHeadSHA: f.base, Candidates: []CandidateEvidence{candidate}}
}

func preservedApply(f *integrationGitFixture, p PreflightResult) ApplyRequest {
	return ApplyRequest{OperationID: p.OperationID, RequestDigest: p.RequestDigest, IntegrationRoot: f.root, ExpectedHeadSHA: p.StartingHeadSHA, TaskID: p.AcceptedTaskIDs[0], CandidateCommitSHA: p.AcceptedCommitSHAs[0], ExpectedResultTreeSHA: p.AcceptedResultTreeSHAs[0], ExpectedResultCommitSHA: p.AcceptedResultCommitSHAs[0]}
}

func TestGitClientPreservedPlanSurvivesIntegration(t *testing.T) {
	for _, kind := range []string{"edited", "deleted", "untracked", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := preservedPlanFixture(t)
			path := preservedTestPlan
			if kind == "legacy" {
				path = ".batuta/plan-demo.md"
				f.run(t, f.root, "mv", preservedTestPlan, path)
				f.run(t, f.root, "commit", "-qm", "test: legacy plan")
				f.base = strings.TrimSpace(f.run(t, f.root, "rev-parse", "HEAD"))
			}
			if kind == "untracked" {
				f.run(t, f.root, "rm", "--cached", path)
				f.run(t, f.root, "commit", "-qm", "test: plan outside Git")
				f.base = strings.TrimSpace(f.run(t, f.root, "rev-parse", "HEAD"))
			}
			candidate := f.candidateEvidence(t, "task_01", "product.txt", "product\n")
			source := filepath.Join(f.root, filepath.FromSlash(path))
			external := []byte("external proposal\r\n")
			if kind == "deleted" {
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(source, external, 0600); err != nil {
				t.Fatal(err)
			}
			var mode os.FileMode
			if info, err := os.Stat(source); err == nil {
				mode = info.Mode()
			}
			client := GitClient{Executable: f.git, Runner: publication.ExecRunner{}, PreservedPlanPath: path}
			preflight, err := client.Preflight(context.Background(), preservedPreflight(f, candidate))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Apply(context.Background(), preservedApply(f, preflight)); err != nil {
				t.Fatal(err)
			}
			result, err := client.Reconcile(context.Background(), ReconcileRequest{IntegrationRoot: f.root, Preflight: preflight})
			if err != nil || len(result.AcceptedTaskIDs) != 1 {
				t.Fatalf("reconcile=%+v %v", result, err)
			}
			if kind == "deleted" {
				if _, err := os.Stat(source); !os.IsNotExist(err) {
					t.Fatal("deleted source recreated", err)
				}
			} else {
				body, err := os.ReadFile(source)
				if err != nil || !bytes.Equal(body, external) {
					t.Fatalf("source changed: %q %v", body, err)
				}
				info, err := os.Stat(source)
				if err != nil || info.Mode() != mode {
					t.Fatal("source mode changed", err)
				}
			}
			if got := f.run(t, f.root, "diff", "--cached", "--name-only"); got != "" {
				t.Fatal("dirty index", got)
			}
		})
	}
}

func TestGitClientPreservedPlanRejectsForeignState(t *testing.T) {
	for _, kind := range []string{"staged plan", "staged foreign", "foreign", "invalid path", "candidate plan"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := preservedPlanFixture(t)
			candidate := f.candidateEvidence(t, "task_01", "product.txt", "product\n")
			client := GitClient{Executable: f.git, Runner: publication.ExecRunner{}, PreservedPlanPath: preservedTestPlan}
			switch kind {
			case "staged plan":
				writeIntegrationFile(t, filepath.Join(f.root, preservedTestPlan), "external\n")
				f.run(t, f.root, "add", preservedTestPlan)
			case "staged foreign":
				writeIntegrationFile(t, filepath.Join(f.root, "foreign.txt"), "foreign\n")
				f.run(t, f.root, "add", "foreign.txt")
			case "foreign":
				writeIntegrationFile(t, filepath.Join(f.root, "foreign.txt"), "foreign\n")
			case "invalid path":
				client.PreservedPlanPath = ".batuta/plans/../plans/demo.md"
			case "candidate plan":
				writeIntegrationFile(t, filepath.Join(candidate.WorktreeRoot, preservedTestPlan), "worker replacement\n")
				f.run(t, candidate.WorktreeRoot, "add", preservedTestPlan)
				f.run(t, candidate.WorktreeRoot, "commit", "--amend", "--no-edit", "-q")
				candidate.CommitSHA = strings.TrimSpace(f.run(t, candidate.WorktreeRoot, "rev-parse", "HEAD"))
				candidate.TreeSHA = strings.TrimSpace(f.run(t, candidate.WorktreeRoot, "rev-parse", "HEAD^{tree}"))
			}
			before := f.run(t, f.root, "status", "--porcelain=v1", "-z")
			if _, err := client.Preflight(context.Background(), preservedPreflight(f, candidate)); err == nil {
				t.Fatal("unsafe state accepted")
			}
			if head := strings.TrimSpace(f.run(t, f.root, "rev-parse", "HEAD")); head != f.base {
				t.Fatal("refused operation moved HEAD")
			}
			if after := f.run(t, f.root, "status", "--porcelain=v1", "-z"); after != before {
				t.Fatal("refused operation changed state")
			}
		})
	}
}

func TestGitClientPreservedPlanSurvivesFailedApplyCleanup(t *testing.T) {
	t.Parallel()
	f := preservedPlanFixture(t)
	candidate := f.candidateEvidence(t, "task_01", "product.txt", "product\n")
	client := GitClient{Executable: f.git, Runner: publication.ExecRunner{}, PreservedPlanPath: preservedTestPlan}
	preflight, err := client.Preflight(context.Background(), preservedPreflight(f, candidate))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(f.root, preservedTestPlan)
	external := []byte("external edit after preflight\r\n")
	if err := os.WriteFile(source, external, 0600); err != nil {
		t.Fatal(err)
	}
	client.Runner = preservedRunnerFunc(func(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
		result, err := (publication.ExecRunner{}).Run(ctx, c)
		if err == nil && c.Directory == f.root && len(c.Args) > 0 && c.Args[0] == "cherry-pick" && c.Args[1] == "--no-commit" {
			return result, errors.New("injected failure after cherry-pick")
		}
		return result, err
	})
	if _, err := client.Apply(context.Background(), preservedApply(f, preflight)); err == nil {
		t.Fatal("injected failure accepted")
	}
	body, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(body, external) {
		t.Fatalf("failure cleanup changed source: %q %v", body, err)
	}
	if index := f.run(t, f.root, "diff", "--cached", "--name-only"); index != "" {
		t.Fatal("failed apply left index", index)
	}
	if head := strings.TrimSpace(f.run(t, f.root, "rev-parse", "HEAD")); head != f.base {
		t.Fatal("failed apply moved HEAD")
	}
}

type preservedRunnerFunc func(context.Context, publication.Command) (publication.CommandResult, error)

func (f preservedRunnerFunc) Run(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
	return f(ctx, c)
}

func TestGitClientPreservedPlanApplyRejectsStateChangedAfterPreflight(t *testing.T) {
	for _, kind := range []string{"staged plan", "foreign", "candidate plan"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := preservedPlanFixture(t)
			candidate := f.candidateEvidence(t, "task_01", "product.txt", "product\n")
			client := GitClient{Executable: f.git, Runner: publication.ExecRunner{}, PreservedPlanPath: preservedTestPlan}
			preflight, err := client.Preflight(context.Background(), preservedPreflight(f, candidate))
			if err != nil {
				t.Fatal(err)
			}
			request := preservedApply(f, preflight)
			switch kind {
			case "staged plan":
				writeIntegrationFile(t, filepath.Join(f.root, preservedTestPlan), "staged external\n")
				f.run(t, f.root, "add", preservedTestPlan)
			case "foreign":
				writeIntegrationFile(t, filepath.Join(f.root, "foreign.txt"), "external\n")
			case "candidate plan":
				writeIntegrationFile(t, filepath.Join(candidate.WorktreeRoot, preservedTestPlan), "changed after preflight\n")
				f.run(t, candidate.WorktreeRoot, "add", preservedTestPlan)
				f.run(t, candidate.WorktreeRoot, "commit", "--amend", "--no-edit", "-q")
				request.CandidateCommitSHA = strings.TrimSpace(f.run(t, candidate.WorktreeRoot, "rev-parse", "HEAD"))
			}
			before := f.run(t, f.root, "status", "--porcelain=v1", "-z")
			if _, err := client.Apply(context.Background(), request); err == nil {
				t.Fatal("unsafe apply accepted")
			}
			if head := strings.TrimSpace(f.run(t, f.root, "rev-parse", "HEAD")); head != f.base {
				t.Fatal("refused apply moved HEAD")
			}
			if after := f.run(t, f.root, "status", "--porcelain=v1", "-z"); after != before {
				t.Fatal("refused apply changed state")
			}
		})
	}
}
