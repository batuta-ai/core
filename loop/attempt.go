package loop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/integration"
	"github.com/batuta-ai/core/routing"
)

// Blocker codes the loop records on a failed attempt.
const (
	blockerExecutorFailed = "executor_failed"
	blockerRateLimited    = "rate_limited"
	blockerTimedOut       = "timed_out"
	blockerNoChanges      = "no_changes"
	blockerTestsFailed    = "tests_failed"
	blockerScope          = "scope_violation"
	blockerProof          = "proof_failed"
	blockerVerifier       = "verifier_incomplete"
	blockerInstall        = "install_failed"
	blockerInterrupted    = "interrupted"
	blockerCandidate      = "candidate_invalid"
	blockerSelf           = "needs_conducting_session"
	blockerUnsafeQuestion = "question_unsafe"
	// blockerAlreadySatisfied is not a failure: the criteria held before the
	// executor touched anything, so there is no candidate to integrate. The
	// task is ticked in the plan at the end without a commit.
	blockerAlreadySatisfied = "already_satisfied"
)

type attemptContext struct {
	taskID    string
	execution int
	runtime   routing.RuntimeValue
	base      string
	plan      routing.PlanTask
	worktree  attemptWorktree
	adapter   executor.Adapter
	runID     string
	previous  *routing.GraphTaskAttempt // the answered attempt, on a continuation
}

// runAttempt drives one attempt of one task from worktree to candidate or
// failure. Graph mutations and journal writes happen under r.mu; the
// executor and the gates run outside it so parallel tasks overlap.
func (r *Runner) runAttempt(ctx context.Context, taskID string) error {
	r.mu.Lock()
	task, found := r.graph.Task(taskID)
	if !found || len(task.Attempts) == 0 {
		r.mu.Unlock()
		return fmt.Errorf("loop: task %s has no attempt to run", taskID)
	}
	attempt := task.Attempts[len(task.Attempts)-1]
	ac := attemptContext{
		taskID: taskID, execution: attempt.Execution, runtime: attempt.Runtime, base: attempt.BaseHeadSHA,
		plan: r.planTask(taskID), runID: r.delivery + "-" + strings.ReplaceAll(taskID, "_", "-") + "-e" + fmt.Sprint(attempt.Execution),
	}
	if attempt.ChildRunID != "" {
		// A continuation after an answer keeps the run identity of the
		// attempt that asked; the graph checks it on every later record.
		ac.runID = attempt.ChildRunID
	}
	if len(task.Attempts) >= 2 {
		earlier := task.Attempts[len(task.Attempts)-2]
		if earlier.Question != nil && earlier.Question.Answer != nil && attempt.State == routing.GraphTaskRunning {
			ac.previous = &earlier
		}
	}
	r.mu.Unlock()

	if ac.runtime.Provider == string(routing.ExecutorSelf) {
		return r.recordFailure(ctx, ac, nil, blockerSelf, []string{"the routing table escalates this task to `self`, the conducting session, which the loop cannot run"})
	}
	adapter, err := r.adapterLocked(ac.runtime.Provider)
	if err != nil {
		return err
	}
	ac.adapter = adapter

	if err := r.ensureWorktree(ctx, &ac, attempt); err != nil {
		return err
	}
	if ac.worktree.Fresh && strings.TrimSpace(r.profile.Install) != "" {
		if code, output, err := r.shell.Run(ctx, ac.worktree.Root, r.profile.Install); err != nil || code != 0 {
			return r.recordFailure(ctx, ac, nil, blockerInstall, []string{fmt.Sprintf("install command `%s` exited %d\n%s", r.profile.Install, code, executor.Tail([]byte(output), 20))})
		}
	}

	criteria := gates.ParseCriteria(ac.plan.Accept)
	r.mu.Lock()
	feedback := append([]string(nil), r.feedback[taskID]...)
	r.mu.Unlock()
	input := BriefInput{
		Plan: r.plan, Task: ac.plan, Profile: r.profile, Conventions: r.sections, Missing: r.missing,
		Worktree: ac.worktree.Root, Base: ac.base, Criteria: criteria, Feedback: feedback,
		Lane: string(ac.plan.Domain) + "/" + string(ac.plan.Complexity),
	}
	if ac.previous != nil && ac.previous.Question != nil {
		input.Question = ac.previous.Question.Prompt
		input.Answer = ac.previous.Question.Answer.Value
	}
	brief := Brief(input)
	briefPath := filepath.Join(ac.worktree.Root, ".batuta", "brief-"+strings.ReplaceAll(taskID, "_", "-")+".md")
	trail := r.trailPath(taskID)
	_ = os.MkdirAll(filepath.Dir(trail), 0o755)
	_ = os.WriteFile(strings.TrimSuffix(trail, ".md")+"-e"+fmt.Sprint(ac.execution)+".brief.md", []byte(brief), 0o644)

	before, err := r.gitState.WorktreeState(ctx, ac.worktree.Root)
	if err != nil {
		return fmt.Errorf("loop: snapshot %s: %w", ac.worktree.Name, err)
	}
	request := executor.Request{Brief: brief, BriefFile: briefPath, Cwd: ac.worktree.Root, Model: ac.runtime.Model, Effort: ac.runtime.Reasoning}
	invocation, err := adapter.Command(request)
	if err != nil {
		return fmt.Errorf("loop: %s: %w", adapter.Name, err)
	}
	if invocation.UsedFile {
		if err := os.MkdirAll(filepath.Dir(briefPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(briefPath, []byte(brief), 0o644); err != nil {
			return err
		}
	}
	if err := r.locked(KindStarted, taskID, map[string]any{
		"execution": ac.execution, "run_id": ac.runID, "executor": adapter.Name, "model": ac.runtime.Model,
		"reasoning": ac.runtime.Reasoning, "argv": redactArgs(invocation, brief), "worktree": ac.worktree.Root,
		"brief_lines": strings.Count(brief, "\n") + 1, "via_file": invocation.UsedFile,
	}, func() error { r.started[attemptKey(taskID, ac.execution)] = true; return nil }); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "%s e%d → %s/%s in %s\n", taskID, ac.execution, adapter.Name, ac.runtime.Model, filepath.Base(ac.worktree.Root))

	// Invariant from the harness this loop descends from: a usage limit is
	// not a failure. Wait for the reset and run the SAME attempt again; it
	// consumes no retry, no escalation. The cap keeps a run from sleeping
	// forever on a limit that never lifts.
	var (
		result  executor.Result
		execErr error
		waits   int
	)
	for {
		result, execErr = r.subprocess.Execute(ctx, adapter, invocation, r.opts.TaskTimeout)
		if execErr != nil || !result.RateLimited || waits >= r.opts.MaxLimitWaits {
			break
		}
		waits++
		delay := r.limitDelay(result.ResetAt)
		if err := r.locked(KindLimitWait, taskID, map[string]any{"execution": ac.execution, "wait": waits, "seconds": int(delay.Seconds()), "reset_at": result.ResetAt}, nil); err != nil {
			return err
		}
		fmt.Fprintf(r.out, "%s e%d: %s hit a usage limit; waiting %s before re-running the same attempt (%d/%d)\n", taskID, ac.execution, adapter.Name, delay.Round(time.Second), waits, r.opts.MaxLimitWaits)
		if err := r.sleep(ctx, delay); err != nil {
			return nil // canceled while waiting; the attempt resumes as interrupted
		}
	}
	if invocation.UsedFile {
		_ = os.Remove(briefPath)
	}
	r.writeLog(taskID, ac.execution, result)
	if execErr != nil {
		if ctx.Err() != nil {
			return nil // the run is being canceled; the attempt stays running and resumes as interrupted
		}
		return r.recordFailure(ctx, ac, &result, blockerExecutorFailed, []string{"the executor could not start: " + execErr.Error()})
	}
	after, err := r.gitState.WorktreeState(ctx, ac.worktree.Root)
	if err != nil {
		return fmt.Errorf("loop: snapshot %s: %w", ac.worktree.Name, err)
	}
	if err := r.locked(KindFinished, taskID, map[string]any{
		"execution": ac.execution, "exit_code": result.ExitCode, "finished": result.Finished, "timed_out": result.TimedOut,
		"rate_limited": result.RateLimited, "duration_ms": result.Duration.Milliseconds(), "question": result.Question,
		"stdout_bytes": len(result.Stdout), "stderr_bytes": len(result.Stderr), "tree_changed": before != after,
	}, nil); err != nil {
		return err
	}

	if result.Finished && result.Question != "" {
		return r.recordQuestion(ctx, ac, result, before != after)
	}

	report := gates.Report{TaskID: taskID, Execution: ac.execution}
	report.Finished = gates.Finished(result.Finished, result.TimedOut, result.RateLimited, result.ExitCode, executor.Tail(append(result.Stdout, result.Stderr...), 30))
	report.Tree = gates.Tree(before, after)
	silent := before == after
	if report.Finished.Pass && !silent {
		report.Tests = gates.Tests(ctx, r.shell, ac.worktree.Root, r.profile.Test)
		changed, err := r.git.ChangedPaths(ctx, ac.worktree.Root, ac.base)
		if err != nil {
			return err
		}
		report.Scope = gates.Scope(changed, ac.plan.Scope)
		report.Proofs = gates.Proofs(ctx, r.shell, ac.worktree.Root, criteria)
		if gates.NeedsVerifier(string(ac.plan.Complexity), silent, ac.execution) && len(criteria) > 0 {
			verdict := r.verify(ctx, ac, criteria, report.Proofs)
			report.Verifier = &verdict
		}
	} else if silent && report.Finished.Pass {
		// The session wrote nothing. Either the task was already done on the
		// base (the verifier decides, against the real code) or the executor
		// gave up silently. Neither yields a candidate: a satisfied task is
		// ticked in the plan at the end; a silent give-up is a failure.
		report.Tests = gates.Tests(ctx, r.shell, ac.worktree.Root, r.profile.Test)
		report.Scope = gates.Verdict{Name: "scope", Pass: true, Signal: "nothing changed"}
		if len(criteria) > 0 {
			verdict := r.verify(ctx, ac, criteria, report.Proofs)
			report.Verifier = &verdict
			if verdict.Pass && report.Tests.Pass {
				report.Passed = true
				if err := r.locked(KindGates, taskID, report, nil); err != nil {
					return err
				}
				r.writeTrail(ac, brief, result, report, "already satisfied on the base — no commit")
				return r.recordBlocked(ctx, ac, &result, blockerAlreadySatisfied, []string{"gates 2 and 3 green against the base commit: the criteria already hold; nothing to commit"})
			}
		}
		report.Tree = gates.Verdict{Name: "tree", Pass: false, Signal: "silent: the session wrote nothing and the criteria do not all hold on the base"}
	} else {
		report.Tests = gates.Verdict{Name: "tests", Pass: false, Signal: "skipped: the executor did not finish"}
		report.Scope = gates.Verdict{Name: "scope", Pass: true, Signal: "not evaluated"}
	}
	report.Decide()
	if err := r.locked(KindGates, taskID, report, nil); err != nil {
		return err
	}
	r.writeTrail(ac, brief, result, report, "")

	if !report.Passed {
		code := blockerCode(report, result, silent)
		return r.recordFailure(ctx, ac, &result, code, report.Failures())
	}
	return r.recordCandidate(ctx, ac, report, result)
}

// limitDelay is how long to wait for a usage limit: until the reset the
// output named plus a buffer, or the default when it named none.
func (r *Runner) limitDelay(resetAt time.Time) time.Duration {
	if resetAt.IsZero() {
		return r.opts.LimitWaitDefault
	}
	delay := time.Until(resetAt) + r.opts.LimitBuffer
	if delay < r.opts.LimitBuffer {
		delay = r.opts.LimitBuffer
	}
	return delay
}

func (r *Runner) sleep(ctx context.Context, delay time.Duration) error {
	if r.opts.Sleep != nil {
		return r.opts.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Runner) adapterLocked(name string) (executor.Adapter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.adapter(name)
}

// ensureWorktree gives the attempt its worktree: the one already attached
// (continuation after an answer, or a same-runtime retry that keeps the
// partial work), or a fresh one at the attempt's base.
func (r *Runner) ensureWorktree(ctx context.Context, ac *attemptContext, attempt routing.GraphTaskAttempt) error {
	r.mu.Lock()
	existing, known := r.worktrees[attemptKey(ac.taskID, ac.execution)]
	if !known && attempt.WorktreeRoot != "" {
		// The graph already binds this attempt to a worktree (a continuation
		// after an answer copies the asking attempt's): find it by root.
		for _, candidate := range r.worktrees {
			if candidate.Root == attempt.WorktreeRoot {
				existing, known = candidate, true
				break
			}
		}
		if !known {
			existing, known = attemptWorktree{Name: attempt.WorktreeID, Branch: r.branchName(ac.taskID, ac.execution-1), Root: attempt.WorktreeRoot}, true
		}
		r.worktrees[attemptKey(ac.taskID, ac.execution)] = existing
	}
	r.mu.Unlock()
	if known {
		if _, err := os.Stat(existing.Root); err == nil {
			existing.Fresh = false
			ac.worktree = existing
			if attempt.State == routing.GraphTaskPreparing {
				return r.attach(ac, existing)
			}
			return nil
		}
	}
	name, branch := r.worktreeName(ac.taskID, ac.execution), r.branchName(ac.taskID, ac.execution)
	root, err := r.git.Add(ctx, name, branch, ac.base)
	if err != nil {
		return fmt.Errorf("loop: %w", err)
	}
	ac.worktree = attemptWorktree{Name: name, Branch: branch, Root: root, Fresh: true}
	return r.attach(ac, ac.worktree)
}

func (r *Runner) attach(ac *attemptContext, wt attemptWorktree) error {
	return r.locked(KindWorktree, ac.taskID, map[string]any{"execution": ac.execution, "worktree": wt}, func() error {
		intent := routing.TaskWorktreeIntent{
			OperationID:   digestString("worktree:" + r.delivery + ":" + ac.taskID + ":" + fmt.Sprint(ac.execution)),
			RequestDigest: digestString(wt.Root + ":" + wt.Branch), Name: wt.Name, Branch: wt.Branch,
		}
		task := graphTask(r.graph, ac.taskID)
		if task != nil && task.State == routing.GraphTaskPreparing {
			if _, err := r.graph.PlanWorktree(ac.taskID, ac.execution, intent); err != nil {
				return fmt.Errorf("loop: plan worktree: %w", err)
			}
			if _, err := r.graph.AttachWorktree(ac.taskID, ac.execution, routing.GraphWorktree{ID: wt.Name, Root: wt.Root, Ready: true}); err != nil {
				return fmt.Errorf("loop: attach worktree: %w", err)
			}
		}
		r.worktrees[attemptKey(ac.taskID, ac.execution)] = wt
		return nil
	})
}

// verify dispatches the independent read-only verifier: the `low` row's
// executor when it differs from the one that wrote the diff, else the
// task's own adapter on the task's model.
func (r *Runner) verify(ctx context.Context, ac attemptContext, criteria []gates.Criterion, proofs []gates.Verdict) gates.Verdict {
	name, model := ac.adapter.Name, ac.runtime.Model
	if row, found := r.table.Row(routing.ComplexityLow, ac.plan.Domain); found && row.Executor != routing.ExecutorSelf && string(row.Executor) != ac.adapter.Name {
		if _, err := r.adapterLocked(string(row.Executor)); err == nil {
			name, model = string(row.Executor), row.Model
		}
	}
	adapter, err := r.adapterLocked(name)
	if err != nil {
		return gates.Verdict{Name: "verifier", Pass: false, Signal: "no verifier adapter: " + err.Error()}
	}
	prompt := gates.VerifierPrompt(ac.plan.Title, criteria, proofs, ac.base)
	invocation, err := adapter.ReadonlyCommand(executor.Request{Prompt: prompt, Cwd: ac.worktree.Root, Model: model})
	if err != nil {
		return gates.Verdict{Name: "verifier", Pass: false, Signal: "verifier invocation: " + err.Error()}
	}
	before, err := r.gitState.WorktreeState(ctx, ac.worktree.Root)
	if err != nil {
		return gates.Verdict{Name: "verifier", Pass: false, Signal: "verifier guard: " + err.Error()}
	}
	result, err := r.subprocess.Execute(ctx, adapter, invocation, r.opts.TaskTimeout)
	if err != nil {
		return gates.Verdict{Name: "verifier", Pass: false, Signal: "verifier did not start: " + err.Error()}
	}
	after, err := r.gitState.WorktreeState(ctx, ac.worktree.Root)
	if err == nil && before != after {
		return gates.Verdict{Name: "verifier", Pass: false, Signal: "the verifier wrote to the tree; round invalid", Detail: executor.Tail(result.Stdout, 10)}
	}
	verdict := gates.Verifier(string(result.Stdout), len(criteria), proofs)
	verdict.Signal = name + "/" + model + ": " + verdict.Signal
	return verdict
}

func (r *Runner) recordQuestion(ctx context.Context, ac attemptContext, result executor.Result, treeChanged bool) error {
	question := routing.TaskQuestion{
		RequestID: digestString("question:" + ac.runID + ":" + result.Question), Prompt: result.Question,
		ContextDigest: digestString(string(result.Stdout)),
	}
	if !routing.SafeTaskQuestionText(question.Prompt) || len(question.Prompt) > 2000 {
		return r.recordFailure(ctx, ac, &result, blockerUnsafeQuestion, []string{"the executor asked a question the journal cannot carry (a path, a token or over 2000 bytes): " + executor.Tail([]byte(result.Question), 1)})
	}
	askPath := filepath.Join(r.root, ".batuta", "asks", r.plan.Slug+"-"+strings.ReplaceAll(ac.taskID, "_", "-")+".md")
	err := r.locked(KindQuestion, ac.taskID, map[string]any{"execution": ac.execution, "run_id": ac.runID, "question": question.Prompt, "request_id": question.RequestID, "ask_path": askPath}, func() error {
		if _, err := r.graph.RecordQuestion(ac.taskID, ac.execution, ac.runID, question, r.now()); err != nil {
			return fmt.Errorf("loop: record question: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(askPath), 0o755)
	body := fmt.Sprintf("# Question — %s (%s)\n\n**Delivery:** %s · **Executor:** %s/%s · **Worktree:** %s\n\n%s\n\nAnswer with:\n\n    batuta loop --answer %s \"<your answer>\"\n", ac.plan.Title, ac.taskID, r.delivery, ac.adapter.Name, ac.runtime.Model, ac.worktree.Root, question.Prompt, ac.taskID)
	_ = os.WriteFile(askPath, []byte(body), 0o644)
	fmt.Fprintf(r.out, "%s asks: %s\n  answer: batuta loop --answer %s \"<text>\"\n", ac.taskID, question.Prompt, ac.taskID)
	return nil
}

// recordCandidate squashes the worktree into one commit, proves it with
// the integration layer and records it on the graph.
func (r *Runner) recordCandidate(ctx context.Context, ac attemptContext, report gates.Report, result executor.Result) error {
	canonical, digest, err := report.Canonical()
	if err != nil {
		return err
	}
	message := commitMessage(ac.plan, r.plan.Slug)
	sha, err := r.git.Squash(ctx, ac.worktree.Root, ac.base, message)
	if err != nil {
		return r.recordFailure(ctx, ac, &result, blockerCandidate, []string{"could not squash the worktree into one commit: " + err.Error()})
	}
	evidence, err := r.integ.Candidate(ctx, integration.CandidateRequest{
		TaskID: ac.taskID, Slug: r.plan.Slug, WorktreeRoot: ac.worktree.Root, RepositoryRoot: r.root,
		ExpectedBranch: ac.worktree.Branch, BaseSHA: ac.base, Verification: canonical, VerificationDigest: digest,
		AllowedTrackingPaths: []string{},
	})
	if err != nil {
		return r.recordFailure(ctx, ac, &result, blockerCandidate, []string{"the candidate commit did not validate: " + err.Error()})
	}
	err = r.locked(KindCandidate, ac.taskID, map[string]any{"execution": ac.execution, "commit": sha, "verification_digest": digest, "evidence": evidence, "message": message}, func() error {
		if _, err := r.graph.RecordCandidate(ac.taskID, ac.execution, routing.TaskCandidate{
			ChildRunID: ac.runID, BaseHeadSHA: ac.base, CommitSHA: sha, VerificationDigest: digest,
		}); err != nil {
			return fmt.Errorf("loop: record candidate: %w", err)
		}
		r.candidates[ac.taskID] = evidence
		delete(r.feedback, ac.taskID)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "%s e%d ✓ candidate %s\n", ac.taskID, ac.execution, short(sha))
	return nil
}

// recordFailure applies the conducting policy — retry once on the same
// runtime, escalate once, then abort — and keeps or drops the worktree
// accordingly: a same-runtime retry continues in the same worktree with
// the failure as feedback; an escalation starts clean.
func (r *Runner) recordFailure(ctx context.Context, ac attemptContext, result *executor.Result, code string, feedback []string) error {
	return r.recordFailureWithPolicy(ctx, ac, result, code, feedback, routing.ConductingFailurePolicy)
}

// recordBlocked aborts the task at once: no retry, no escalation.
func (r *Runner) recordBlocked(ctx context.Context, ac attemptContext, result *executor.Result, code string, feedback []string) error {
	return r.recordFailureWithPolicy(ctx, ac, result, code, feedback, routing.FailurePolicy{})
}

func (r *Runner) recordFailureWithPolicy(ctx context.Context, ac attemptContext, result *executor.Result, code string, feedback []string, policy routing.FailurePolicy) error {
	status := "failed"
	if code == blockerInterrupted {
		status = "stalled"
	}
	if result != nil && result.TimedOut {
		status = "stalled"
	}
	r.mu.Lock()
	outcome, ferr := r.graph.RecordFailureWithPolicy(ac.taskID, ac.execution, routing.TaskFailure{
		ChildRunID: ac.runID, TerminalStatus: status, BlockerCode: code,
	}, r.generation, ac.base, policy)
	if ferr != nil {
		r.mu.Unlock()
		return fmt.Errorf("loop: record failure of %s: %w", ac.taskID, ferr)
	}
	sameRuntime := !outcome.Blocked && outcome.Runtime == ac.runtime
	if !outcome.Blocked {
		r.feedback[ac.taskID] = feedback
		if sameRuntime && ac.worktree.Root != "" {
			r.worktrees[attemptKey(ac.taskID, ac.execution+1)] = attemptWorktree{Name: ac.worktree.Name, Branch: ac.worktree.Branch, Root: ac.worktree.Root}
		}
	}
	detail := map[string]any{
		"execution": ac.execution, "blocker": code, "status": status, "feedback": feedback, "blocked": outcome.Blocked,
		"next_execution": ac.execution + 1, "next_runtime": outcome.Runtime, "same_runtime": sameRuntime, "reuse_worktree": sameRuntime && ac.worktree.Root != "",
	}
	recordErr := r.record(KindFailure, ac.taskID, detail)
	r.mu.Unlock()
	if recordErr != nil {
		return recordErr
	}
	switch {
	case outcome.Blocked && code == blockerAlreadySatisfied:
		fmt.Fprintf(r.out, "%s e%d ✓ already satisfied on the base; no commit\n", ac.taskID, ac.execution)
		r.writeTrailVerdict(ac.taskID, "✅ already satisfied — no commit", feedback)
	case outcome.Blocked:
		fmt.Fprintf(r.out, "%s e%d ✗ %s — aborted (%s)\n", ac.taskID, ac.execution, code, firstLine(strings.Join(feedback, " ")))
		r.writeTrailVerdict(ac.taskID, "❌ aborted — "+code, feedback)
	case sameRuntime:
		fmt.Fprintf(r.out, "%s e%d ✗ %s — retry on %s/%s with feedback\n", ac.taskID, ac.execution, code, ac.runtime.Provider, ac.runtime.Model)
	default:
		fmt.Fprintf(r.out, "%s e%d ✗ %s — escalating to %s/%s\n", ac.taskID, ac.execution, code, outcome.Runtime.Provider, outcome.Runtime.Model)
		r.writeTrailVerdict(ac.taskID, "⏫ escalated from "+string(ac.plan.Complexity)+" ("+ac.runtime.Provider+"/"+ac.runtime.Model+" → "+outcome.Runtime.Provider+"/"+outcome.Runtime.Model+")", feedback)
	}
	if (outcome.Blocked || !sameRuntime) && ac.worktree.Root != "" && !r.opts.KeepWorktrees {
		_ = r.git.Remove(context.WithoutCancel(ctx), ac.worktree.Root, ac.worktree.Branch)
	}
	return nil
}

func blockerCode(report gates.Report, result executor.Result, silent bool) string {
	switch {
	case result.RateLimited:
		return blockerRateLimited
	case result.TimedOut:
		return blockerTimedOut
	case !report.Finished.Pass:
		return blockerExecutorFailed
	case silent:
		return blockerNoChanges
	case !report.Tests.Pass:
		return blockerTestsFailed
	case !report.Scope.Pass:
		return blockerScope
	case report.Verifier != nil && !report.Verifier.Pass:
		return blockerVerifier
	default:
		return blockerProof
	}
}

// commitMessage is the candidate's conventional commit subject. The
// integration layer requires `type(scope)?: subject`; the type follows the
// task's domain and title, and a trailer names the plan task.
func commitMessage(task routing.PlanTask, slug string) string {
	title := strings.TrimSpace(task.Title)
	lower := strings.ToLower(title)
	kind := "feat"
	switch {
	case task.Domain == routing.DomainDocs:
		kind = "docs"
	case task.Domain == routing.DomainTesting:
		kind = "test"
	case strings.HasPrefix(lower, "fix"), strings.HasPrefix(lower, "repair"), strings.Contains(lower, " bug"):
		kind = "fix"
	case strings.HasPrefix(lower, "refactor"), strings.HasPrefix(lower, "extract"), strings.HasPrefix(lower, "rename"):
		kind = "refactor"
	case task.Domain == routing.DomainInfra:
		kind = "chore"
	}
	if len(title) > 0 {
		title = strings.ToLower(title[:1]) + title[1:]
	}
	title = strings.TrimSuffix(title, ".")
	if len(title) > 68 {
		title = strings.TrimSpace(title[:68])
	}
	return fmt.Sprintf("%s: %s\n\nPlan %s, %s. Delivered by batuta loop.\n", kind, title, slug, task.ID)
}

func redactArgs(invocation executor.Invocation, brief string) []string {
	args := []string{invocation.Executable}
	for _, arg := range invocation.Args {
		if arg == brief {
			args = append(args, "<brief>")
			continue
		}
		if len(arg) > 200 {
			arg = arg[:200] + "…"
		}
		args = append(args, arg)
	}
	return args
}

func (r *Runner) writeLog(taskID string, execution int, result executor.Result) {
	path := strings.TrimSuffix(r.trailPath(taskID), ".md") + "-e" + fmt.Sprint(execution) + ".out.log"
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	var b strings.Builder
	fmt.Fprintf(&b, "# exit %d · finished %v · timed out %v · rate limited %v · %s\n\n## stdout\n\n", result.ExitCode, result.Finished, result.TimedOut, result.RateLimited, result.Duration.Round(time.Second))
	b.Write(result.Stdout)
	b.WriteString("\n\n## stderr\n\n")
	b.Write(result.Stderr)
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return value[:index]
	}
	if len(value) > 160 {
		return value[:160] + "…"
	}
	return value
}
