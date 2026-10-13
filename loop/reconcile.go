package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
	"github.com/batuta-ai/core/worktree"
)

// Reconcile statuses.
const (
	ReconcileClean      = "clean"
	ReconcileConflict   = "conflict"
	ReconcileIntegrated = "integrated"
)

// ReconcileReport says how a task's parked ref merges into the delivery
// branch. Changed lists the paths the ref changed against the merge base and
// Conflicts the paths a merge would leave conflicted.
type ReconcileReport struct {
	Ref       string
	SHA       string
	Branch    string
	Status    string
	Changed   []string
	Conflicts []string
}

// Reconcile previews the merge of the task's parked ref into the delivery
// branch. It is read-only: it takes no lock, appends nothing and never calls
// prepare, so it also works on a blocked delivery whose worktree is gone.
func Reconcile(ctx context.Context, workspace, delivery, taskID string) (ReconcileReport, error) {
	if !journal.ValidDeliveryID(delivery) {
		return ReconcileReport{}, fmt.Errorf("loop: %q is not a delivery id", delivery)
	}
	// journal.Open creates directories; refuse before it can.
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(journal.Dir), delivery+".jsonl")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ReconcileReport{}, fmt.Errorf("loop: unknown delivery %s: no journal under %s", delivery, journal.Dir)
		}
		return ReconcileReport{}, fmt.Errorf("loop: delivery %s journal: %w", delivery, err)
	}
	store, err := journal.Open(workspace)
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("loop: %w", err)
	}
	records, err := store.Read(delivery)
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("loop: %w", err)
	}
	if len(records) == 0 || records[0].Kind != KindOpened {
		return ReconcileReport{}, errors.New("loop: the journal does not start with delivery_opened")
	}
	var opened openedDetail
	if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
		return ReconcileReport{}, fmt.Errorf("loop: journal: %w", err)
	}
	var graph routing.DeliveryGraph
	if err := json.Unmarshal(records[len(records)-1].Graph, &graph); err != nil {
		return ReconcileReport{}, fmt.Errorf("loop: journal graph: %w", err)
	}
	if graphTask(&graph, taskID) == nil {
		return ReconcileReport{}, fmt.Errorf("loop: unknown task %s in delivery %s", taskID, delivery)
	}
	ref, sha := parkedSnapshot(records, taskID)
	if ref == "" {
		return ReconcileReport{}, fmt.Errorf("loop: task %s has no worktree_snapshotted record in delivery %s", taskID, delivery)
	}
	git, err := worktree.New(ctx, workspace)
	if err != nil {
		return ReconcileReport{}, err
	}
	if err := requireParkedRef(ctx, git, ref, sha); err != nil {
		return ReconcileReport{}, err
	}
	// The ref can move after the check above: preview the commit it named.
	preview, err := git.MergePreview(ctx, opened.Branch, sha)
	if err != nil {
		return ReconcileReport{}, err
	}
	report := ReconcileReport{Ref: ref, SHA: sha, Branch: opened.Branch, Changed: preview.Changed, Conflicts: preview.Conflicts}
	switch {
	case preview.Merged:
		report.Status = ReconcileIntegrated
	case len(preview.Conflicts) > 0:
		report.Status = ReconcileConflict
	default:
		report.Status = ReconcileClean
	}
	return report, nil
}

// parkedSnapshot returns the ref and SHA of the last snapshot record of the
// task that names a ref. The ref name is shared by every delivery of a plan,
// so only this journal says which commit the delivery parked.
func parkedSnapshot(records []journal.Record, taskID string) (string, string) {
	for index := len(records) - 1; index >= 0; index-- {
		record := records[index]
		if record.Kind != KindSnapshot || record.TaskID != taskID {
			continue
		}
		var detail struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(record.Detail, &detail); err != nil || detail.Ref == "" {
			continue
		}
		return detail.Ref, detail.SHA
	}
	return "", ""
}

func requireParkedRef(ctx context.Context, git worktree.GitProvider, ref, sha string) error {
	result, err := git.Runner.Run(ctx, publication.Command{
		Executable: git.Git, Args: []string{"rev-parse", "--verify", "--quiet", ref + "^{commit}"}, Directory: git.Root,
		Environment: []string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"},
		StdoutLimit: 1 << 10, StderrLimit: 1 << 10,
	})
	if err != nil {
		return fmt.Errorf("loop: parked ref %s is missing; the delivery parked %s", ref, sha)
	}
	if got := strings.TrimSpace(string(result.Stdout)); got != sha {
		return fmt.Errorf("loop: parked ref %s is %s; the delivery parked %s (a later delivery overwrote the ref)", ref, got, sha)
	}
	return nil
}
