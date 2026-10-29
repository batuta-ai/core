package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
)

type cancelCommandObservation struct {
	command publication.Command
	result  publication.CommandResult
	err     error
	counts  map[journal.Kind]int
}

func TestCanceledRunSnapshotCommandSequence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, shape, fault string
		calls, snapshots   int
	}{
		{name: "supervised_clean", shape: "clean", calls: 29},
		{name: "supervised_dirty_staged_working", shape: "dirty", calls: 37, snapshots: 1},
		{name: "supervised_prior_ref", shape: "prior_ref", calls: 31},
		{name: "supervised_ref_lost_before_caller_revalidation", shape: "prior_ref", fault: "ref_loss"},
		{name: "supervised_git_error_before_failure", shape: "dirty", fault: "first_read_tree"},
		{name: "supervised_git_error_before_terminal", shape: "dirty", fault: "second_read_tree", snapshots: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, approved := approvedSnapshotFixture(t)
			var out bytes.Buffer
			opts := snapshotTestOptions(f, approved, "default", &out)
			opts.Parallel, opts.KeepWorktrees = 1, true
			opts.Supervision, opts.Supervisor = true, &SuperviseOptions{}
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var r *Runner
			var mu sync.Mutex
			var calls []cancelCommandObservation
			var wt fixture
			var index, rootIndex []byte
			var indexPath, rootIndexPath, head, branch, ref, previous string
			started, removed, injected, passes := false, false, false, 0
			opts.Runner = commandRunnerFunc(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
				mu.Lock()
				defer mu.Unlock()
				if !started && command.Executable == f.fake && slices.Contains(command.Args, "run") && !strings.Contains(strings.Join(command.Args, " "), executor.CapabilityMarker) {
					wt = f
					wt.root = command.Directory
					branch = wt.run(t, "symbolic-ref", "--short", "HEAD")
					ref = "refs/batuta/parked/" + strings.TrimPrefix(branch, "batuta/")
					switch tc.shape {
					case "dirty":
						cancelSequenceWrite(t, filepath.Join(wt.root, "pending.txt"), []byte("staged\n"))
						wt.run(t, "add", "pending.txt")
						cancelSequenceWrite(t, filepath.Join(wt.root, "pending.txt"), []byte("working\n"))
					case "prior_ref":
						cancelSequenceWrite(t, filepath.Join(wt.root, "pending.txt"), []byte("committed\n"))
						wt.run(t, "add", "pending.txt")
						wt.run(t, "commit", "-qm", "wip: retained executor work")
						plain := r.git
						plain.Runner = publication.ExecRunner{}
						var err error
						previous, err = plain.Park(context.Background(), wt.root, ref, "wip: prior protection")
						if err != nil || previous == "" {
							t.Fatalf("prepare recovery ref = %q, %v", previous, err)
						}
					}
					head = wt.run(t, "rev-parse", "HEAD")
					indexPath = wt.run(t, "rev-parse", "--git-path", "index")
					if !filepath.IsAbs(indexPath) {
						indexPath = filepath.Join(wt.root, indexPath)
					}
					rootIndexPath = filepath.Join(f.root, ".git", "index")
					index, rootIndex = cancelSequenceRead(t, indexPath), cancelSequenceRead(t, rootIndexPath)
					started = true
					cancel()
					result, err := (publication.ExecRunner{}).Run(ctx, command)
					t.Logf("cancellation boundary: synthetic executor run; argv=%q dir=%q exit=%d err=%v", command.Args, command.Directory, result.ExitCode, err)
					return result, err
				}
				if !started || command.Executable != f.git {
					return (publication.ExecRunner{}).Run(ctx, command)
				}
				if slices.Equal(command.Args, []string{"check-ref-format", ref}) {
					passes++
				}
				if !injected && slices.Equal(command.Args, []string{"read-tree", head}) &&
					(tc.fault == "first_read_tree" && passes == 1 || tc.fault == "second_read_tree" && passes == 2) {
					injected = true
					command.Args = []string{"read-tree", "refs/heads/missing-cancel-sequence-object"}
				}
				counts := kinds(readJournal(t, f, r.Delivery()))
				result, err := (publication.ExecRunner{}).Run(ctx, command)
				command.Args = slices.Clone(command.Args)
				command.Environment = slices.Clone(command.Environment)
				command.Stdin = bytes.Clone(command.Stdin)
				calls = append(calls, cancelCommandObservation{command, result, err, counts})
				// This boundary is Park's successful ancestry observation, before its caller revalidates.
				if tc.fault == "ref_loss" && !removed && err == nil && slices.Equal(command.Args, []string{"merge-base", "--is-ancestor", head, previous}) {
					removed = true
					f.run(t, "update-ref", "-d", ref)
				}
				return result, err
			})
			var err error
			r, err = New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(f.root, filepath.FromSlash(approved.Path))
			external := bytes.Replace(approved.Content, []byte("Original context."), []byte("External proposal."), 1)
			cancelSequenceWrite(t, source, external)
			state, runErr := r.Run(runCtx)
			if !started {
				t.Fatalf("executor cancellation boundary not reached: %s, %v\n%s", state, runErr, &out)
			}
			missing, unexpected := 0, 0
			for i, call := range calls {
				classification := "success"
				if call.err != nil {
					switch {
					case call.command.Args[0] == "show-ref" && (call.result.ExitCode == 1 || call.result.ExitCode == 128):
						classification = "expected missing ref"
						missing++
					case call.command.Args[0] == "merge-base" && call.result.ExitCode == 1:
						classification = "expected non-ancestor"
					case injected && slices.Equal(call.command.Args, []string{"read-tree", "refs/heads/missing-cancel-sequence-object"}):
						classification = "injected real Git failure"
					default:
						unexpected++
					}
				}
				t.Logf("cancel Git %02d argv=%q dir=%q env=%q stdin=%q exit=%d stdout=%q stderr=%q err=%v (%s) journal[snapshot=%d failure=%d terminal=%d]", i+1, call.command.Args, call.command.Directory, call.command.Environment, call.command.Stdin, call.result.ExitCode, call.result.Stdout, call.result.Stderr, call.err, classification, call.counts[KindSnapshot], call.counts[KindFailure], call.counts[KindTerminal])
				if call.counts[KindTerminal] != 0 || call.result.StdoutTruncated || call.result.StderrTruncated {
					t.Errorf("postterminal or truncated Git command %d", i+1)
				}
			}
			t.Logf("shape=%s fault=%q passes=%d calls=%d missing-ref=%d unexpected-Git-errors=%d Run=%q err=%v", tc.shape, tc.fault, passes, len(calls), missing, unexpected, state, runErr)
			if unexpected != 0 {
				t.Fatal("unexpected Git error")
			}
			if !bytes.Equal(cancelSequenceRead(t, indexPath), index) || !bytes.Equal(cancelSequenceRead(t, rootIndexPath), rootIndex) {
				t.Fatal("cancellation changed worktree or integration index bytes")
			}
			if wt.run(t, "rev-parse", "HEAD") != head || f.run(t, "rev-parse", "refs/heads/"+branch) != head || f.run(t, "rev-parse", "HEAD") != f.base {
				t.Fatal("cancellation moved HEAD or branch")
			}
			if _, err := os.Stat(wt.root); err != nil {
				t.Fatalf("retained worktree missing: %v", err)
			}
			if tc.shape != "clean" {
				want := "working\n"
				if tc.shape == "prior_ref" {
					want = "committed\n"
				}
				if got := string(cancelSequenceRead(t, filepath.Join(wt.root, "pending.txt"))); got != want {
					t.Fatalf("lost pending data: %q", got)
				}
			}
			if !bytes.Equal(cancelSequenceRead(t, source), external) || !bytes.Equal(approved.Content, []byte(approvedSnapshotSource)) {
				t.Fatal("cancellation changed external or approved plan bytes")
			}
			records := readJournal(t, f, r.Delivery())
			counts := kinds(records)
			for _, record := range records {
				if record.Kind == KindSnapshot || record.Kind == KindFailure || record.Kind == KindTerminal || record.Kind == KindInterrupted {
					t.Logf("journal seq=%d kind=%s task=%q detail=%s", record.Seq, record.Kind, record.TaskID, record.Detail)
				}
			}
			var opened openedDetail
			if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
				t.Fatal(err)
			}
			if !opened.Supervision || opened.Parallel != 1 || opened.ApprovalReceiptID != approved.ReceiptID || opened.PlanContentDigest != approved.ContentDigest || opened.PlanPath != approved.Path || opened.Workspace != f.root {
				t.Fatalf("wrong supervised approved opening: %+v", opened)
			}
			if counts[KindSnapshot] != tc.snapshots {
				t.Fatalf("snapshot records=%d, want %d", counts[KindSnapshot], tc.snapshots)
			}
			if tc.fault != "" {
				wantError, wantFailures := "missing-cancel-sequence-object", 0
				if tc.fault == "ref_loss" {
					wantError = "unprotected"
					if !removed || f.run(t, "for-each-ref", "--format=%(refname)", ref) != "" {
						t.Fatal("ref-loss boundary did not remove the observed protection")
					}
				} else if !injected {
					t.Fatal("Git error boundary not reached")
				}
				if tc.fault == "second_read_tree" {
					wantFailures = 1
				}
				cancelSequenceErrors(t, runErr, wantError)
				if counts[KindTerminal] != 0 || counts[KindFailure] != wantFailures || r.terminal != "" {
					t.Fatalf("snapshot failure crossed journal boundary: terminal=%d failure=%d state=%q", counts[KindTerminal], counts[KindFailure], r.terminal)
				}
				if tc.fault == "ref_loss" {
					last := calls[len(calls)-1]
					if last.command.Args[0] != "for-each-ref" || len(last.result.Stdout) != 0 {
						t.Fatal("caller did not observe missing protection after Park")
					}
				}
				return
			}
			cancelSequenceErrors(t, runErr, "")
			if state != StateCanceled || r.terminal != StateCanceled || counts[KindTerminal] != 1 || counts[KindFailure] != 1 || passes != 2 || len(calls) != tc.calls {
				t.Fatalf("cancellation: state=%q terminal=%q counts=%v passes=%d commands=%d; want %d commands", state, r.terminal, counts, passes, len(calls), tc.calls)
			}
			assertCancelSequenceBoundaries(t, calls, records, tc.shape, tc.snapshots)
			plain := r.git
			plain.Runner = publication.ExecRunner{}
			refs, err := plain.Parked(context.Background(), "greetings")
			wantRefs := map[string]string{}
			if tc.shape == "dirty" {
				wantRefs[ref], wantRefs[ref+"-index"] = "working\n", "staged\n"
			} else if tc.shape == "prior_ref" {
				wantRefs[ref] = "committed\n"
			}
			if err != nil || len(refs) != len(wantRefs) {
				t.Fatalf("recovery refs=%v, %v; want %d", refs, err, len(wantRefs))
			}
			for _, saved := range refs {
				want, ok := wantRefs[saved.Ref]
				if !ok || string(plain.Show(context.Background(), wt.root, saved.Ref, "pending.txt")) != want {
					t.Fatalf("lost recovery data at %s", saved.Ref)
				}
				if tc.shape == "prior_ref" && saved.SHA != previous {
					t.Fatal("prior recovery ref changed")
				}
				if ancestor, err := plain.IsAncestor(context.Background(), head, saved.SHA); err != nil || !ancestor {
					t.Fatalf("recovery ref lost HEAD: %t, %v", ancestor, err)
				}
			}
			var terminal terminalDetail
			if err := json.Unmarshal(records[len(records)-1].Detail, &terminal); err != nil {
				t.Fatal(err)
			}
			if terminal.State != StateCanceled || len(terminal.Deletions) != 0 || terminal.CleanupPending || terminal.BookkeepingPending || len(terminal.Summary.Parked) != len(refs) {
				t.Fatalf("incorrect canceled terminal: %+v", terminal)
			}
			for i := range refs {
				if terminal.Summary.Parked[i].Ref != refs[i].Ref || terminal.Summary.Parked[i].SHA != refs[i].SHA {
					t.Fatal("terminal recovery inventory differs from real refs")
				}
			}
			assertCancelApprovedResume(t, f, r, approved, external)
		})
	}
}

func cancelSequenceRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func cancelSequenceWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o640); err != nil {
		t.Fatal(err)
	}
}

// Inspect every joined branch: errors.Is alone would hide a snapshot error beside cancellation.
func cancelSequenceErrors(t *testing.T, err error, wantFailure string) {
	t.Helper()
	canceled, failed := 0, 0
	var visit func(error)
	visit = func(err error) {
		if err == nil {
			return
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}
		} else if wrapped, ok := err.(interface{ Unwrap() error }); ok {
			visit(wrapped.Unwrap())
		} else if err == context.Canceled {
			canceled++
		} else if wantFailure != "" && strings.Contains(err.Error(), wantFailure) {
			failed++
		} else {
			t.Errorf("unexpected returned error leaf: %v", err)
		}
	}
	visit(err)
	// Git wraps its process error with the argv/stderr diagnostic at an outer layer.
	if wantFailure != "" && strings.Contains(wantFailure, "missing-") {
		if err == nil || !strings.Contains(err.Error(), wantFailure) {
			t.Errorf("missing Git diagnostic %q: %v", wantFailure, err)
		}
	}
	if canceled == 0 || wantFailure != "" && failed == 0 || wantFailure == "" && failed != 0 {
		t.Errorf("returned errors: cancellations=%d failures=%d want failure=%q: %v", canceled, failed, wantFailure, err)
	}
}

func assertCancelSequenceBoundaries(t *testing.T, calls []cancelCommandObservation, records []journal.Record, shape string, snapshots int) {
	t.Helper()
	var starts []int
	for i, call := range calls {
		if call.command.Args[0] == "check-ref-format" {
			starts = append(starts, i)
		}
	}
	if len(starts) != 2 || starts[0] != 0 {
		t.Fatalf("snapshot starts = %v", starts)
	}
	first, second, last := calls[0], calls[starts[1]], calls[len(calls)-1]
	if first.counts[KindSnapshot] != 0 || first.counts[KindFailure] != 0 || second.counts[KindSnapshot] != snapshots || second.counts[KindFailure] != 1 || last.counts[KindSnapshot] != snapshots || last.counts[KindFailure] != 1 || last.command.Args[0] != "for-each-ref" {
		t.Fatalf("wrong snapshot/failure/final listing boundaries: first=%v second=%v last=%v argv=%q", first.counts, second.counts, last.counts, last.command.Args)
	}
	if shape == "clean" {
		for pass := range 2 {
			segment := calls[starts[pass] : starts[pass]+14]
			want := []string{"check-ref-format", "rev-parse", "read-tree", "add", "write-tree", "rev-parse", "ls-files", "write-tree", "show-ref", "rev-parse", "rev-parse", "merge-base", "rev-parse", "merge-base"}
			for i := range segment {
				if segment[i].command.Args[0] != want[i] {
					t.Errorf("clean pass=%d command=%d argv=%q want=%s", pass+1, i+1, segment[i].command.Args, want[i])
				}
			}
			if !slices.Equal(segment[12].command.Args, []string{"rev-parse", "HEAD"}) || !strings.HasPrefix(segment[13].command.Args[3], "refs/heads/") {
				t.Fatal("missing caller HEAD/integration revalidation")
			}
		}
	}
	for _, record := range records {
		if record.Kind == KindFailure {
			var detail struct {
				Blocker   string `json:"blocker"`
				Status    string `json:"status"`
				Execution int    `json:"execution"`
			}
			if err := json.Unmarshal(record.Detail, &detail); err != nil || record.TaskID != "task_1" || detail.Blocker != blockerInterrupted || detail.Status != "stalled" || detail.Execution != 1 {
				t.Fatalf("incorrect interruption failure: %s, %v", record.Detail, err)
			}
			for _, snapshot := range records {
				if snapshot.Kind == KindSnapshot && snapshot.Seq >= record.Seq {
					t.Fatal("failure preceded recovery snapshot")
				}
			}
		}
	}
	if records[len(records)-1].Kind != KindTerminal {
		t.Fatal("terminal is not the last journal record")
	}
	t.Logf("snapshot command spans: first=%d second=%d final-list=1", starts[1], len(calls)-starts[1]-1)
}

func assertCancelApprovedResume(t *testing.T, f fixture, r *Runner, approved *ApprovedPlanSnapshot, external []byte) {
	t.Helper()
	var out bytes.Buffer
	resume := snapshotTestOptions(f, approved, "default", &out)
	resume.Resume, resume.Parallel, resume.KeepWorktrees = r.Delivery(), 1, true
	resume.Supervision, resume.Supervisor = true, &SuperviseOptions{}
	before := cancelSequenceRead(t, r.store.Path(r.Delivery()))
	for _, mismatch := range []string{"receipt", "bytes"} {
		wrong := *approved
		wrong.Content = bytes.Clone(approved.Content)
		if mismatch == "receipt" {
			wrong.ReceiptID = "another-approval"
		} else {
			wrong.Content = bytes.ReplaceAll(wrong.Content, []byte("\r\n"), []byte("\n"))
			wrong.ContentDigest = snapshotTestDigest(wrong.Content)
		}
		invalid := resume
		invalid.ApprovedSnapshot = &wrong
		got, err := Resume(context.Background(), invalid)
		if got != nil {
			if releaseErr := got.Release(); releaseErr != nil {
				t.Fatal(releaseErr)
			}
		}
		if !errors.Is(err, ErrSnapshotMismatch) || !bytes.Equal(cancelSequenceRead(t, r.store.Path(r.Delivery())), before) {
			t.Fatalf("resume accepted %s or changed journal: %v", mismatch, err)
		}
	}
	resumed, err := Resume(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := resumed.Release(); err != nil {
			t.Error(err)
		}
	})
	if resumed.Delivery() != r.Delivery() || resumed.plan.Context != "Original context." || resumed.plan.Set.Digest != r.plan.Set.Digest || !bytes.Equal(resumed.opts.ApprovedSnapshot.Content, approved.Content) || resumed.opts.ApprovedSnapshot.ReceiptID != approved.ReceiptID {
		t.Fatal("resume changed approved identity or bytes")
	}
	state, err := resumed.Run(context.Background())
	if err != nil || state != StateReviewBlocked || resumed.terminal != StateDone {
		t.Fatalf("supervised resume=%s, %v terminal=%s\n%s", state, err, resumed.terminal, &out)
	}
	if !bytes.Equal(cancelSequenceRead(t, filepath.Join(f.root, filepath.FromSlash(approved.Path))), external) || !bytes.Equal(approved.Content, []byte(approvedSnapshotSource)) {
		t.Fatal("resume changed source or approved bytes")
	}
	t.Logf("resume preserved delivery=%s receipt=%s digest=%s; terminal=done, supervision=review_blocked (no review requested)", resumed.Delivery(), approved.ReceiptID, approved.ContentDigest)
}
