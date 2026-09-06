package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/batuta-ai/core/integration"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

// settleCandidates integrates every wave that holds unsettled candidates,
// in wave order: preflight in a disposable worktree (conflicts surface
// there, never on the user's checkout), then one deterministic apply per
// accepted candidate on the branch, then the graph settlement. It returns
// how many waves were settled.
func (r *Runner) settleCandidates(ctx context.Context) (int, error) {
	settled := 0
	for {
		wave, taskIDs, commits := r.nextSettlement()
		if len(taskIDs) == 0 {
			return settled, nil
		}
		if err := r.settleWave(ctx, wave, taskIDs, commits); err != nil {
			return settled, err
		}
		settled++
	}
}

func (r *Runner) nextSettlement() (routing.DeliveryWave, []string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, wave := range r.graph.Waves {
		var taskIDs, commits []string
		for _, taskID := range wave.TaskIDs {
			task := graphTask(r.graph, taskID)
			if task == nil || task.State != routing.GraphTaskCandidate || len(task.Attempts) == 0 {
				continue
			}
			attempt := task.Attempts[len(task.Attempts)-1]
			if attempt.State != routing.GraphTaskCandidate || attempt.Conflict != nil {
				continue
			}
			if _, known := r.candidates[taskID]; !known {
				continue
			}
			taskIDs = append(taskIDs, taskID)
			commits = append(commits, attempt.CandidateCommitSHA)
		}
		if len(taskIDs) > 0 {
			return wave, taskIDs, commits
		}
	}
	return routing.DeliveryWave{}, nil, nil
}

func (r *Runner) settleWave(ctx context.Context, wave routing.DeliveryWave, taskIDs, commits []string) error {
	head, err := r.git.Head(ctx, r.root)
	if err != nil {
		return err
	}
	r.mu.Lock()
	expected := r.expectedHead()
	candidates := make([]integration.CandidateEvidence, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		candidates = append(candidates, r.candidates[taskID])
	}
	r.mu.Unlock()
	operationID := digestString(strings.Join(append([]string{"integration", r.delivery, fmt.Sprint(wave.Number), expected}, append(taskIDs, commits...)...), "\x00"))
	requestDigest := digestString(operationID + "\x00" + r.root)

	r.mu.Lock()
	pending, resumed := r.preflights[operationID]
	r.mu.Unlock()
	var preflight integration.PreflightResult
	applied := integration.ApplyResult{StartingHeadSHA: expected, ResultingHeadSHA: expected, AcceptedTaskIDs: []string{}, AcceptedCommitSHAs: []string{}}
	if resumed {
		// A crash between preflight and settlement: git says how far the
		// applies got, and the rest continues from there.
		preflight = pending
		applied, err = r.integ.Reconcile(ctx, integration.ReconcileRequest{IntegrationRoot: r.root, Preflight: preflight})
		if err != nil {
			return fmt.Errorf("loop: reconcile wave %d: %w", wave.Number, err)
		}
		head = applied.ResultingHeadSHA
	} else {
		if head != expected {
			return fmt.Errorf("loop: branch %s is at %s but the delivery expects %s; commits landed outside the loop — close the delivery with --abandon and start a new one", r.branch, short(head), short(expected))
		}
		preflight, err = r.integ.Preflight(ctx, integration.PreflightRequest{
			OperationID: operationID, RequestDigest: requestDigest, IntegrationRoot: r.root,
			StartingHeadSHA: head, Candidates: candidates,
		})
		if err != nil {
			return fmt.Errorf("loop: preflight wave %d: %w", wave.Number, err)
		}
		if err := r.locked(KindPreflight, "", map[string]any{"wave": wave.Number, "operation_id": operationID, "preflight": preflight}, func() error {
			r.preflights[operationID] = preflight
			return nil
		}); err != nil {
			return err
		}
	}
	integrated := append([]string{}, preflight.AcceptedResultCommitSHAs[:len(applied.AcceptedTaskIDs)]...)
	for index := len(applied.AcceptedTaskIDs); index < len(preflight.AcceptedTaskIDs); index++ {
		result, err := r.integ.Apply(ctx, integration.ApplyRequest{
			OperationID: operationID, RequestDigest: requestDigest, IntegrationRoot: r.root,
			ExpectedHeadSHA: head, TaskID: preflight.AcceptedTaskIDs[index], CandidateCommitSHA: preflight.AcceptedCommitSHAs[index],
			ExpectedResultTreeSHA: preflight.AcceptedResultTreeSHAs[index], ExpectedResultCommitSHA: preflight.AcceptedResultCommitSHAs[index],
		})
		if err != nil {
			return fmt.Errorf("loop: apply %s: %w", preflight.AcceptedTaskIDs[index], err)
		}
		head = result.ResultingHeadSHA
		integrated = append(integrated, result.ResultingHeadSHA)
	}
	settlement := routing.WaveSettlement{
		OperationID: operationID, RequestDigest: requestDigest, Wave: wave.Number, StartingHeadSHA: preflight.StartingHeadSHA,
		OrderedTaskIDs: taskIDs, CandidateCommitSHAs: commits,
		AcceptedTaskIDs: append([]string{}, preflight.AcceptedTaskIDs...), AcceptedCommitSHAs: append([]string{}, preflight.AcceptedCommitSHAs...),
		IntegratedCommitSHAs: integrated, FirstConflictTaskID: preflight.FirstConflictTaskID,
		ConflictEvidenceDigest: preflight.ConflictEvidenceDigest, FinalHeadSHA: head,
	}
	r.mu.Lock()
	result, serr := r.graph.SettleWave(settlement, r.generation, true)
	if serr != nil {
		r.mu.Unlock()
		return fmt.Errorf("loop: settle wave %d: %w", wave.Number, serr)
	}
	for index, taskID := range settlement.AcceptedTaskIDs {
		r.commits[taskID] = settlement.IntegratedCommitSHAs[index]
		delete(r.candidates, taskID)
	}
	if settlement.FirstConflictTaskID != "" {
		delete(r.candidates, settlement.FirstConflictTaskID)
		r.feedback[settlement.FirstConflictTaskID] = []string{fmt.Sprintf("the previous candidate conflicted with the branch at %s when integrated after %s; re-implement on the current base", short(head), strings.Join(settlement.AcceptedTaskIDs, ", "))}
	}
	delete(r.preflights, operationID)
	recordErr := r.record(KindSettled, "", map[string]any{
		"wave": wave.Number, "operation_id": operationID, "settlement": settlement, "disposition": result.Disposition,
		"conflict_task": settlement.FirstConflictTaskID, "final_head": head,
	})
	r.mu.Unlock()
	if recordErr != nil {
		return recordErr
	}
	for index, taskID := range settlement.AcceptedTaskIDs {
		fmt.Fprintf(r.out, "wave %d: %s integrated as %s\n", wave.Number, taskID, short(settlement.IntegratedCommitSHAs[index]))
		r.writeTrailVerdict(taskID, "✅ approved — commit "+short(settlement.IntegratedCommitSHAs[index]), nil)
	}
	if settlement.FirstConflictTaskID != "" {
		fmt.Fprintf(r.out, "wave %d: %s conflicted; %s\n", wave.Number, settlement.FirstConflictTaskID, result.Disposition)
	}
	return r.cleanupAfterSettlement(ctx, settlement, result)
}

// cleanupAfterSettlement removes the worktrees of integrated tasks (and of
// a conflicting task, which re-executes in a fresh one), recording each
// cleanup on the graph.
func (r *Runner) cleanupAfterSettlement(ctx context.Context, settlement routing.WaveSettlement, result routing.WaveSettlementResult) error {
	if r.opts.KeepWorktrees {
		return nil
	}
	targets := append([]string{}, settlement.AcceptedTaskIDs...)
	if settlement.FirstConflictTaskID != "" {
		targets = append(targets, settlement.FirstConflictTaskID)
	}
	for _, taskID := range targets {
		r.mu.Lock()
		task, found := r.graph.Task(taskID)
		if !found || len(task.Attempts) == 0 {
			r.mu.Unlock()
			continue
		}
		// The attempt that produced the candidate: the last integrated or
		// candidate attempt, not a fresh re-execution attempt.
		var attempt routing.GraphTaskAttempt
		for index := len(task.Attempts) - 1; index >= 0; index-- {
			if task.Attempts[index].WorktreeID != "" {
				attempt = task.Attempts[index]
				break
			}
		}
		wt := r.worktrees[attemptKey(taskID, attempt.Execution)]
		operation := routing.CleanupOperation{
			OperationID:   digestString("cleanup:" + r.delivery + ":" + taskID + ":" + fmt.Sprint(attempt.Execution)),
			RequestDigest: digestString(wt.Root), TaskID: taskID, Execution: attempt.Execution, WorktreeID: attempt.WorktreeID, State: routing.CleanupPlanned,
		}
		planned := task.State == routing.GraphTaskIntegrated && attempt.WorktreeID != ""
		if planned {
			if _, err := r.graph.RecordCleanup(operation); err != nil {
				planned = false
			}
		}
		r.mu.Unlock()
		if wt.Root == "" {
			continue
		}
		removeErr := r.git.Remove(context.WithoutCancel(ctx), wt.Root, wt.Branch)
		state, blocker := routing.CleanupRemoved, ""
		if removeErr != nil {
			state, blocker = routing.CleanupRetained, "worktree_remove_failed"
		}
		if err := r.locked(KindCleanup, taskID, map[string]any{"worktree": wt, "state": state, "error": errorString(removeErr)}, func() error {
			if planned {
				if _, _, err := r.graph.CompleteCleanup(operation.OperationID, state, blocker); err != nil {
					return fmt.Errorf("loop: complete cleanup: %w", err)
				}
			}
			for key, known := range r.worktrees {
				if known.Root == wt.Root {
					delete(r.worktrees, key)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	_ = result
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// replay rebuilds the runner's memory from a journal: the graph from the
// last record, worktrees, feedback, candidates, integrated commits, started
// attempts and a pending preflight.
func (r *Runner) replay(records []journal.Record) error {
	last := records[len(records)-1]
	if len(last.Graph) == 0 {
		return errors.New("loop: the last journal record carries no graph")
	}
	var graph routing.DeliveryGraph
	if err := json.Unmarshal(last.Graph, &graph); err != nil {
		return fmt.Errorf("loop: journal graph: %w", err)
	}
	r.graph = &graph
	settledOps := map[string]bool{}
	for _, record := range records {
		switch record.Kind {
		case KindWorktree:
			var detail struct {
				Execution int             `json:"execution"`
				Worktree  attemptWorktree `json:"worktree"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil {
				r.worktrees[attemptKey(record.TaskID, detail.Execution)] = detail.Worktree
			}
		case KindStarted:
			var detail struct {
				Execution int `json:"execution"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil {
				r.started[attemptKey(record.TaskID, detail.Execution)] = true
			}
		case KindFailure:
			var detail struct {
				Execution     int      `json:"execution"`
				Feedback      []string `json:"feedback"`
				Blocked       bool     `json:"blocked"`
				ReuseWorktree bool     `json:"reuse_worktree"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil {
				if detail.Blocked {
					delete(r.feedback, record.TaskID)
				} else {
					r.feedback[record.TaskID] = detail.Feedback
				}
				if detail.ReuseWorktree {
					r.worktrees[attemptKey(record.TaskID, detail.Execution+1)] = r.worktrees[attemptKey(record.TaskID, detail.Execution)]
				}
			}
		case KindCandidate:
			var detail struct {
				Evidence integration.CandidateEvidence `json:"evidence"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil {
				r.candidates[record.TaskID] = detail.Evidence
				delete(r.feedback, record.TaskID)
			}
		case KindPreflight:
			var detail struct {
				OperationID string                      `json:"operation_id"`
				Preflight   integration.PreflightResult `json:"preflight"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil {
				r.preflights[detail.OperationID] = detail.Preflight
			}
		case KindSettled:
			var detail struct {
				OperationID string                 `json:"operation_id"`
				Settlement  routing.WaveSettlement `json:"settlement"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil {
				settledOps[detail.OperationID] = true
				for index, taskID := range detail.Settlement.AcceptedTaskIDs {
					r.commits[taskID] = detail.Settlement.IntegratedCommitSHAs[index]
					delete(r.candidates, taskID)
				}
				if detail.Settlement.FirstConflictTaskID != "" {
					delete(r.candidates, detail.Settlement.FirstConflictTaskID)
				}
			}
		case KindCleanup:
			var detail struct {
				Worktree attemptWorktree `json:"worktree"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil {
				for key, known := range r.worktrees {
					if known.Root == detail.Worktree.Root {
						delete(r.worktrees, key)
					}
				}
			}
		case KindTerminal:
			var detail struct {
				State string `json:"state"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil {
				r.terminal = detail.State
			}
		}
	}
	for operationID := range r.preflights {
		if settledOps[operationID] {
			delete(r.preflights, operationID)
		}
	}
	// Attempts that were running when the process died are stalled: the
	// executor is gone. The graph records the failure now, so the policy
	// decides (retry in the same worktree, then escalate).
	for _, task := range r.graph.Tasks {
		if len(task.Attempts) == 0 || task.State != routing.GraphTaskRunning {
			continue
		}
		attempt := task.Attempts[len(task.Attempts)-1]
		if attempt.State != routing.GraphTaskRunning || !r.started[attemptKey(task.TaskID, attempt.Execution)] {
			continue
		}
		wt := r.worktrees[attemptKey(task.TaskID, attempt.Execution)]
		ac := attemptContext{taskID: task.TaskID, execution: attempt.Execution, runtime: attempt.Runtime, base: attempt.BaseHeadSHA, plan: r.planTask(task.TaskID), worktree: wt,
			runID: r.delivery + "-" + strings.ReplaceAll(task.TaskID, "_", "-") + "-e" + fmt.Sprint(attempt.Execution)}
		if attempt.ChildRunID != "" {
			ac.runID = attempt.ChildRunID
		}
		if err := r.recordFailure(context.Background(), ac, nil, blockerInterrupted, []string{"the previous run was interrupted while this executor was working; the worktree keeps whatever it wrote"}); err != nil {
			return err
		}
	}
	return nil
}
