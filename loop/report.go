package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
	"github.com/batuta-ai/core/worktree"
)

const kindFinalizing journal.Kind = "delivery_finalizing"
const kindCleanup journal.Kind = "delivery_cleanup"

type terminalDetail struct {
	Deletions          []worktree.ParkedRef `json:"pending_ref_deletions,omitempty"`
	BookkeepingPending bool                 `json:"bookkeeping_pending,omitempty"`
	BookkeepingError   string               `json:"bookkeeping_error,omitempty"`
	PlanPath           string               `json:"plan_path,omitempty"`
	State              string               `json:"state"`
	Summary            Summary              `json:"summary"`
	CleanupPending     bool                 `json:"cleanup_pending,omitempty"`
	CleanupError       string               `json:"cleanup_error,omitempty"`
	Worktrees          []attemptWorktree    `json:"retained_worktrees,omitempty"`
}

// finish snapshots executor work and checkpoints recovery before cleanup.
// Parked refs survive until the terminal record contains the retention plan.
func (r *Runner) finish(ctx context.Context, state string) (string, error) {
	if err := r.snapshotWorktrees(ctx); err != nil {
		return state, err
	}
	parked, err := r.git.Parked(ctx, r.plan.Slug)
	if err != nil {
		return state, err
	}
	r.mu.Lock()
	detail := terminalDetail{State: state, Summary: r.summaryLocked()}
	detail.Summary.Parked = parked
	r.mu.Unlock()
	final := state == StateDone || state == StateBlocked || state == StateAbandoned
	if final {
		if !r.opts.KeepWorktrees {
			seen := map[string]bool{}
			for _, wt := range r.worktrees {
				if !seen[wt.Root] {
					detail.Worktrees = append(detail.Worktrees, wt)
					seen[wt.Root] = true
				}
			}
			slices.SortFunc(detail.Worktrees, func(a, b attemptWorktree) int { return strings.Compare(a.Root, b.Root) })
		}
		detail.CleanupPending, detail.BookkeepingPending = true, true
		detail.PlanPath = r.plan.Path
		if err := r.checkpointFinalization(detail); err != nil {
			return state, err
		}
		return r.completeFinalization(ctx, detail)
	}
	return r.recordFinalization(detail, nil)
}

func (r *Runner) checkpointFinalization(detail terminalDetail) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record(kindFinalizing, "", detail); err != nil {
		return err
	}
	r.pendingFinish = &detail
	return nil
}

func (r *Runner) completeFinalization(ctx context.Context, detail terminalDetail) (string, error) {
	var cleanupErr, bookkeepingErr error
	if detail.CleanupPending {
		cleanupErr = r.cleanFinalization(ctx, &detail)
	}
	if detail.BookkeepingPending {
		bookkeepingErr = r.bookkeeping(ctx, detail.State, detail.Summary)
		detail.BookkeepingPending = bookkeepingErr != nil
		detail.BookkeepingError = errorString(bookkeepingErr)
	}
	// A separate completed-bookkeeping checkpoint closes the interruption window
	// between committing and appending the terminal record. Earlier checkpoints
	// can safely repeat bookkeeping when the commit succeeded but was not recorded.
	checkpointErr := r.checkpointFinalization(detail)
	if bookkeepingErr != nil || checkpointErr != nil {
		r.printFinalization(detail)
		if checkpointErr != nil {
			fmt.Fprintf(r.out, "finalization checkpoint: %s\nretry with batuta loop --resume %s or batuta loop --abandon %s\n", checkpointErr, r.delivery, r.delivery)
		}
		return detail.State, errors.Join(cleanupErr, bookkeepingErr, checkpointErr)
	}
	// Resolve retention after bookkeeping, which may add a reachable tree.
	retained, deletions, deletionErr := r.git.PlanParkedCleanup(ctx, r.plan.Slug, r.branch)
	if deletionErr == nil {
		detail.Summary.Parked, detail.Deletions = retained, deletions
	}
	if deletionErr != nil {
		detail.CleanupPending = true
		detail.CleanupError = errorString(errors.Join(cleanupErr, deletionErr))
		checkpointErr := r.checkpointFinalization(detail)
		r.printFinalization(detail)
		return detail.State, errors.Join(cleanupErr, deletionErr, checkpointErr)
	}
	if _, err := r.recordFinalization(detail, nil); err != nil {
		return detail.State, errors.Join(cleanupErr, err)
	}
	if len(detail.Deletions) == 0 {
		return detail.State, cleanupErr
	}
	deletionErr = r.git.DeleteParked(ctx, detail.Deletions)
	detail.Deletions = nil
	if deletionErr != nil {
		parked, listErr := r.git.Parked(ctx, r.plan.Slug)
		if listErr == nil {
			detail.Summary.Parked = parked
		}
		cleanupErr = errors.Join(cleanupErr, deletionErr, listErr)
		detail.CleanupPending = true
		detail.CleanupError = errorString(cleanupErr)
	}
	r.mu.Lock()
	followupErr := r.record(kindCleanup, "", detail)
	if followupErr == nil {
		r.pendingFinish = nil
		if detail.CleanupPending {
			r.pendingFinish = &detail
		}
	}
	r.mu.Unlock()
	if cleanupErr != nil {
		r.printFinalization(detail)
	}
	if followupErr != nil {
		fmt.Fprintf(r.out, "cleanup record: %s\nretry with batuta loop --resume %s\n", followupErr, r.delivery)
	}
	return detail.State, errors.Join(cleanupErr, followupErr)
}

func (r *Runner) cleanFinalization(ctx context.Context, detail *terminalDetail) error {
	var retained []attemptWorktree
	var cleanupErr error
	for _, wt := range detail.Worktrees {
		if err := r.removeWorktree(ctx, wt.Root, wt.Branch); err != nil {
			retained = append(retained, wt)
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("loop: retain worktree %s: %w", wt.Root, err))
		}
	}
	detail.Worktrees = retained
	detail.CleanupPending = cleanupErr != nil
	detail.CleanupError = errorString(cleanupErr)
	return cleanupErr
}

func (r *Runner) recordFinalization(detail terminalDetail, cleanupErr error) (string, error) {
	r.mu.Lock()
	err := r.record(KindTerminal, "", detail)
	if err == nil {
		r.terminal = detail.State
		r.pendingFinish = nil
		if detail.CleanupPending || len(detail.Deletions) > 0 {
			r.pendingFinish = &detail
		}
	}
	r.mu.Unlock()
	r.printFinalization(detail)
	if err != nil {
		fmt.Fprintf(r.out, "terminal record: %s\nretry with batuta loop --resume %s or batuta loop --abandon %s\n", err, r.delivery, r.delivery)
	}
	return detail.State, errors.Join(cleanupErr, err)
}

func (r *Runner) printFinalization(detail terminalDetail) {
	r.printSummary(detail.State, detail.Summary)
	if detail.BookkeepingPending {
		fmt.Fprintf(r.out, "bookkeeping pending: %s\nretry with batuta loop --resume %s or batuta loop --abandon %s\n", detail.BookkeepingError, r.delivery, r.delivery)
	}
	if detail.CleanupPending {
		fmt.Fprintf(r.out, "cleanup pending: %s\nretry with batuta loop --resume %s or --abandon %s\n", detail.CleanupError, r.delivery, r.delivery)
	}
}

func pendingFinalization(records []journal.Record) *terminalDetail {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Kind != KindTerminal && records[i].Kind != kindFinalizing && records[i].Kind != kindCleanup {
			continue
		}
		var detail terminalDetail
		if json.Unmarshal(records[i].Detail, &detail) == nil && (records[i].Kind == kindFinalizing || detail.CleanupPending || len(detail.Deletions) > 0) {
			return &detail
		}
		return nil
	}
	return nil
}

func (r *Runner) restoreFinalization(records []journal.Record, opened openedDetail, detail *terminalDetail) error {
	if r.branch != opened.Branch {
		return fmt.Errorf("loop: delivery %s runs on branch %s; %s is checked out", r.opts.Resume, opened.Branch, r.branch)
	}
	var graph routing.DeliveryGraph
	if err := json.Unmarshal(records[len(records)-1].Graph, &graph); err != nil {
		return err
	}
	r.graph = &graph
	r.delivery, r.plan.Slug = r.opts.Resume, opened.Slug
	r.plan.Path = detail.PlanPath
	r.planPath = filepath.Join(r.root, detail.PlanPath)
	r.journaled, r.pendingFinish = true, detail
	return nil
}

func (r *Runner) retryFinalization(ctx context.Context) (string, error) {
	return r.completeFinalization(ctx, *r.pendingFinish)
}

func (r *Runner) snapshotWorktrees(ctx context.Context) error {
	// Retries can bind several executions to one directory. Visit the latest
	// binding first so a terminal snapshot names the execution that wrote it.
	r.mu.Lock()
	keys := make([]string, 0, len(r.worktrees))
	for key := range r.worktrees {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	slices.Reverse(keys)
	worktrees := make(map[string]attemptWorktree, len(keys))
	for _, key := range keys {
		worktrees[key] = r.worktrees[key]
	}
	r.mu.Unlock()
	seen := map[string]bool{}
	for _, key := range keys {
		wt := worktrees[key]
		if seen[wt.Root] {
			continue
		}
		seen[wt.Root] = true
		taskID, execution, _ := strings.Cut(key, ":")
		n, err := strconv.Atoi(execution)
		if err != nil {
			return err
		}
		if err := r.snapshotWorktree(ctx, taskID, n, wt); err != nil {
			return err
		}
	}
	return nil
}

// Summary is the terminal report of a delivery.
type Summary struct {
	Parked     []worktree.ParkedRef `json:"parked,omitempty"`
	Integrated []SummaryTask        `json:"integrated"`
	Blocked    []SummaryTask        `json:"blocked"`
	Waiting    []SummaryTask        `json:"waiting"`
	Pending    []SummaryTask        `json:"pending"`
	Waves      int                  `json:"waves"`
}

type SummaryTask struct {
	ID        string `json:"task_id"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Executor  string `json:"executor"`
	Model     string `json:"model"`
	Attempts  int    `json:"attempts"`
	Commit    string `json:"commit,omitempty"`
	Base      string `json:"base,omitempty"`
	Satisfied bool   `json:"satisfied,omitempty"`
	Blocker   string `json:"blocker,omitempty"`
	Question  string `json:"question,omitempty"`
	Story     string `json:"story"`
	Worktree  string `json:"worktree,omitempty"`
}

func (r *Runner) summaryLocked() Summary {
	summary := Summary{Waves: len(r.graph.Waves)}
	for _, task := range r.graph.Tasks {
		plan := r.planTask(task.TaskID)
		entry := SummaryTask{ID: task.TaskID, Number: plan.Number, Title: plan.Title, Attempts: len(task.Attempts)}
		if len(task.Attempts) > 0 {
			last := task.Attempts[len(task.Attempts)-1]
			entry.Executor, entry.Model = last.Runtime.Provider, last.Runtime.Model
			entry.Worktree = r.worktrees[attemptKey(task.TaskID, last.Execution)].Root
			if entry.Worktree == "" {
				entry.Worktree = last.WorktreeRoot
			}
			if last.Question != nil && last.Question.Answer == nil {
				entry.Question = last.Question.Prompt
			}
			entry.Story = routingStory(task.Attempts)
		}
		switch task.State {
		case routing.GraphTaskIntegrated:
			entry.Commit = task.IntegratedCommitSHA
			entry.Satisfied = task.AlreadySatisfied
			if entry.Satisfied {
				entry.Base = task.IntegratedCommitSHA
			}
			if entry.Commit == "" {
				entry.Commit = r.commits[task.TaskID]
			}
			if len(task.Attempts) == 0 {
				entry.Story = "ticked in the plan before the run"
			}
			summary.Integrated = append(summary.Integrated, entry)
		case routing.GraphTaskBlocked:
			entry.Blocker = task.BlockerCode
			summary.Blocked = append(summary.Blocked, entry)
		case routing.GraphTaskWaitingInput:
			summary.Waiting = append(summary.Waiting, entry)
		default:
			summary.Pending = append(summary.Pending, entry)
		}
	}
	return summary
}

// routingStory tells how a task travelled: executor, retries, escalation.
func routingStory(attempts []routing.GraphTaskAttempt) string {
	if len(attempts) == 0 {
		return ""
	}
	first := attempts[0].Runtime
	last := attempts[len(attempts)-1].Runtime
	story := first.Provider + " (" + first.Model + ")"
	retries := 0
	for index := 1; index < len(attempts); index++ {
		if attempts[index].Runtime == attempts[index-1].Runtime && attempts[index].RunExecution != attempts[index-1].RunExecution {
			retries++
		}
	}
	if last != first {
		story = last.Provider + " (" + last.Model + "), escalated from " + first.Provider + " after " + strconv.Itoa(retries+1) + " fails"
	} else if retries > 0 {
		story += fmt.Sprintf(", %d retry", retries)
	}
	return story
}

func (r *Runner) printSummary(state string, summary Summary) {
	fmt.Fprintf(r.out, "\ndelivery %s: %s (%d waves)\n", r.delivery, state, summary.Waves)
	for _, task := range summary.Integrated {
		if task.Satisfied {
			fmt.Fprintf(r.out, "  ✅ %s %s → already satisfied on the base %s, no commit\n", task.ID, task.Title, short(task.Base))
			continue
		}
		if task.Commit != "" {
			fmt.Fprintf(r.out, "  ✅ %s %s → %s, commit %s\n", task.ID, task.Title, task.Story, short(task.Commit))
		}
	}
	for _, task := range summary.Blocked {
		if task.Blocker == blockerAlreadySatisfied {
			fmt.Fprintf(r.out, "  ✅ %s %s → already satisfied on the base, no commit\n", task.ID, task.Title)
			continue
		}
		fmt.Fprintf(r.out, "  ❌ %s %s → %s, aborted: %s\n", task.ID, task.Title, task.Story, task.Blocker)
	}
	for _, task := range summary.Waiting {
		fmt.Fprintf(r.out, "  ❓ %s %s asks: %s\n     batuta loop --answer %s \"<text>\"\n", task.ID, task.Title, task.Question, task.ID)
	}
	for _, task := range summary.Pending {
		worktree := ""
		if task.Worktree != "" {
			worktree = ", worktree " + task.Worktree
		}
		fmt.Fprintf(r.out, "  ⏸ %s %s not run (%s%s)\n", task.ID, task.Title, pendingReason(task), worktree)
	}
	for _, ref := range summary.Parked {
		fmt.Fprintf(r.out, "  %s %s kept (unmerged work)\n", ref.Ref, ref.SHA)
		if len(ref.Conflicts) > 0 {
			fmt.Fprintf(r.out, "    conflicted paths: %q\n", ref.Conflicts)
		}
	}
	fmt.Fprintf(r.out, "journal   %s\n", filepath.Join(journal.Dir, r.delivery+".jsonl"))
}

func pendingReason(task SummaryTask) string {
	if task.Attempts == 0 {
		return "dependency blocked or run ended first"
	}
	return "interrupted"
}

// bookkeeping ticks integrated tasks in the plan, sets Status: done when
// every task is integrated, appends the WORK.md lines and commits them.
func (r *Runner) bookkeeping(ctx context.Context, state string, summary Summary) error {
	// Recovery bypasses the general clean-tree preflight. Check before touching
	// bookkeeping files so refusing a foreign staged path preserves the index.
	staged, err := r.git.Runner.Run(ctx, publication.Command{
		Executable: r.git.Git, Args: []string{"diff", "--cached", "--name-only", "--no-renames", "-z", "--"}, Directory: r.root,
	})
	if err != nil {
		return err
	}
	if staged.ExitCode != 0 || staged.StdoutTruncated || staged.StderrTruncated {
		return errors.New("loop: cannot inspect staged bookkeeping paths")
	}
	allowed := map[string]bool{"WORK.md": true, ".batuta/roadmap.md": true, filepath.ToSlash(r.plan.Path): true, ".batuta/plans/done/" + r.plan.Slug + ".md": true}
	current, err := filepath.Rel(r.root, r.planPath)
	if err != nil {
		return err
	}
	allowed[filepath.ToSlash(current)] = true
	for _, path := range strings.Split(string(staged.Stdout), "\x00") {
		if path != "" && !allowed[path] {
			return fmt.Errorf("loop: staged path outside bookkeeping: %q; unstage it before retrying", path)
		}
	}

	_, err = r.tickPlan(summary)
	if err != nil {
		return err
	}
	if err := r.writeWork(summary, state); err != nil {
		return err
	}
	// Stage even when retrying an already ticked plan. The source may already
	// be archived, or its removal may already have been committed.
	paths := []string{r.planPath}
	original := filepath.Join(r.root, r.plan.Path)
	if original != r.planPath {
		tracked, err := r.git.Runner.Run(ctx, publication.Command{
			Executable: r.git.Git, Args: []string{"ls-files", "-z", "--", r.plan.Path}, Directory: r.root,
		})
		if err != nil {
			return err
		}
		if len(tracked.Stdout) > 0 {
			paths = append(paths, original)
		}
	}
	roadmap := filepath.Join(r.root, ".batuta", "roadmap.md")
	if _, err := os.Stat(roadmap); err == nil {
		paths = append(paths, roadmap)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	args := append([]string{"add", "-A", "--"}, paths...)
	if _, err := r.git.Runner.Run(ctx, publication.Command{
		Executable: r.git.Git, Args: args, Directory: r.root,
	}); err != nil {
		return fmt.Errorf("loop: stage plan bookkeeping: %w", err)
	}
	message := fmt.Sprintf("chore(batuta): %s — loop %s\n\n%d integrated, %d blocked. Delivery %s.\n", r.plan.Slug, state, len(summary.Integrated), len(summary.Blocked), r.delivery)
	if _, err := r.git.Commit(ctx, message, "WORK.md"); err != nil {
		return fmt.Errorf("loop: bookkeeping commit: %w", err)
	}
	return nil
}

var (
	planTick   = regexp.MustCompile(`^- \[ \] ([0-9]+)\.`)
	planStatus = regexp.MustCompile(`\*\*Status:\*\*\s*(proposed|approved|in progress|done)`)
)

func (r *Runner) tickPlan(summary Summary) (bool, error) {
	payload, err := os.ReadFile(r.planPath)
	if errors.Is(err, os.ErrNotExist) {
		// Archival may have succeeded before staging or journaling failed.
		r.planPath = filepath.Join(r.root, ".batuta", "plans", "done", r.plan.Slug+".md")
		payload, err = os.ReadFile(r.planPath)
	}
	if err != nil {
		return false, err
	}
	integrated := map[int]bool{}
	satisfied := map[int]string{}
	for _, task := range summary.Integrated {
		integrated[task.Number] = true
		if task.Satisfied {
			satisfied[task.Number] = task.Base
		}
	}
	for _, task := range summary.Blocked {
		if task.Blocker == blockerAlreadySatisfied {
			integrated[task.Number] = true
		}
	}
	lines := strings.Split(string(payload), "\n")
	rendered := make([]string, 0, len(lines)+len(satisfied))
	changed := false
	for _, line := range lines {
		match := planTick.FindStringSubmatch(line)
		if match == nil {
			rendered = append(rendered, line)
			continue
		}
		number, _ := strconv.Atoi(match[1])
		if integrated[number] {
			line = "- [x]" + line[5:]
			changed = true
		}
		rendered = append(rendered, line)
		if base := satisfied[number]; base != "" {
			rendered = append(rendered, "      Result: already satisfied on the base "+short(base)+", no commit")
			changed = true
		}
	}
	lines = rendered
	allDone := len(summary.Waiting) == 0 && len(summary.Pending) == 0
	for _, task := range summary.Blocked {
		if task.Blocker != blockerAlreadySatisfied {
			allDone = false
		}
	}
	if allDone {
		for index, line := range lines {
			if planStatus.MatchString(line) && !strings.Contains(line, "**Status:** done") {
				lines[index] = planStatus.ReplaceAllString(line, "**Status:** done")
				changed = true
				break
			}
		}
	}
	if changed {
		if err := os.WriteFile(r.planPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return false, err
		}
	}
	if allDone {
		done := filepath.Join(r.root, ".batuta", "plans", "done", r.plan.Slug+".md")
		if err := os.MkdirAll(filepath.Dir(done), 0o755); err != nil {
			return false, err
		}
		if r.planPath != done {
			if err := os.Rename(r.planPath, done); err != nil {
				return false, err
			}
			changed = true
		}
		r.planPath = done
		roadmap := filepath.Join(r.root, ".batuta", "roadmap.md")
		if _, err := os.Stat(roadmap); err == nil {
			if err := routing.TickPhase(roadmap, r.plan.Slug); err != nil {
				return false, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return changed, nil
}

func (r *Runner) writeWork(summary Summary, state string) error {
	path := filepath.Join(r.root, "WORK.md")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := string(existing)
	if strings.TrimSpace(content) == "" {
		content = "# WORK — " + filepath.Base(r.root) + "\n\n## In progress\n\n## Done\n"
	}
	date := r.now().Format("2006-01-02")
	var done, blocked []string
	for _, task := range summary.Integrated {
		if strings.Contains(content, "(trail: "+r.trailRelative(task.ID)+", delivery "+r.delivery+",") {
			continue
		}
		if task.Satisfied {
			done = append(done, fmt.Sprintf("- [x] %s → %s, already satisfied on the base %s, no commit (trail: %s, delivery %s, plan %s, %s)", task.Title, task.Story, short(task.Base), r.trailRelative(task.ID), r.delivery, r.plan.Slug, date))
			continue
		}
		if task.Commit == "" {
			continue
		}
		done = append(done, fmt.Sprintf("- [x] %s → %s, commit %s (trail: %s, delivery %s, plan %s, %s)", task.Title, task.Story, short(task.Commit), r.trailRelative(task.ID), r.delivery, r.plan.Slug, date))
	}
	for _, task := range summary.Blocked {
		if strings.Contains(content, "(trail: "+r.trailRelative(task.ID)+", delivery "+r.delivery+",") {
			continue
		}
		if task.Blocker == blockerAlreadySatisfied {
			done = append(done, fmt.Sprintf("- [x] %s → %s, already satisfied on the base, no commit (trail: %s, delivery %s, plan %s, %s)", task.Title, task.Story, r.trailRelative(task.ID), r.delivery, r.plan.Slug, date))
			continue
		}
		blocked = append(blocked, fmt.Sprintf("- [ ] %s → %s, aborted: %s (trail: %s, delivery %s, plan %s, %s)", task.Title, task.Story, task.Blocker, r.trailRelative(task.ID), r.delivery, r.plan.Slug, date))
	}
	content = appendUnderHeading(content, "## Done", done)
	if len(blocked) > 0 {
		content = appendUnderHeading(content, "## Blocked", blocked)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// appendUnderHeading adds lines at the end of a `## heading` section,
// creating the section when the file has none.
func appendUnderHeading(content, heading string, lines []string) string {
	if len(lines) == 0 {
		return content
	}
	block := strings.Join(lines, "\n") + "\n"
	start := strings.Index(content, heading+"\n")
	if start < 0 {
		start = strings.Index(content, heading)
	}
	if start < 0 {
		return strings.TrimRight(content, "\n") + "\n\n" + heading + "\n" + block
	}
	after := start + len(heading)
	next := strings.Index(content[after:], "\n## ")
	if next < 0 {
		return strings.TrimRight(content, "\n") + "\n" + block
	}
	end := after + next
	section := strings.TrimRight(content[:end], "\n")
	return section + "\n" + block + content[end:]
}

// Run trails: one file per task under .batuta/runs/, appended per attempt.

func (r *Runner) trailPath(taskID string) string {
	return filepath.Join(r.root, ".batuta", "runs", r.trailName(taskID))
}

func (r *Runner) trailRelative(taskID string) string {
	return filepath.ToSlash(filepath.Join(".batuta", "runs", r.trailName(taskID)))
}

func (r *Runner) trailName(taskID string) string {
	return r.deliveryDate() + "-" + r.plan.Slug + "-" + strings.ReplaceAll(taskID, "_", "-") + ".md"
}

func (r *Runner) deliveryDate() string {
	if parts := strings.Split(r.delivery, "-"); len(parts) >= 2 {
		if stamp := parts[len(parts)-2]; len(stamp) == 8 {
			return stamp[:4] + "-" + stamp[4:6] + "-" + stamp[6:]
		}
	}
	return r.now().Format("2006-01-02")
}

func (r *Runner) writeTrail(ac attemptContext, brief string, result executor.Result, report gates.Report, verdict string) {
	path := r.trailPath(ac.taskID)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	var b strings.Builder
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(&b, "# Run — %s\n\n**Date:** %s · **Lane:** %s/%s · **Plan:** %s (%s) · **Delivery:** %s\n\n", ac.plan.Title, r.now().Format("2006-01-02"), ac.plan.Domain, ac.plan.Complexity, r.plan.Slug, ac.taskID, r.delivery)
	}
	fmt.Fprintf(&b, "## Attempt %d — %s/%s\n\n", ac.execution, ac.adapter.Name, ac.runtime.Model)
	fmt.Fprintf(&b, "**Worktree:** %s · **Base:** %s · **Exit:** %d · **Duration:** %s\n\n", ac.worktree.Root, short(ac.base), result.ExitCode, result.Duration.Round(time.Second))
	b.WriteString("### Brief\n\n```markdown\n" + strings.ReplaceAll(brief, "```", "~~~") + "\n```\n\n")
	b.WriteString("### Executor report\n\n```\n" + strings.ReplaceAll(executor.Tail(result.Stdout, 200), "```", "~~~") + "\n```\n\n")
	b.WriteString("### Verification\n\n")
	for _, proof := range report.Proofs {
		fmt.Fprintf(&b, "- %s\n", proof.Signal)
	}
	fmt.Fprintf(&b, "- Gates: %s\n", report.Summary())
	if report.Verifier != nil {
		fmt.Fprintf(&b, "- Verifier: %s\n", report.Verifier.Signal)
		if report.Verifier.Detail != "" {
			b.WriteString("\n```\n" + report.Verifier.Detail + "\n```\n")
		}
	}
	if !report.Passed {
		b.WriteString("\n### Failures\n\n")
		for _, failure := range report.Failures() {
			b.WriteString("- " + strings.ReplaceAll(failure, "\n", "\n  ") + "\n")
		}
	}
	if verdict != "" {
		fmt.Fprintf(&b, "\n**Verdict:** %s\n", verdict)
	}
	b.WriteString("\n")
	appendFile(path, b.String())
}

func (r *Runner) writeTrailVerdict(taskID, verdict string, feedback []string) {
	path := r.trailPath(taskID)
	var b strings.Builder
	fmt.Fprintf(&b, "**Verdict:** %s\n", verdict)
	for _, line := range feedback {
		b.WriteString("- " + strings.ReplaceAll(line, "\n", "\n  ") + "\n")
	}
	b.WriteString("\n")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	appendFile(path, b.String())
}

func appendFile(path, content string) {
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer handle.Close()
	_, _ = handle.WriteString(content)
}

// openDeliveries lists the deliveries of a plan that have not ended.
func (r *Runner) openDeliveries(slug string) []string {
	ids, err := r.store.List()
	if err != nil {
		return nil
	}
	var open []string
	for _, id := range ids {
		records, err := r.store.Read(id)
		if err != nil || len(records) == 0 || records[0].Kind != KindOpened {
			continue
		}
		var opened openedDetail
		if json.Unmarshal(records[0].Detail, &opened) != nil || opened.Slug != slug {
			continue
		}
		if terminalState(records) == "" {
			open = append(open, id)
		}
	}
	return open
}

func terminalState(records []journal.Record) string {
	if pendingFinalization(records) != nil {
		return ""
	}
	state := ""
	for _, record := range records {
		if record.Kind != KindTerminal {
			continue
		}
		var detail struct {
			State string `json:"state"`
		}
		if json.Unmarshal(record.Detail, &detail) == nil {
			state = detail.State
		}
	}
	if state == StateWaitingInput || state == StateCanceled {
		return ""
	}
	return state
}

// Answer records the human's answer to a parked task and returns the
// delivery to resume. taskRef is `task_N` or `N`.
func Answer(workspace, taskRef, text string) (string, error) {
	return answer(workspace, taskRef, text, time.Now().UTC())
}

// AnswerDelivery records an answer only for the specified delivery and question.
func AnswerDelivery(workspace, delivery, task, questionID, text string) (string, error) {
	return answerDelivery(workspace, delivery, task, 0, questionID, text, time.Now().UTC())
}

func answerDelivery(workspace, delivery, task string, execution int, questionID, text string, now time.Time) (string, error) {
	if !journal.ValidDeliveryID(delivery) || strings.TrimSpace(questionID) == "" {
		return "", errors.New("loop: the answer requires a delivery and question ID")
	}
	return answerSelected(workspace, delivery, task, execution, questionID, text, now)
}

func answer(workspace, taskRef, text string, now time.Time) (string, error) {
	return answerSelected(workspace, "", taskRef, 0, "", text, now)
}

func answerSelected(workspace, delivery, taskRef string, execution int, questionID, text string, now time.Time) (string, error) {
	root, store, err := openStore(workspace)
	if err != nil {
		return "", err
	}
	_ = root
	if delivery != "" {
		if state, _ := Presence(root, delivery, now); state == "running" {
			return "", errors.New("loop still running · wait for waiting_input")
		}
	}
	taskID := taskRef
	if _, err := strconv.Atoi(taskRef); err == nil {
		taskID = "task_" + taskRef
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("loop: the answer is empty")
	}
	var ids []string
	if delivery != "" {
		ids = []string{delivery}
	} else {
		ids, err = store.List()
		if err != nil {
			return "", err
		}
	}
	blockedAtCeiling := false
	for _, id := range ids {
		records, err := store.Read(id)
		if err != nil || len(records) == 0 {
			continue
		}
		last := records[len(records)-1]
		var graph routing.DeliveryGraph
		if json.Unmarshal(last.Graph, &graph) != nil {
			continue
		}
		task := graphTask(&graph, taskID)
		if task != nil && task.State == routing.GraphTaskBlocked && task.BlockerCode == routing.BlockerQuestionAtCeiling &&
			len(task.Attempts) > 0 && task.Attempts[len(task.Attempts)-1].Question != nil {
			blockedAtCeiling = true
			continue
		}
		if terminalState(records) != "" {
			continue
		}
		if task == nil || task.State != routing.GraphTaskWaitingInput || len(task.Attempts) == 0 {
			continue
		}
		attempt := task.Attempts[len(task.Attempts)-1]
		if attempt.Question == nil {
			continue
		}
		ownership, err := acquireDeliveryOwnership(context.Background(), root, id, now)
		if err != nil {
			if state, _ := Presence(root, id, now); delivery != "" && state == "running" {
				return "", errors.New("loop still running · wait for waiting_input")
			}
			return "", err
		}
		records, err = store.Read(id)
		if err != nil {
			return "", errors.Join(err, ownership.stop())
		}
		last = records[len(records)-1]
		graph = routing.DeliveryGraph{}
		if json.Unmarshal(last.Graph, &graph) != nil {
			if err := ownership.stop(); err != nil {
				return "", err
			}
			continue
		}
		task = graphTask(&graph, taskID)
		if terminalState(records) != "" || task == nil || task.State != routing.GraphTaskWaitingInput || len(task.Attempts) == 0 {
			if err := ownership.stop(); err != nil {
				return "", err
			}
			continue
		}
		attempt = task.Attempts[len(task.Attempts)-1]
		if attempt.Question == nil {
			if err := ownership.stop(); err != nil {
				return "", err
			}
			continue
		}
		if delivery != "" && (attempt.Question.RequestID != questionID || (execution != 0 && attempt.Execution != execution)) {
			return "", errors.Join(errors.New("loop: the shown question is no longer waiting for an answer"), ownership.stop())
		}
		answer := routing.TaskAnswer{
			QuestionOperationID: attempt.Question.RequestID, LoopRunID: attempt.ChildRunID,
			Generation: 1, NodeID: "loop", ItemIndex: 0, Value: text,
		}
		if _, _, err := graph.RecordAnswer(taskID, attempt.Execution, answer, now); err != nil {
			return "", errors.Join(fmt.Errorf("loop: record answer: %w", err), ownership.stop())
		}
		graphJSON, _ := json.Marshal(graph)
		detail, _ := json.Marshal(map[string]any{"execution": attempt.Execution, "answer": text, "question": attempt.Question.Prompt})
		if _, err := store.Append(id, journal.Record{Kind: KindAnswer, TaskID: taskID, Detail: detail, Graph: graphJSON}); err != nil {
			return "", errors.Join(err, ownership.stop())
		}
		var opened openedDetail
		_ = json.Unmarshal(records[0].Detail, &opened)
		_ = os.Remove(filepath.Join(root, ".batuta", "asks", opened.Slug+"-"+strings.ReplaceAll(taskID, "_", "-")+".md"))
		return id, ownership.stop()
	}
	if blockedAtCeiling {
		return "", fmt.Errorf("loop: %s is blocked at the execution ceiling; answer the question in a new plan or an interactive cycle", taskID)
	}
	return "", fmt.Errorf("loop: no open delivery has %s waiting for an answer", taskID)
}

// Abandon closes a delivery that will not continue: terminal `abandoned`,
// bookkeeping for whatever integrated, worktrees removed.
func Abandon(ctx context.Context, opts Options) (state string, abandonErr error) {
	r, err := prepare(ctx, opts)
	if err != nil {
		return "", err
	}
	ownership, err := acquireDeliveryOwnership(ctx, r.root, opts.Resume, r.now(), presenceTiming{now: r.now, sleep: r.sleep})
	if err != nil {
		return "", err
	}
	r.ownership = ownership
	defer func() { abandonErr = errors.Join(abandonErr, r.releaseOwnership()) }()
	records, err := r.store.Read(opts.Resume)
	if err != nil {
		return "", fmt.Errorf("loop: %w", err)
	}
	if len(records) == 0 || records[0].Kind != KindOpened {
		return "", errors.New("loop: the journal does not start with delivery_opened")
	}
	var opened openedDetail
	if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
		return "", err
	}
	if detail := pendingFinalization(records); detail != nil {
		if err := r.restoreFinalization(records, opened, detail); err != nil {
			return "", err
		}
		return r.retryFinalization(ctx)
	}
	if state := terminalState(records); state != "" {
		return "", fmt.Errorf("loop: delivery %s already ended: %s", opts.Resume, state)
	}
	if err := r.loadPlan(opened.Slug); err != nil {
		return "", err
	}
	r.delivery = opts.Resume
	r.generation = opened.Generation
	r.branch = opened.Branch
	var graph routing.DeliveryGraph
	if err := json.Unmarshal(records[len(records)-1].Graph, &graph); err != nil {
		return "", err
	}
	r.graph = &graph
	for _, record := range records {
		if record.Kind == KindWorktree {
			var detail struct {
				Execution int             `json:"execution"`
				Worktree  attemptWorktree `json:"worktree"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil {
				r.worktrees[attemptKey(record.TaskID, detail.Execution)] = detail.Worktree
			}
		}
	}
	if entries, err := r.git.Status(ctx, r.root, false); err == nil && len(entries) > 0 {
		return "", fmt.Errorf("%w: commit or stash before abandoning (the bookkeeping commit needs a clean tree)", worktree.ErrDirty)
	}
	return r.finish(ctx, StateAbandoned)
}

// Dashboard prints the state of every task of the open deliveries (or of
// one delivery) as TSV: one writer per file, readable while the loop runs.
func Dashboard(workspace, delivery string, w io.Writer) error {
	_, store, err := openStore(workspace)
	if err != nil {
		return err
	}
	ids := []string{delivery}
	if delivery == "" {
		ids, err = store.List()
		if err != nil {
			return err
		}
	}
	table := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(table, "delivery\tstate\ttask\ttask_state\texecutor/model\texec\tworktree\tupdated")
	shown := 0
	for _, id := range ids {
		records, err := store.Read(id)
		if err != nil || len(records) == 0 {
			continue
		}
		state := terminalState(records)
		if state == "" {
			state = "open"
			if s := lastTerminal(records); s != "" {
				state = s
			}
		}
		if delivery == "" && state != "open" && state != StateWaitingInput && state != StateCanceled {
			continue
		}
		last := records[len(records)-1]
		var graph routing.DeliveryGraph
		if json.Unmarshal(last.Graph, &graph) != nil {
			continue
		}
		for _, task := range graph.Tasks {
			runtime, execution, wt := "", 0, ""
			if len(task.Attempts) > 0 {
				attempt := task.Attempts[len(task.Attempts)-1]
				runtime = attempt.Runtime.Provider + "/" + attempt.Runtime.Model
				execution = attempt.Execution
				wt = attempt.WorktreeID
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", id, state, task.TaskID, task.State, runtime, execution, wt, last.At.Local().Format("15:04:05"))
		}
		shown++
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if shown == 0 {
		fmt.Fprintln(w, "no open deliveries")
	}
	return nil
}

func lastTerminal(records []journal.Record) string {
	for index := len(records) - 1; index >= 0; index-- {
		if records[index].Kind == KindTerminal {
			var detail struct {
				State string `json:"state"`
			}
			if json.Unmarshal(records[index].Detail, &detail) == nil {
				return detail.State
			}
		}
	}
	return ""
}

// Trail prints a delivery's journal as one line per record.
func Trail(workspace, delivery string, w io.Writer) error {
	_, store, err := openStore(workspace)
	if err != nil {
		return err
	}
	if delivery == "" {
		ids, err := store.List()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return errors.New("loop: no deliveries journaled under " + journal.Dir)
		}
		delivery = ids[0]
	}
	records, err := store.Read(delivery)
	if err != nil {
		return fmt.Errorf("loop: %w", err)
	}
	for _, record := range records {
		summary := recordSummary(record)
		fmt.Fprintf(w, "%3d  %s  %-22s %-8s %s\n", record.Seq, record.At.Local().Format("15:04:05"), record.Kind, record.TaskID, summary)
	}
	return nil
}

func recordSummary(record journal.Record) string {
	var detail map[string]any
	if json.Unmarshal(record.Detail, &detail) != nil {
		return ""
	}
	pick := func(keys ...string) string {
		var parts []string
		for _, key := range keys {
			if value, present := detail[key]; present && value != nil && value != "" && value != false {
				switch typed := value.(type) {
				case float64:
					parts = append(parts, fmt.Sprintf("%s=%v", key, typed))
				case []any:
					items := make([]string, 0, len(typed))
					for _, item := range typed {
						items = append(items, fmt.Sprint(item))
					}
					parts = append(parts, key+"="+strings.Join(items, ","))
				default:
					text := fmt.Sprint(typed)
					if len(text) > 80 {
						text = text[:80] + "…"
					}
					parts = append(parts, key+"="+text)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	switch record.Kind {
	case KindOpened:
		summary := pick("slug", "branch", "head", "parallel")
		var opened openedDetail
		if json.Unmarshal(record.Detail, &opened) == nil && opened.Phase > 0 && opened.PhaseTitle != "" {
			summary += fmt.Sprintf(" phase %d · %s", opened.Phase, opened.PhaseTitle)
		}
		return summary
	case KindWave:
		return pick("wave", "tasks")
	case KindStarted:
		return pick("execution", "executor", "model", "reasoning")
	case KindFinished:
		return pick("execution", "exit_code", "duration_ms", "tree_changed", "question")
	case KindProgress:
		return pick("execution", "criterion", "state")
	case KindGates:
		var report gates.Report
		if json.Unmarshal(record.Detail, &report) != nil {
			return ""
		}
		summary := fmt.Sprintf("e%d passed=%t", report.Execution, report.Passed)
		var failures []string
		for _, failure := range report.Failures() {
			failures = append(failures, strings.Join(strings.Fields(strings.TrimPrefix(failure, "gate ")), " "))
		}
		if len(failures) > 0 {
			summary += " (" + strings.Join(failures, "; ") + ")"
		}
		return summary
	case KindCandidate:
		return pick("execution", "commit")
	case KindSnapshot:
		return pick("execution", "sha", "ref")
	case KindFailure:
		return pick("execution", "blocker", "blocked", "same_runtime")
	case KindSettled:
		return pick("wave", "disposition", "conflict_task", "final_head")
	case KindTerminal:
		return pick("state")
	case KindQuestion:
		return pick("question")
	case KindAnswer:
		return pick("answer")
	default:
		return pick("wave", "execution", "state", "reason", "error")
	}
}

func openStore(workspace string) (string, *journal.Store, error) {
	root := workspace
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		root = cwd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	store, err := journal.Open(abs)
	if err != nil {
		return "", nil, err
	}
	return abs, store, nil
}
