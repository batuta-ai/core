package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

type GateFailureError struct{ Failures []string }

func (e *GateFailureError) Error() string { return strings.Join(e.Failures, "\n") }

func Recover(ctx context.Context, opts Options, delivery, taskID string) (recoverErr error) {
	r, err := prepare(ctx, opts)
	if err != nil {
		return err
	}
	if !journal.ValidDeliveryID(delivery) {
		return fmt.Errorf("loop: %q is not a delivery id", delivery)
	}
	check := func() error {
		_, _, err := r.readBoundOpening(delivery)
		return err
	}
	ownership, err := acquireCheckedDeliveryOwnership(ctx, r.root, delivery, r.now(), check, presenceTiming{now: r.now, sleep: r.sleep})
	if err != nil {
		return err
	}
	r.ownership = ownership
	defer func() { recoverErr = errors.Join(recoverErr, r.releaseOwnership()) }()

	records, opened, err := r.readBoundOpening(delivery)
	if err != nil {
		return err
	}
	var graph routing.DeliveryGraph
	if err := json.Unmarshal(records[len(records)-1].Graph, &graph); err != nil {
		return fmt.Errorf("loop: journal graph: %w", err)
	}
	task := graphTask(&graph, taskID)
	if task == nil {
		return fmt.Errorf("loop: unknown task %s", taskID)
	}
	if task.State != routing.GraphTaskWaitingInput {
		if task.State == routing.GraphTaskIntegrated {
			return fmt.Errorf("loop: task %s already integrated", taskID)
		}
		return fmt.Errorf("loop: task %s is %s, not waiting_input", taskID, task.State)
	}
	if state := lastTerminal(records); state != "" && state != StateWaitingInput && state != StateCanceled {
		return fmt.Errorf("loop: delivery %s already ended: %s", delivery, state)
	}
	if live := liveAttemptTask(&graph, taskID); live != "" {
		return fmt.Errorf("loop: task %s has a live attempt; recovery would fail it as interrupted", live)
	}
	if err := r.loadPlan(opened.Slug); err != nil {
		return err
	}
	if r.plan.Set.Digest != opened.PlanDigest {
		return fmt.Errorf("loop: plan %s changed since delivery %s started (task set digest differs)", opened.Slug, delivery)
	}
	if r.branch != opened.Branch {
		return fmt.Errorf("loop: delivery %s runs on branch %s; %s is checked out", delivery, opened.Branch, r.branch)
	}
	r.delivery = delivery
	r.generation = opened.Generation
	r.parallel = opened.Parallel
	r.journaled = true
	if err := r.loadCapabilityProbes(records); err != nil {
		return err
	}
	if err := r.replay(records); err != nil {
		return err
	}
	if expected := r.expectedHead(); expected != "" && expected != r.openedHead {
		return fmt.Errorf("loop: branch %s moved since delivery %s last integrated (%s now, %s expected)", r.branch, delivery, short(r.openedHead), short(expected))
	}
	task = graphTask(r.graph, taskID)
	if task == nil || len(task.Attempts) == 0 {
		return fmt.Errorf("loop: task %s has no parked attempt", taskID)
	}
	attempt := task.Attempts[len(task.Attempts)-1]
	wt, ok := r.worktrees[attemptKey(taskID, attempt.Execution)]
	if !ok || wt.Root == "" {
		return fmt.Errorf("loop: task %s has no attached worktree", taskID)
	}
	info, err := os.Stat(wt.Root)
	if err != nil {
		return fmt.Errorf("loop: task %s worktree: %w", taskID, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("loop: task %s worktree %s is not a directory", taskID, wt.Root)
	}
	ac := attemptContext{
		taskID: taskID, execution: attempt.Execution, runtime: attempt.Runtime,
		base: attempt.BaseHeadSHA, runID: attempt.ChildRunID,
		plan: r.planTask(taskID), worktree: wt,
	}
	ac.adapter, err = r.adapterLocked(ac.runtime.Provider)
	if err != nil {
		return err
	}
	if err := r.preflight(ctx); err != nil {
		return err
	}
	criteria := gates.ParseCriteria(ac.plan.Accept)
	treeChanged, err := r.treeChangedFromBase(ctx, wt.Root, ac.base)
	if err != nil {
		return fmt.Errorf("loop: compare %s against base %s: %w", wt.Name, ac.base, err)
	}
	result := executor.Result{Finished: true}
	var report gates.Report
	verifierSkipped := ""
	if treeChanged {
		report, _, verifierSkipped, err = r.attemptGates(ctx, &ac, result, true, criteria, "recovered by the conductor")
		if err != nil {
			return err
		}
	} else {
		report = gates.Report{
			TaskID: taskID, Execution: ac.execution,
			Finished: gates.Finished(true, false, false, 0, "recovered by the conductor"),
			Tree:     gates.Verdict{Name: "tree", Pass: false, Signal: "nothing to recover: the worktree equals the attempt's base"},
			Tests:    gates.Verdict{Name: "tests", Pass: true, Signal: "not evaluated"},
			Scope:    gates.Verdict{Name: "scope", Pass: true, Signal: "not evaluated"},
		}
		report.Decide()
	}
	if err := r.locked(KindGates, taskID, gatesReportDetail{Report: report, VerifierSkipped: verifierSkipped, Source: "conductor"}, nil); err != nil {
		return err
	}
	if !report.Passed {
		return &GateFailureError{Failures: report.Failures()}
	}
	sha, digest, message, evidence, canonicalErr, squashErr, validateErr := r.candidateCommit(ctx, ac, report)
	if err := errors.Join(canonicalErr, squashErr, validateErr); err != nil {
		return err
	}
	if err := r.locked(KindCandidate, taskID, map[string]any{
		"execution": ac.execution, "commit": sha, "verification_digest": digest,
		"evidence": evidence, "message": message, "source": "conductor",
	}, func() error {
		if _, err := r.graph.RecordRecoveredCandidate(taskID, ac.execution, routing.TaskCandidate{
			ChildRunID: ac.runID, BaseHeadSHA: ac.base, CommitSHA: sha, VerificationDigest: digest,
		}, r.now()); err != nil {
			return fmt.Errorf("loop: record recovered candidate: %w", err)
		}
		r.candidates[taskID] = evidence
		delete(r.feedback, taskID)
		return nil
	}); err != nil {
		return err
	}
	askPath := filepath.Join(r.root, ".batuta", "asks", r.plan.Slug+"-"+strings.ReplaceAll(taskID, "_", "-")+".md")
	if err := os.Remove(askPath); err != nil {
		return fmt.Errorf("loop: remove recovered ask: %w", err)
	}
	return nil
}

func liveAttemptTask(graph *routing.DeliveryGraph, except string) string {
	for _, task := range graph.Tasks {
		if task.TaskID == except {
			continue
		}
		for _, attempt := range task.Attempts {
			if attempt.State == routing.GraphTaskPreparing || attempt.State == routing.GraphTaskRunning {
				return task.TaskID
			}
		}
	}
	return ""
}
