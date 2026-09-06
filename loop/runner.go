package loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/integration"
	"github.com/batuta-ai/core/inventory"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
	"github.com/batuta-ai/core/worktree"
)

// Journal kinds written by the loop.
const (
	KindOpened      journal.Kind = "delivery_opened"
	KindWave        journal.Kind = "wave_admitted"
	KindAttempts    journal.Kind = "attempts_begun"
	KindWorktree    journal.Kind = "worktree_attached"
	KindStarted     journal.Kind = "executor_started"
	KindFinished    journal.Kind = "executor_finished"
	KindQuestion    journal.Kind = "question_recorded"
	KindAnswer      journal.Kind = "answer_recorded"
	KindGates       journal.Kind = "gates_reported"
	KindCandidate   journal.Kind = "candidate_recorded"
	KindFailure     journal.Kind = "failure_recorded"
	KindPreflight   journal.Kind = "integration_preflight"
	KindSettled     journal.Kind = "wave_settled"
	KindCleanup     journal.Kind = "cleanup"
	KindTerminal    journal.Kind = "delivery_terminal"
	KindInterrupted journal.Kind = "run_interrupted"
	KindLimitWait   journal.Kind = "limit_wait"
)

// Terminal states of a delivery.
const (
	StateDone         = "done"
	StateBlocked      = "blocked"
	StateWaitingInput = "waiting_input"
	StateCanceled     = "canceled"
	StateAbandoned    = "abandoned"
)

// ErrStopped is returned when --max-waves ended the run early; the delivery
// is not terminal and `--resume` continues it. Tests use it to simulate a
// kill between waves.
var ErrStopped = errors.New("loop: stopped after the requested number of waves")

// Options configure one run.
type Options struct {
	Workspace     string
	Skills        string
	Plan          string // path (.batuta/plan-<slug>.md) or slug
	Resume        string // delivery to continue
	Parallel      int    // 0 → the profile's Execution line
	TaskTimeout   time.Duration
	TestTimeout   time.Duration
	MaxWaves      int
	KeepWorktrees bool
	// Usage limits: how many consecutive waits one attempt may take, the
	// wait when the output names no reset time, and the buffer after a
	// named reset.
	MaxLimitWaits    int
	LimitWaitDefault time.Duration
	LimitBuffer      time.Duration
	Sleep            func(context.Context, time.Duration) error
	Stdout           io.Writer
	Inventory        func(context.Context) (inventory.InventorySnapshot, error)
	Runner           publication.CommandRunner
	Environment      []string // extra environment for executors (tests)
	Now              func() time.Time
}

// Runner holds one delivery in flight.
type Runner struct {
	opts       Options
	root       string
	git        worktree.GitProvider
	gitState   publication.GitClient
	integ      integration.GitClient
	store      *journal.Store
	profile    Profile
	skills     string
	plan       routing.Plan
	planPath   string
	table      routing.RoutingTable
	generation routing.RoutingGeneration
	graph      *routing.DeliveryGraph
	delivery   string
	branch     string
	openedHead string
	parallel   int
	shell      gates.ShellRunner
	subprocess executor.Subprocess
	adapters   map[string]executor.Adapter
	sections   []string
	missing    []string
	now        func() time.Time
	out        io.Writer

	mu         sync.Mutex
	worktrees  map[string]attemptWorktree // task:execution
	feedback   map[string][]string
	candidates map[string]integration.CandidateEvidence
	commits    map[string]string
	started    map[string]bool
	preflights map[string]integration.PreflightResult // operation id → pending preflight
	terminal   string
	wavesRun   int
	warnings   []string
	journaled  bool
}

type attemptWorktree struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
	Root   string `json:"root"`
	Fresh  bool   `json:"fresh"`
}

type openedDetail struct {
	Slug       string                    `json:"slug"`
	PlanPath   string                    `json:"plan_path"`
	PlanDigest string                    `json:"plan_digest"`
	Branch     string                    `json:"branch"`
	Head       string                    `json:"head"`
	Parallel   int                       `json:"parallel"`
	Workspace  string                    `json:"workspace"`
	Generation routing.RoutingGeneration `json:"generation"`
	Tasks      []taskSummary             `json:"tasks"`
}

type taskSummary struct {
	ID         string `json:"task_id"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Domain     string `json:"domain"`
	Complexity string `json:"complexity"`
	Hint       string `json:"hint,omitempty"`
}

// New prepares a fresh delivery for a plan: profile, table, plan, skills,
// inventory, routing generation and the graph. Nothing is journaled until
// Run; DryRun shows what Run would do.
func New(ctx context.Context, opts Options) (*Runner, error) {
	r, err := prepare(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := r.loadPlan(opts.Plan); err != nil {
		return nil, err
	}
	if r.plan.Status != routing.PlanApproved {
		return nil, fmt.Errorf("loop: plan %s has Status: %s — the loop runs approved plans only (approval happens in /batuta-plan)", r.plan.Slug, r.plan.Status)
	}
	if err := r.preflight(ctx); err != nil {
		return nil, err
	}
	if open := r.openDeliveries(r.plan.Slug); len(open) > 0 {
		return nil, fmt.Errorf("loop: delivery %s of plan %s is not finished — continue it with --resume %s, or close it with --abandon %s", open[0], r.plan.Slug, open[0], open[0])
	}
	if opts.Inventory == nil {
		return nil, errors.New("loop: an inventory function is required")
	}
	snapshot, err := opts.Inventory(ctx)
	if err != nil {
		return nil, fmt.Errorf("loop: inventory: %w", err)
	}
	tasks := make([]routing.GenerationTask, 0, len(r.plan.Set.Tasks))
	for _, task := range r.plan.Set.Tasks {
		tasks = append(tasks, routing.GenerationTask{ID: task.ID, Domain: task.Domain, Complexity: task.Complexity})
	}
	generation, err := r.table.Generation(routing.TableGenerationInput{
		Snapshot: snapshot, Tasks: tasks, TaskSetDigest: r.plan.Set.Digest,
		WorkspaceIdentityDigest: digestString(r.root),
		EnclosingBudget:         routing.LoopBudgetCeiling{IterationCap: routing.MaxTaskExecutions},
	})
	if err != nil {
		return nil, fmt.Errorf("loop: routing: %w", err)
	}
	r.generation = generation
	if err := r.checkRouting(); err != nil {
		return nil, err
	}
	snapshotTasks, err := r.plan.Set.DeliverySnapshot()
	if err != nil {
		return nil, fmt.Errorf("loop: plan: %w", err)
	}
	graph, err := routing.NewDeliveryGraph(snapshotTasks, generation, r.openedHead)
	if err != nil {
		return nil, fmt.Errorf("loop: graph: %w", err)
	}
	r.graph = graph
	r.delivery = r.plan.Slug + "-" + r.now().UTC().Format("20060102-150405")
	return r, nil
}

// Resume reopens a delivery from its journal.
func Resume(ctx context.Context, opts Options) (*Runner, error) {
	r, err := prepare(ctx, opts)
	if err != nil {
		return nil, err
	}
	if !journal.ValidDeliveryID(opts.Resume) {
		return nil, fmt.Errorf("loop: %q is not a delivery id", opts.Resume)
	}
	records, err := r.store.Read(opts.Resume)
	if err != nil {
		return nil, fmt.Errorf("loop: %w", err)
	}
	if len(records) == 0 || records[0].Kind != KindOpened {
		return nil, errors.New("loop: the journal does not start with delivery_opened")
	}
	var opened openedDetail
	if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
		return nil, fmt.Errorf("loop: journal: %w", err)
	}
	if err := r.loadPlan(opened.Slug); err != nil {
		return nil, err
	}
	if r.plan.Set.Digest != opened.PlanDigest {
		return nil, fmt.Errorf("loop: plan %s changed since delivery %s started (task set digest differs); finish that delivery with --abandon, then start a new one", opened.Slug, opts.Resume)
	}
	if r.branch != opened.Branch {
		return nil, fmt.Errorf("loop: delivery %s runs on branch %s; %s is checked out", opts.Resume, opened.Branch, r.branch)
	}
	r.delivery = opts.Resume
	r.generation = opened.Generation
	r.parallel = opened.Parallel
	if opts.Parallel > 0 {
		r.parallel = opts.Parallel
	}
	r.journaled = true
	if err := r.replay(records); err != nil {
		return nil, err
	}
	if r.terminal != "" && r.terminal != StateWaitingInput && r.terminal != StateCanceled {
		return nil, fmt.Errorf("loop: delivery %s already ended: %s", opts.Resume, r.terminal)
	}
	if err := r.preflight(ctx); err != nil {
		return nil, err
	}
	if expected := r.expectedHead(); expected != "" && expected != r.openedHead {
		return nil, fmt.Errorf("loop: branch %s moved since delivery %s last integrated (%s now, %s expected); the integration chain must stay contiguous — close the delivery with --abandon and start a new one", r.branch, r.delivery, short(r.openedHead), short(expected))
	}
	r.terminal = ""
	return r, nil
}

func prepare(ctx context.Context, opts Options) (*Runner, error) {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Runner == nil {
		opts.Runner = publication.ExecRunner{}
	}
	if opts.TaskTimeout == 0 {
		opts.TaskTimeout = 45 * time.Minute
	}
	if opts.TestTimeout == 0 {
		opts.TestTimeout = 15 * time.Minute
	}
	if opts.MaxLimitWaits == 0 {
		opts.MaxLimitWaits = 20
	}
	if opts.LimitWaitDefault == 0 {
		opts.LimitWaitDefault = 30 * time.Minute
	}
	if opts.LimitBuffer == 0 {
		opts.LimitBuffer = time.Minute
	}
	root := opts.Workspace
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		root = cwd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("loop: workspace %s: %w", abs, err)
	}
	git, err := worktree.New(ctx, root)
	if err != nil {
		return nil, err
	}
	git.Runner = opts.Runner
	if git.Root != root {
		return nil, fmt.Errorf("loop: run from the repository root (%s), not %s", git.Root, root)
	}
	profile, err := LoadProfile(root)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(profile.Test) == "" {
		return nil, errors.New("loop: the profile has no `Test:` line — the loop cannot run gate 2 without the test command")
	}
	skills, err := FindSkills(root, opts.Skills)
	if err != nil {
		return nil, err
	}
	tablePayload, err := os.ReadFile(filepath.Join(root, ".batuta", "routing.md"))
	if err != nil {
		return nil, errors.New("loop: .batuta/routing.md is missing — run /batuta-init first")
	}
	table, err := routing.ParseRoutingTable(tablePayload)
	if err != nil {
		return nil, fmt.Errorf("loop: %w", err)
	}
	store, err := journal.Open(root)
	if err != nil {
		return nil, err
	}
	shell, err := gates.NewShellRunner(opts.TestTimeout)
	if err != nil {
		return nil, err
	}
	shell.Runner = opts.Runner
	branch, err := git.Branch(ctx)
	if err != nil {
		return nil, err
	}
	head, err := git.Head(ctx, root)
	if err != nil {
		return nil, err
	}
	sections, missing := Conventions(skills, profile.Template)
	subprocess := executor.NewSubprocess()
	subprocess.Runner = opts.Runner
	subprocess.Environment = opts.Environment
	parallel := profile.Parallelism()
	if opts.Parallel > 0 {
		parallel = min(opts.Parallel, routing.MaxParallelTasks)
	}
	return &Runner{
		opts: opts, root: root, git: git,
		gitState: publication.GitClient{Executable: git.Git, Runner: opts.Runner},
		integ:    integration.GitClient{Executable: git.Git, Runner: opts.Runner},
		store:    store, profile: profile, skills: skills, table: table,
		branch: branch, openedHead: head, parallel: parallel, shell: shell, subprocess: subprocess,
		adapters: map[string]executor.Adapter{}, sections: sections, missing: missing,
		now: opts.Now, out: opts.Stdout,
		worktrees: map[string]attemptWorktree{}, feedback: map[string][]string{},
		candidates: map[string]integration.CandidateEvidence{}, commits: map[string]string{},
		started: map[string]bool{}, preflights: map[string]integration.PreflightResult{},
	}, nil
}

func (r *Runner) loadPlan(reference string) error {
	slug := strings.TrimSpace(reference)
	if slug == "" {
		loader, err := routing.NewPlanLoader(r.root)
		if err != nil {
			return err
		}
		slugs, err := loader.ListPlans()
		if err != nil {
			return fmt.Errorf("loop: %w", err)
		}
		var approved []string
		for _, candidate := range slugs {
			if plan, err := loader.LoadPlan(candidate); err == nil && plan.Status == routing.PlanApproved {
				approved = append(approved, candidate)
			}
		}
		if len(approved) != 1 {
			return fmt.Errorf("loop: name the plan — %d approved plans under .batuta/ (%s)", len(approved), strings.Join(approved, ", "))
		}
		slug = approved[0]
	}
	if base := filepath.Base(slug); strings.HasPrefix(base, "plan-") && strings.HasSuffix(base, ".md") {
		slug = strings.TrimSuffix(strings.TrimPrefix(base, "plan-"), ".md")
	}
	loader, err := routing.NewPlanLoader(r.root)
	if err != nil {
		return err
	}
	plan, err := loader.LoadPlan(slug)
	if err != nil {
		return fmt.Errorf("loop: plan %s: %w", slug, err)
	}
	r.plan = plan
	r.planPath = filepath.Join(r.root, routing.PlanPath(slug))
	return nil
}

// preflight is what stops the run before anything is spent: a clean tree
// (managed state included — the loop integrates through git and cannot
// carry dirt), a checked-out branch, valid scopes.
func (r *Runner) preflight(ctx context.Context) error {
	entries, err := r.git.Status(ctx, r.root, false)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		managedOnly := true
		for _, entry := range entries {
			if len(entry) > 3 && !worktree.IsManaged(strings.TrimSpace(entry[3:])) {
				managedOnly = false
			}
		}
		hint := "commit or stash it first"
		if managedOnly {
			hint = "commit WORK.md and .batuta/ first (the loop integrates through git and needs a clean tree)"
		}
		return fmt.Errorf("%w: %d entries — %s", worktree.ErrDirty, len(entries), hint)
	}
	for _, task := range r.plan.Tasks {
		if err := gates.ValidScope(task.Scope); err != nil {
			return fmt.Errorf("loop: task %d: %w", task.Number, err)
		}
	}
	return r.git.EnsureExcluded(ctx)
}

// checkRouting loads every adapter the generation may use and refuses
// tasks whose selected executor is the conducting session.
func (r *Runner) checkRouting() error {
	var selfTasks []string
	for _, cell := range r.generation.Cells {
		if cell.Selected.ExecutorID == routing.ExecutorSelf {
			selfTasks = append(selfTasks, cell.TaskIDs...)
			continue
		}
		for _, candidate := range append([]routing.RuntimeCandidate{cell.Selected}, cell.Fallbacks...) {
			if candidate.ExecutorID == routing.ExecutorSelf {
				continue
			}
			if _, err := r.adapter(string(candidate.ExecutorID)); err != nil {
				return err
			}
		}
	}
	if len(selfTasks) > 0 {
		sort.Strings(selfTasks)
		return fmt.Errorf("loop: %s route to `self` (the conducting session): run them interactively through /batuta, tick them in the plan, then loop the rest", strings.Join(selfTasks, ", "))
	}
	for _, task := range r.plan.Tasks {
		if task.Executor == "" {
			continue
		}
		cell, found := r.cellFor(task.ID)
		if found && (string(cell.Selected.ExecutorID) != task.Executor || (task.Model != "" && cell.Selected.ModelID != task.Model)) {
			r.warnings = append(r.warnings, fmt.Sprintf("task %d hints %s/%s; the routing table decides: %s/%s", task.Number, task.Executor, task.Model, cell.Selected.ExecutorID, cell.Selected.ModelID))
		}
	}
	return nil
}

func (r *Runner) adapter(name string) (executor.Adapter, error) {
	if adapter, loaded := r.adapters[name]; loaded {
		return adapter, nil
	}
	adapter, err := executor.LoadAdapter(r.skills, name)
	if err != nil {
		return executor.Adapter{}, fmt.Errorf("loop: %w", err)
	}
	r.adapters[name] = adapter
	return adapter, nil
}

func (r *Runner) cellFor(taskID string) (routing.RoutingCell, bool) {
	for _, cell := range r.generation.Cells {
		if slices.Contains(cell.TaskIDs, taskID) {
			return cell, true
		}
	}
	return routing.RoutingCell{}, false
}

func (r *Runner) planTask(taskID string) routing.PlanTask {
	for _, task := range r.plan.Tasks {
		if task.ID == taskID {
			return task
		}
	}
	return routing.PlanTask{}
}

// Delivery is the identifier of the delivery in flight.
func (r *Runner) Delivery() string { return r.delivery }

// Warnings are non-blocking findings of the preflight (routing hints the
// table overrides, missing templates).
func (r *Runner) Warnings() []string {
	warnings := slices.Clone(r.warnings)
	if len(r.missing) > 0 {
		warnings = append(warnings, "templates not installed: "+strings.Join(r.missing, ", "))
	}
	return warnings
}

// Preview is what --dry-run prints: the waves in dependency order with
// the executor and model per task, computed on a copy of the graph.
type Preview struct {
	Delivery string
	Branch   string
	Head     string
	Test     string
	Parallel int
	Waves    []PreviewWave
	Warnings []string
}

type PreviewWave struct {
	Number int
	Tasks  []PreviewTask
}

type PreviewTask struct {
	ID         string
	Number     int
	Title      string
	Lane       string
	Executor   string
	Model      string
	Reasoning  string
	Fallbacks  []string
	Worktree   string
	Completed  bool
	Dependency []string
}

// DryRun simulates wave admission on a copy of the graph, integrating each
// wave's tasks virtually so dependents show up in later waves.
func (r *Runner) DryRun() (Preview, error) {
	preview := Preview{Delivery: r.delivery, Branch: r.branch, Head: r.openedHead, Test: r.profile.Test, Parallel: r.parallel, Warnings: r.Warnings()}
	payload, err := json.Marshal(r.graph)
	if err != nil {
		return Preview{}, err
	}
	var copy routing.DeliveryGraph
	if err := json.Unmarshal(payload, &copy); err != nil {
		return Preview{}, err
	}
	head := r.openedHead
	reachable := map[string]bool{head: true}
	for _, task := range copy.Tasks {
		if task.State == routing.GraphTaskIntegrated {
			reachable[task.IntegratedCommitSHA] = true
		}
	}
	for len(preview.Waves) < routing.MaxDeliveryTasks {
		wave, err := copy.AdmitReadyWave(routing.ReadyWaveInput{IntegrationHeadSHA: head, RemainingSlots: r.parallel, ReachableCommits: reachable})
		if err != nil {
			if errors.Is(err, routing.ErrDependencyBlocked) {
				return preview, errors.New("loop: the plan's dependencies cannot all be satisfied")
			}
			return Preview{}, err
		}
		if len(wave.TaskIDs) == 0 {
			break
		}
		if err := copy.BeginWaveAttempts(wave.Number, r.generation); err != nil {
			return Preview{}, err
		}
		previewWave := PreviewWave{Number: wave.Number}
		for _, taskID := range wave.TaskIDs {
			previewWave.Tasks = append(previewWave.Tasks, r.previewTask(taskID, 1))
			// Virtually integrate: a fake but well-formed commit per task.
			fake := digestHex(head + taskID)
			task := graphTask(&copy, taskID)
			task.State = routing.GraphTaskIntegrated
			task.IntegratedCommitSHA = fake
			task.Attempts[len(task.Attempts)-1].State = routing.GraphTaskIntegrated
			reachable[fake] = true
			head = fake
		}
		preview.Waves = append(preview.Waves, previewWave)
	}
	return preview, nil
}

func (r *Runner) previewTask(taskID string, execution int) PreviewTask {
	plan := r.planTask(taskID)
	cell, _ := r.cellFor(taskID)
	task := PreviewTask{
		ID: taskID, Number: plan.Number, Title: plan.Title, Lane: string(plan.Domain) + "/" + string(plan.Complexity),
		Executor: string(cell.Selected.ExecutorID), Model: cell.Selected.ModelID, Reasoning: cell.Selected.Reasoning,
		Worktree: filepath.Join(worktree.Dir, r.worktreeName(taskID, execution)), Completed: plan.Status == "completed",
		Dependency: slices.Clone(plan.Dependencies),
	}
	for _, fallback := range cell.Fallbacks {
		task.Fallbacks = append(task.Fallbacks, string(fallback.ExecutorID)+"/"+fallback.ModelID)
	}
	return task
}

// PrintPreview renders a Preview for humans.
func PrintPreview(w io.Writer, preview Preview) {
	fmt.Fprintf(w, "delivery  %s\nbranch    %s @ %s\ntests     %s\nparallel  %d\n", preview.Delivery, preview.Branch, short(preview.Head), preview.Test, preview.Parallel)
	for _, warning := range preview.Warnings {
		fmt.Fprintf(w, "warning   %s\n", warning)
	}
	fmt.Fprintln(w)
	if len(preview.Waves) == 0 {
		fmt.Fprintln(w, "nothing to run: every task is ticked in the plan")
		return
	}
	for _, wave := range preview.Waves {
		fmt.Fprintf(w, "wave %d\n", wave.Number)
		for _, task := range wave.Tasks {
			fallbacks := ""
			if len(task.Fallbacks) > 0 {
				fallbacks = " (then " + strings.Join(task.Fallbacks, ", ") + ")"
			}
			depends := ""
			if len(task.Dependency) > 0 {
				depends = " after " + strings.Join(task.Dependency, ", ")
			}
			fmt.Fprintf(w, "  %-8s %-16s %s/%s reasoning %s%s%s\n           %s\n           %s\n",
				task.ID, task.Lane, task.Executor, task.Model, task.Reasoning, fallbacks, depends, task.Title, task.Worktree)
		}
	}
}

// Run drives the delivery to a terminal state, or returns ErrStopped when
// --max-waves ended it early.
func (r *Runner) Run(ctx context.Context) (string, error) {
	if !r.journaled {
		if err := r.open(); err != nil {
			return "", err
		}
	}
	for {
		if ctx.Err() != nil {
			return r.finish(context.WithoutCancel(ctx), StateCanceled)
		}
		ran, err := r.runPreparingWaves(ctx)
		if err != nil {
			return r.fail(ctx, err)
		}
		if ctx.Err() != nil {
			return r.finish(context.WithoutCancel(ctx), StateCanceled)
		}
		settled, err := r.settleCandidates(ctx)
		if err != nil {
			return r.fail(ctx, err)
		}
		if ran > 0 || settled > 0 {
			continue
		}
		if r.opts.MaxWaves > 0 && r.wavesRun >= r.opts.MaxWaves {
			r.record(KindInterrupted, "", map[string]any{"reason": "max_waves", "waves": r.wavesRun})
			return "", ErrStopped
		}
		head, err := r.git.Head(ctx, r.root)
		if err != nil {
			return r.fail(ctx, err)
		}
		reachable, err := r.reachable(ctx, head)
		if err != nil {
			return r.fail(ctx, err)
		}
		r.mu.Lock()
		wave, err := r.graph.AdmitReadyWave(routing.ReadyWaveInput{IntegrationHeadSHA: head, RemainingSlots: r.parallel, ReachableCommits: reachable})
		r.mu.Unlock()
		if err != nil {
			if errors.Is(err, routing.ErrDependencyBlocked) {
				return r.finish(ctx, StateBlocked)
			}
			return r.fail(ctx, err)
		}
		if len(wave.TaskIDs) == 0 {
			switch {
			case r.allIntegrated():
				return r.finish(ctx, StateDone)
			case r.anyState(routing.GraphTaskWaitingInput):
				return r.finish(ctx, StateWaitingInput)
			default:
				return r.finish(ctx, StateBlocked)
			}
		}
		r.wavesRun++
		r.record(KindWave, "", map[string]any{"wave": wave.Number, "base": wave.BaseHeadSHA, "tasks": wave.TaskIDs})
		fmt.Fprintf(r.out, "wave %d: %s (base %s)\n", wave.Number, strings.Join(wave.TaskIDs, ", "), short(wave.BaseHeadSHA))
	}
}

func (r *Runner) open() error {
	tasks := make([]taskSummary, 0, len(r.plan.Tasks))
	for _, task := range r.plan.Tasks {
		hint := ""
		if task.Executor != "" {
			hint = task.Executor + "/" + task.Model
		}
		tasks = append(tasks, taskSummary{ID: task.ID, Number: task.Number, Title: task.Title, Domain: string(task.Domain), Complexity: string(task.Complexity), Hint: hint})
	}
	detail := openedDetail{
		Slug: r.plan.Slug, PlanPath: routing.PlanPath(r.plan.Slug), PlanDigest: r.plan.Set.Digest,
		Branch: r.branch, Head: r.openedHead, Parallel: r.parallel, Workspace: r.root,
		Generation: r.generation, Tasks: tasks,
	}
	if err := r.record(KindOpened, "", detail); err != nil {
		return err
	}
	r.journaled = true
	fmt.Fprintf(r.out, "delivery %s opened on %s @ %s — journal %s\n", r.delivery, r.branch, short(r.openedHead), filepath.Join(journal.Dir, r.delivery+".jsonl"))
	return nil
}

// record appends a journal record carrying the graph as it stands. Callers
// hold r.mu or run before any concurrency starts.
func (r *Runner) record(kind journal.Kind, taskID string, detail any) error {
	var raw json.RawMessage
	if detail != nil {
		encoded, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		raw = encoded
	}
	graph, err := json.Marshal(r.graph)
	if err != nil {
		return err
	}
	_, err = r.store.Append(r.delivery, journal.Record{Kind: kind, TaskID: taskID, Detail: raw, Graph: graph, At: r.now()})
	return err
}

func (r *Runner) locked(kind journal.Kind, taskID string, detail any, mutate func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mutate != nil {
		if err := mutate(); err != nil {
			return err
		}
	}
	return r.record(kind, taskID, detail)
}

// runPreparingWaves begins and executes every attempt that is ready:
// tasks admitted to a wave, retries, re-executions after a conflict, and
// continuations after an answer. It returns how many attempts ran.
func (r *Runner) runPreparingWaves(ctx context.Context) (int, error) {
	r.mu.Lock()
	for _, wave := range r.graph.Waves {
		needsBegin := false
		for _, taskID := range wave.TaskIDs {
			task := graphTask(r.graph, taskID)
			if task != nil && task.State == routing.GraphTaskPreparing && (len(task.Attempts) == 0 || task.Attempts[len(task.Attempts)-1].State != routing.GraphTaskPreparing) {
				needsBegin = true
			}
		}
		if needsBegin {
			if err := r.graph.BeginWaveAttempts(wave.Number, r.generation); err != nil {
				r.mu.Unlock()
				return 0, fmt.Errorf("loop: begin wave %d: %w", wave.Number, err)
			}
			if err := r.record(KindAttempts, "", map[string]any{"wave": wave.Number}); err != nil {
				r.mu.Unlock()
				return 0, err
			}
		}
	}
	var ready []string
	for _, task := range r.graph.Tasks {
		if len(task.Attempts) == 0 {
			continue
		}
		attempt := task.Attempts[len(task.Attempts)-1]
		key := attemptKey(task.TaskID, attempt.Execution)
		switch {
		case task.State == routing.GraphTaskPreparing && attempt.State == routing.GraphTaskPreparing:
			ready = append(ready, task.TaskID)
		case task.State == routing.GraphTaskRunning && attempt.State == routing.GraphTaskRunning && !r.started[key]:
			ready = append(ready, task.TaskID) // continuation after an answer
		}
	}
	r.mu.Unlock()
	if len(ready) == 0 {
		return 0, nil
	}
	semaphore := make(chan struct{}, max(r.parallel, 1))
	var wg sync.WaitGroup
	errs := make(chan error, len(ready))
	for _, taskID := range ready {
		wg.Add(1)
		go func(taskID string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			if ctx.Err() != nil {
				return
			}
			if err := r.runAttempt(ctx, taskID); err != nil {
				errs <- err
			}
		}(taskID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return len(ready), err
		}
	}
	return len(ready), nil
}

func (r *Runner) reachable(ctx context.Context, head string) (map[string]bool, error) {
	reachable := map[string]bool{head: true}
	r.mu.Lock()
	var integrated []string
	for _, task := range r.graph.Tasks {
		if task.State == routing.GraphTaskIntegrated && task.IntegratedCommitSHA != "" {
			integrated = append(integrated, task.IntegratedCommitSHA)
		}
	}
	r.mu.Unlock()
	for _, sha := range integrated {
		if sha == head {
			continue
		}
		ancestor, err := r.git.IsAncestor(ctx, sha, head)
		if err != nil {
			return nil, err
		}
		if ancestor {
			reachable[sha] = true
		}
	}
	return reachable, nil
}

func (r *Runner) allIntegrated() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, task := range r.graph.Tasks {
		if task.State != routing.GraphTaskIntegrated {
			return false
		}
	}
	return true
}

func (r *Runner) anyState(state routing.GraphTaskState) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, task := range r.graph.Tasks {
		if task.State == state {
			return true
		}
	}
	return false
}

func (r *Runner) fail(ctx context.Context, err error) (string, error) {
	r.mu.Lock()
	_ = r.record(KindInterrupted, "", map[string]any{"error": err.Error()})
	r.mu.Unlock()
	return "", err
}

// expectedHead is where the branch must stand for the integration chain to
// continue: the last integration's final head, or the base of the latest
// wave when nothing integrated yet.
func (r *Runner) expectedHead() string {
	if len(r.graph.Integrations) > 0 {
		return r.graph.Integrations[len(r.graph.Integrations)-1].FinalHeadSHA
	}
	if len(r.graph.Waves) > 0 {
		return r.graph.Waves[len(r.graph.Waves)-1].BaseHeadSHA
	}
	return ""
}

func (r *Runner) worktreeName(taskID string, execution int) string {
	return r.plan.Slug + "-" + strings.ReplaceAll(taskID, "_", "-") + "-e" + strconv.Itoa(execution)
}

func (r *Runner) branchName(taskID string, execution int) string {
	return "batuta/" + r.plan.Slug + "/" + strings.ReplaceAll(taskID, "_", "-") + "-e" + strconv.Itoa(execution)
}

func attemptKey(taskID string, execution int) string {
	return taskID + ":" + strconv.Itoa(execution)
}

func graphTask(graph *routing.DeliveryGraph, taskID string) *routing.GraphTask {
	for index := range graph.Tasks {
		if graph.Tasks[index].TaskID == taskID {
			return &graph.Tasks[index]
		}
	}
	return nil
}

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestHex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:40]
}
