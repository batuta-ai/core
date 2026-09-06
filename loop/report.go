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

// finish records the terminal state and, for final states, writes the
// bookkeeping the doctrine expects: ticks in the plan, WORK.md lines, one
// commit. Non-final states (waiting for an answer, canceled) leave the tree
// untouched so the integration chain can continue on --resume.
func (r *Runner) finish(ctx context.Context, state string) (string, error) {
	r.mu.Lock()
	r.terminal = state
	summary := r.summaryLocked()
	err := r.record(KindTerminal, "", map[string]any{"state": state, "summary": summary})
	r.mu.Unlock()
	if err != nil {
		return state, err
	}
	final := state == StateDone || state == StateBlocked || state == StateAbandoned
	if final {
		if err := r.bookkeeping(ctx, state, summary); err != nil {
			return state, err
		}
	}
	r.printSummary(state, summary)
	return state, nil
}

// Summary is the terminal report of a delivery.
type Summary struct {
	Integrated []SummaryTask `json:"integrated"`
	Blocked    []SummaryTask `json:"blocked"`
	Waiting    []SummaryTask `json:"waiting"`
	Pending    []SummaryTask `json:"pending"`
	Waves      int           `json:"waves"`
}

type SummaryTask struct {
	ID       string `json:"task_id"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Executor string `json:"executor"`
	Model    string `json:"model"`
	Attempts int    `json:"attempts"`
	Commit   string `json:"commit,omitempty"`
	Blocker  string `json:"blocker,omitempty"`
	Question string `json:"question,omitempty"`
	Story    string `json:"story"`
}

func (r *Runner) summaryLocked() Summary {
	summary := Summary{Waves: len(r.graph.Waves)}
	for _, task := range r.graph.Tasks {
		plan := r.planTask(task.TaskID)
		entry := SummaryTask{ID: task.TaskID, Number: plan.Number, Title: plan.Title, Attempts: len(task.Attempts)}
		if len(task.Attempts) > 0 {
			last := task.Attempts[len(task.Attempts)-1]
			entry.Executor, entry.Model = last.Runtime.Provider, last.Runtime.Model
			if last.Question != nil && last.Question.Answer == nil {
				entry.Question = last.Question.Prompt
			}
			entry.Story = routingStory(task.Attempts)
		}
		switch task.State {
		case routing.GraphTaskIntegrated:
			entry.Commit = task.IntegratedCommitSHA
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
		fmt.Fprintf(r.out, "  ⏸ %s %s not run (%s)\n", task.ID, task.Title, pendingReason(task))
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
	planChanged, err := r.tickPlan(summary)
	if err != nil {
		return err
	}
	if err := r.writeWork(summary, state); err != nil {
		return err
	}
	if planChanged {
		args := []string{"add", "-A", "--", r.plan.Path}
		if r.planPath != filepath.Join(r.root, r.plan.Path) {
			args = append(args, filepath.Join(".batuta", "plans"))
		}
		roadmap := filepath.Join(".batuta", "roadmap.md")
		if _, err := os.Stat(filepath.Join(r.root, roadmap)); err == nil {
			args = append(args, roadmap)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := r.git.Runner.Run(ctx, publication.Command{
			Executable: r.git.Git, Args: args, Directory: r.root,
		}); err != nil {
			return fmt.Errorf("loop: stage plan bookkeeping: %w", err)
		}
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
	if err != nil {
		return false, err
	}
	integrated := map[int]bool{}
	for _, task := range summary.Integrated {
		integrated[task.Number] = true
	}
	for _, task := range summary.Blocked {
		if task.Blocker == blockerAlreadySatisfied {
			integrated[task.Number] = true
		}
	}
	lines := strings.Split(string(payload), "\n")
	changed := false
	for index, line := range lines {
		match := planTick.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		number, _ := strconv.Atoi(match[1])
		if integrated[number] {
			lines[index] = "- [x]" + line[5:]
			changed = true
		}
	}
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
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(r.planPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return false, err
	}
	if allDone {
		done := filepath.Join(r.root, ".batuta", "plans", "done", r.plan.Slug+".md")
		if err := os.MkdirAll(filepath.Dir(done), 0o755); err != nil {
			return false, err
		}
		if err := os.Rename(r.planPath, done); err != nil {
			return false, err
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
	return true, nil
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
		if task.Commit == "" {
			continue
		}
		done = append(done, fmt.Sprintf("- [x] %s → %s, commit %s (trail: %s, plan %s, %s)", task.Title, task.Story, short(task.Commit), r.trailRelative(task.ID), r.plan.Slug, date))
	}
	for _, task := range summary.Blocked {
		if task.Blocker == blockerAlreadySatisfied {
			done = append(done, fmt.Sprintf("- [x] %s → %s, already satisfied on the base, no commit (trail: %s, plan %s, %s)", task.Title, task.Story, r.trailRelative(task.ID), r.plan.Slug, date))
			continue
		}
		blocked = append(blocked, fmt.Sprintf("- [ ] %s → %s, aborted: %s (trail: %s, plan %s, %s)", task.Title, task.Story, task.Blocker, r.trailRelative(task.ID), r.plan.Slug, date))
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
	root, store, err := openStore(workspace)
	if err != nil {
		return "", err
	}
	_ = root
	taskID := taskRef
	if _, err := strconv.Atoi(taskRef); err == nil {
		taskID = "task_" + taskRef
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("loop: the answer is empty")
	}
	ids, err := store.List()
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		records, err := store.Read(id)
		if err != nil || len(records) == 0 || terminalState(records) != "" {
			continue
		}
		last := records[len(records)-1]
		var graph routing.DeliveryGraph
		if json.Unmarshal(last.Graph, &graph) != nil {
			continue
		}
		task := graphTask(&graph, taskID)
		if task == nil || task.State != routing.GraphTaskWaitingInput || len(task.Attempts) == 0 {
			continue
		}
		attempt := task.Attempts[len(task.Attempts)-1]
		if attempt.Question == nil {
			continue
		}
		answer := routing.TaskAnswer{
			QuestionOperationID: attempt.Question.RequestID, LoopRunID: attempt.ChildRunID,
			Generation: 1, NodeID: "loop", ItemIndex: 0, Value: text,
		}
		if _, _, err := graph.RecordAnswer(taskID, attempt.Execution, answer, time.Now().UTC()); err != nil {
			return "", fmt.Errorf("loop: record answer: %w", err)
		}
		graphJSON, _ := json.Marshal(graph)
		detail, _ := json.Marshal(map[string]any{"execution": attempt.Execution, "answer": text, "question": attempt.Question.Prompt})
		if _, err := store.Append(id, journal.Record{Kind: KindAnswer, TaskID: taskID, Detail: detail, Graph: graphJSON}); err != nil {
			return "", err
		}
		var opened openedDetail
		_ = json.Unmarshal(records[0].Detail, &opened)
		_ = os.Remove(filepath.Join(root, ".batuta", "asks", opened.Slug+"-"+strings.ReplaceAll(taskID, "_", "-")+".md"))
		return id, nil
	}
	return "", fmt.Errorf("loop: no open delivery has %s waiting for an answer", taskID)
}

// Abandon closes a delivery that will not continue: terminal `abandoned`,
// bookkeeping for whatever integrated, worktrees removed.
func Abandon(ctx context.Context, opts Options) (string, error) {
	r, err := prepare(ctx, opts)
	if err != nil {
		return "", err
	}
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
	if state := terminalState(records); state != "" {
		return "", fmt.Errorf("loop: delivery %s already ended: %s", opts.Resume, state)
	}
	if err := r.loadPlan(opened.Slug); err != nil {
		return "", err
	}
	r.delivery = opts.Resume
	r.generation = opened.Generation
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
	if !opts.KeepWorktrees {
		for _, wt := range r.worktrees {
			_ = r.git.Remove(ctx, wt.Root, wt.Branch)
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
