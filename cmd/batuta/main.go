// Command batuta provides the deterministic tools used by the Batuta
// conducting cycle: capability discovery, environment inspection, delivery
// execution and audit trails, and standalone verification gates. Hosts probe
// `batuta capabilities` before relying on a command.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/batuta-ai/core/inventory"
	"github.com/batuta-ai/core/inventory/adapters"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/review"
	"github.com/batuta-ai/core/routing"
	"github.com/batuta-ai/core/worktree"
)

const usage = `batuta — the conductor's deterministic tools

Usage:
  batuta version
  batuta capabilities
  batuta inventory [--workspace <dir>] [--timeout <duration>]
  batuta doctor    [--workspace <dir>] [--json] [--timeout <duration>]
  batuta loop      [--dry-run] [--parallel N] [--skills <dir>] [<plan>]
  batuta loop      --roadmap [--dry-run] [--resume <delivery>]
  batuta loop      --resume <delivery> | --answer <task> "<text>" | --abandon <delivery>
  batuta loop      --dashboard [--watch] [--interval 500ms] [<delivery>]
  batuta watch     [<delivery>] [--interval 500ms] [--once] [--lang en|pt] [--ascii]
  batuta trail     [<delivery>]
  batuta review    [--base <ref>] [--worktree] [--spec <plan>] [--cohort-files N] [--parallel N] [--reviewer <executor/model>] [--full] [--out <dir>]
  batuta gate tree --snapshot [--dir <d>]
  batuta gate tree --before '<json>' [--dir <d>]
  batuta gate tests --command "<cmd>" [--dir <d>] [--timeout <duration>]
  batuta gate scope --base <sha-or-ref> --scope <a,b,c> [--dir <d>]
  batuta gate proofs --accept "<criterion → proof>;..." [--dir <d>] [--timeout <duration>]
  batuta gate verifier --criteria <n> [--proofs '<json array>'] < output

capabilities  The subcommands this binary ships, as JSON. Skills probe it
           before calling gate or loop; an older binary fails the probe.

loop       The mechanical conductor over an approved plan
           (.batuta/plan-<slug>.md): routing from .batuta/routing.md, one
           executor session per task in its own worktree through the
           adapter, the four gates, retry then escalation, one commit per
           task integrated onto the checked-out branch, everything
           journaled under .batuta/journal/. Exit 0 when every task
           integrated; 2 blocked; 3 waiting for an answer; 4 waiting for
           an approved roadmap plan; 130 canceled.
watch      Live dashboard of a delivery (the most recent open one by
           default). --interval sets the journal poll interval; --once prints a
           snapshot; --lang selects labels; --ascii uses ASCII borders and
           status glyphs. Keys: Up/Down and PgUp/PgDn scroll, f follows the
           active task, r opens the answer editor, R shows the answer command,
           d opens the delivery picker, o opens the log, l changes focus,
           ? shows the legend, q quits, and the mouse wheel scrolls the
           focused panel. In the editor, ctrl+enter, alt+enter or ctrl+s
           submits.
trail      One line per journal record of a delivery (the latest by
           default).
review     Read-only, cohort-based delivery review through the configured
           executor adapter. Writes manifest.json, findings.json, review.md
           and state.json; exits 0 SHIP, 2 FIX_BEFORE_SHIP, 3 REWORK.

inventory  Redacted snapshot of the executor CLIs installed on this machine
           (codex, opencode, cursor-agent, claude, agy, compozy): versions,
           provider/model bindings, credential state, diagnostics. JSON.
doctor     What a conductor needs to know before the first cycle: which
           executors run, whether the workspace is a git repository, and
           whether the Batuta skills are installed. Human text, or --json.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var exit *ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "batuta:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("a subcommand is required")
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version())
		return nil
	case "capabilities":
		return runCapabilities(stdout)
	case "inventory":
		return runInventory(args[1:], stdout)
	case "doctor":
		return runDoctor(args[1:], stdout)
	case "loop":
		return runLoop(args[1:], stdout, stderr)
	case "watch":
		return runWatch(args[1:], stdout, stderr)
	case "trail":
		return runTrail(args[1:], stdout)
	case "review":
		return runReview(args[1:], stdout, stderr)
	case "gate":
		return runGate(args[1:], stdout)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// buildVersion is set by goreleaser (-X main.buildVersion={{ .Tag }}); a
// `go install` build reports the module version from build info instead.
var buildVersion string

func version() string {
	if buildVersion != "" {
		return buildVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

// commands lists every capability this binary ships; skills read this list,
// never the usage text.
var commands = []string{"capabilities", "doctor", "gate", "inventory", "loop", "review", "roadmap", "trail", "version", "watch"}

type capabilities struct {
	Version  string   `json:"version"`
	Commands []string `json:"commands"`
}

func runCapabilities(stdout io.Writer) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(capabilities{Version: version(), Commands: commands})
}

// executables resolves the executor CLIs on PATH (and through mise when
// present), the same way the CompozyOS extension does.
type executables struct {
	Compozy, Codex, OpenCode, Cursor, Claude, Agy string
}

func discoverExecutables() executables {
	cursor := optionalExecutable("cursor-agent")
	if cursor == "" {
		cursor = optionalExecutable("agent")
	}
	return executables{
		Compozy: optionalExecutable("compozy"), Codex: optionalExecutable("codex"),
		OpenCode: optionalExecutable("opencode"), Cursor: cursor,
		Claude: optionalExecutable("claude"), Agy: optionalExecutable("agy"),
	}
}

func optionalExecutable(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		if abs, err := filepath.Abs(path); err == nil {
			return abs
		}
	}
	mise, err := exec.LookPath("mise")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, mise, "which", name).Output()
	if err != nil {
		return ""
	}
	path := filepath.Clean(strings.TrimSpace(string(output)))
	if !filepath.IsAbs(path) {
		return ""
	}
	return path
}

func workspaceRoot(flagValue string) (string, error) {
	root := flagValue
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root = cwd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspace %q: %w", root, err)
	}
	return resolved, nil
}

func collect(ctx context.Context, root string) (inventory.InventorySnapshot, executables, error) {
	found := discoverExecutables()
	snapshot, err := collectWithRunner(ctx, root, found, publication.ExecRunner{})
	return snapshot, found, err
}

func collectWithRunner(ctx context.Context, root string, found executables, runner publication.CommandRunner) (inventory.InventorySnapshot, error) {
	collector, err := adapters.NewCollector(runner, adapters.CollectorOptions{
		TrustedWorkspace: root, WorkspaceID: "local",
		CompozyExecutable: found.Compozy, CodexExecutable: found.Codex,
		OpenCodeExecutable: found.OpenCode, CursorExecutable: found.Cursor,
		ClaudeExecutable: found.Claude, AgyExecutable: found.Agy,
		ProbeParallelism: 8,
	})
	if err != nil {
		return inventory.InventorySnapshot{}, err
	}
	return collector.Collect(ctx)
}

func runInventory(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("inventory", flag.ContinueOnError)
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	timeout := flags.Duration("timeout", 60*time.Second, "overall probe timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := workspaceRoot(*workspace)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	snapshot, _, err := collect(ctx, root)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(inventory.Redact(snapshot))
}

type doctorReport struct {
	Workspace      string                `json:"workspace"`
	GitRepository  bool                  `json:"git_repository"`
	GitToplevel    string                `json:"git_toplevel,omitempty"`
	GitState       string                `json:"git_state,omitempty"`
	GitClean       *bool                 `json:"git_clean,omitempty"`
	GitExecutable  string                `json:"git_executable,omitempty"`
	Commands       []string              `json:"commands"`
	SkillsPath     string                `json:"skills_path,omitempty"`
	Executors      []doctorExecutor      `json:"executors"`
	Digest         string                `json:"inventory_digest"`
	ProbeDurations []doctorProbeDuration `json:"-"`
}

type doctorProbeDuration struct {
	Executor string
	Probe    string
	Duration time.Duration
}

type doctorProbeRunner struct {
	runner            publication.CommandRunner
	executorByCommand map[string]string
	mu                sync.Mutex
	durations         []doctorProbeDuration
}

func (r *doctorProbeRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	started := time.Now()
	result, err := r.runner.Run(ctx, command)
	duration := doctorProbeDuration{
		Executor: r.executorByCommand[command.Executable],
		Probe:    strings.Join(command.Args, " "),
		Duration: time.Since(started),
	}
	r.mu.Lock()
	r.durations = append(r.durations, duration)
	r.mu.Unlock()
	return result, err
}

func (r *doctorProbeRunner) snapshot() []doctorProbeDuration {
	r.mu.Lock()
	defer r.mu.Unlock()
	durations := append([]doctorProbeDuration(nil), r.durations...)
	sort.Slice(durations, func(i, j int) bool {
		if durations[i].Executor != durations[j].Executor {
			return durations[i].Executor < durations[j].Executor
		}
		return durations[i].Probe < durations[j].Probe
	})
	return durations
}

func doctorExecutorCommands(found executables) map[string]string {
	commands := make(map[string]string)
	for executor, command := range map[string]string{
		"compozy": found.Compozy, "codex": found.Codex, "opencode": found.OpenCode,
		"cursor-agent": found.Cursor, "claude": found.Claude, "agy": found.Agy,
	} {
		if command != "" {
			commands[command] = executor
		}
	}
	return commands
}

type doctorExecutor struct {
	ID           string   `json:"executor_id"`
	Executable   string   `json:"executable,omitempty"`
	Availability string   `json:"availability"`
	Version      string   `json:"version,omitempty"`
	Models       int      `json:"models"`
	Credential   string   `json:"credential_state,omitempty"`
	Diagnostics  []string `json:"diagnostics,omitempty"`
}

func runDoctor(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	asJSON := flags.Bool("json", false, "machine-readable output")
	timeout := flags.Duration("timeout", 60*time.Second, "overall probe timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := workspaceRoot(*workspace)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	found := discoverExecutables()
	probeRunner := &doctorProbeRunner{runner: publication.ExecRunner{}, executorByCommand: doctorExecutorCommands(found)}
	snapshot, err := collectWithRunner(ctx, root, found, probeRunner)
	if err != nil {
		return err
	}
	report := doctorReport{Workspace: root, Digest: snapshot.Digest, Commands: commands, ProbeDurations: probeRunner.snapshot()}
	if git, err := exec.LookPath("git"); err == nil {
		report.GitExecutable = git
		// The probe context may already be spent by collect; git gets its own.
		gitCtx, cancelGit := context.WithTimeout(context.Background(), 5*time.Second)
		report.GitToplevel, report.GitState, report.GitClean = inspectGit(gitCtx, git, root)
		cancelGit()
		report.GitRepository = report.GitToplevel != ""
	}
	report.SkillsPath = findSkills(root)
	paths := map[inventory.ExecutorID]string{
		"compozy": found.Compozy, "codex": found.Codex, "opencode": found.OpenCode,
		"cursor-agent": found.Cursor, "claude": found.Claude, "agy": found.Agy,
	}
	for _, executor := range snapshot.Executors {
		entry := doctorExecutor{
			ID:           string(executor.ID),
			Executable:   paths[executor.ID],
			Availability: string(executor.Availability),
			Credential:   string(executor.CredentialState),
		}
		entry.Version, entry.Models = summarizeEvidence(executor)
		for _, diagnostic := range executor.Diagnostics {
			entry.Diagnostics = append(entry.Diagnostics, diagnostic.Code)
		}
		report.Executors = append(report.Executors, entry)
	}
	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	printDoctor(stdout, report)
	return nil
}

// summarizeEvidence returns the probed version (first identifier of the
// version evidence) and the number of concrete model bindings.
func summarizeEvidence(executor inventory.ExecutorSnapshot) (string, int) {
	models := 0
	for _, binding := range executor.ProviderBindings {
		if binding.ModelID != "" {
			models++
		}
	}
	if len(executor.Version.Identifiers) > 0 {
		return executor.Version.Identifiers[0], models
	}
	return "", models
}

// inspectGit asks git itself whether root is inside a repository — a
// worktree checkout has a .git file, a nested directory has none — and
// distinguishes a clean tree from conductor-owned state and other changes.
func inspectGit(ctx context.Context, git, root string) (toplevel, state string, clean *bool) {
	out, err := exec.CommandContext(ctx, git, "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", "", nil
	}
	toplevel = strings.TrimSpace(string(out))
	status, err := exec.CommandContext(ctx, git, "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return toplevel, "", nil
	}
	state = gitState(status)
	isClean := state == "clean"
	return toplevel, state, &isClean
}

func gitState(status []byte) string {
	if len(bytes.TrimSpace(status)) == 0 {
		return "clean"
	}
	for _, entry := range strings.Split(strings.TrimSpace(string(status)), "\n") {
		if len(entry) <= 3 {
			return "dirty"
		}
		for _, path := range strings.Split(strings.TrimSpace(entry[3:]), " -> ") {
			if !worktree.IsManaged(path) {
				return "dirty"
			}
		}
	}
	return "managed"
}

func findSkills(root string) string {
	path, err := loop.FindSkills(root, "")
	if err != nil {
		return ""
	}
	return path
}

// ExitError carries the loop's exit code: the delivery ended, and not in
// the state that means success.
type ExitError struct {
	Code  int
	State string
}

func (e *ExitError) Error() string { return "delivery " + e.State }

func runLoop(args []string, stdout, stderr io.Writer) (runErr error) {
	flags := flag.NewFlagSet("loop", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "repository root (default: current directory)")
	skills := flags.String("skills", "", "batuta skill directory holding adapters/ and templates/ (default: auto-detected)")
	dryRun := flags.Bool("dry-run", false, "show the waves, executors and worktrees; run nothing")
	roadmap := flags.Bool("roadmap", false, "run the phases in .batuta/roadmap.md in order")
	resume := flags.String("resume", "", "continue a delivery from its journal")
	abandon := flags.String("abandon", "", "close a delivery that will not continue; ticks what integrated")
	answer := flags.String("answer", "", "task (task_N or N) to answer; the text follows as the next argument")
	dashboard := flags.Bool("dashboard", false, "print the state of the open deliveries as TSV")
	watch := flags.Bool("watch", false, "redraw a live dashboard until the delivery ends")
	interval := flags.Duration("interval", 500*time.Millisecond, "journal poll interval")
	parallel := flags.Int("parallel", 0, "executors per wave, at most 4 (default: the profile's Execution line)")
	taskTimeout := flags.Duration("task-timeout", 45*time.Minute, "time budget per executor session")
	testTimeout := flags.Duration("test-timeout", 15*time.Minute, "time budget for the test command and each proof")
	maxWaves := flags.Int("max-waves", 0, "stop after N waves (the delivery stays resumable)")
	keep := flags.Bool("keep-worktrees", false, "keep task worktrees after integration or abort")
	maxLimitWaits := flags.Int("max-limit-waits", 20, "consecutive usage-limit waits one attempt may take")
	limitHorizon := flags.Duration("limit-horizon", 2*time.Hour, "switch to the next runtime when a usage-limit reset lies beyond this duration")
	limitWait := flags.Duration("limit-wait", 30*time.Minute, "wait when a usage-limit message names no reset time")
	if err := flags.Parse(args); err != nil {
		return err
	}
	rest := flags.Args()
	if *roadmap && (*dashboard || *watch || *abandon != "" || (len(rest) > 0 && *answer == "")) {
		return errors.New("--roadmap runs .batuta/roadmap.md; it cannot be combined with a plan, --dashboard, --watch or --abandon")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *dashboard {
		delivery := ""
		if len(rest) > 0 {
			delivery = rest[0]
		}
		if *watch {
			return loop.Watch(ctx, *workspace, delivery, *interval, stdout)
		}
		return loop.Dashboard(*workspace, delivery, stdout)
	}
	if *watch {
		return errors.New("--watch requires --dashboard")
	}
	opts := loop.Options{
		Workspace: *workspace, Skills: *skills, Parallel: *parallel, TaskTimeout: *taskTimeout, TestTimeout: *testTimeout,
		MaxWaves: *maxWaves, KeepWorktrees: *keep, MaxLimitWaits: *maxLimitWaits, LimitWaitDefault: *limitWait, LimitHorizon: *limitHorizon,
		Stdout: stdout, Inventory: func(ctx context.Context) (inventory.InventorySnapshot, error) {
			root, err := workspaceRoot(*workspace)
			if err != nil {
				return inventory.InventorySnapshot{}, err
			}
			probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			snapshot, _, err := collect(probeCtx, root)
			return snapshot, err
		},
	}
	if *roadmap && *dryRun {
		return loop.DryRunRoadmap(opts)
	}
	if *abandon != "" {
		opts.Resume = *abandon
		state, err := loop.Abandon(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "delivery %s %s\n", *abandon, state)
		return nil
	}
	if *answer != "" {
		if len(rest) != 1 {
			return errors.New("usage: batuta loop --answer <task> \"<text>\"")
		}
		delivery, err := loop.Answer(*workspace, *answer, rest[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "answer recorded for %s in delivery %s; resuming\n", *answer, delivery)
		opts.Resume = delivery
	} else if *resume != "" {
		opts.Resume = *resume
	}
	if *roadmap {
		state, err := loop.RunRoadmap(ctx, opts)
		if errors.Is(err, loop.ErrStopped) {
			return nil
		}
		if err != nil {
			return err
		}
		return loopExit(state)
	}
	var runner *loop.Runner
	var err error
	if opts.Resume != "" {
		runner, err = loop.Resume(ctx, opts)
	} else {
		if len(rest) > 0 {
			opts.Plan = rest[0]
		}
		runner, err = loop.New(ctx, opts)
	}
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, runner.Release()) }()
	if *dryRun {
		preview, err := runner.DryRun()
		if err != nil {
			return err
		}
		loop.PrintPreview(stdout, preview)
		return nil
	}
	state, err := runner.Run(ctx)
	if err != nil {
		if errors.Is(err, loop.ErrStopped) {
			fmt.Fprintf(stdout, "stopped after %d wave(s); resume with: batuta loop --resume %s\n", *maxWaves, runner.Delivery())
			return nil
		}
		return err
	}
	return loopExit(state)
}

func loopExit(state string) error {
	switch state {
	case loop.StateDone:
		return nil
	case loop.StateWaitingInput:
		return &ExitError{Code: 3, State: state}
	case loop.StateWaitingPlan:
		return &ExitError{Code: 4, State: state}
	case loop.StateCanceled:
		return &ExitError{Code: 130, State: state}
	default:
		return &ExitError{Code: 2, State: state}
	}
}

var (
	reviewNow            = time.Now
	reviewSessionOptions = func(root string, parallel int) review.SessionOptions {
		return review.SessionOptions{Root: root, Parallel: parallel}
	}
)

func runReview(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("review", flag.ContinueOnError)
	flags.SetOutput(stderr)
	base := flags.String("base", "", "base git ref (default: the branch point or HEAD)")
	includeWorktree := flags.Bool("worktree", false, "include untracked, non-ignored files")
	spec := flags.String("spec", "", "plan slug or plan file whose criteria are the review spec")
	cohortFiles := flags.Int("cohort-files", review.CohortFiles, "maximum files per cohort")
	parallel := flags.Int("parallel", 1, "reviewer sessions to run in parallel")
	reviewer := flags.String("reviewer", "", "reviewer override as executor/model")
	full := flags.Bool("full", false, "ignore prior review state and review from --base")
	out := flags.String("out", "", "artifact directory (default: .batuta/reviews/<date>-<slug>)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("review accepts flags only")
	}
	if *cohortFiles < 1 || *parallel < 1 {
		return errors.New("review requires positive --cohort-files and --parallel values")
	}
	root, err := workspaceRoot("")
	if err != nil {
		return err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	git := publication.GitClient{Executable: gitPath, Runner: publication.ExecRunner{}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	baseline, err := git.WorktreeState(ctx, root)
	if err != nil {
		return err
	}
	trackedBaseline, err := trackedChangeSignatures(root, baseline.HeadSHA)
	if err != nil {
		return err
	}
	var rules []review.SpecRule
	var resolvedSpecPath string
	if *spec != "" {
		rules, err = review.LoadSpecCriteria(root, *spec)
		if err != nil {
			return fmt.Errorf("review: load spec %s: %w", *spec, err)
		}
		resolvedSpecPath, err = reviewSpecPath(root, *spec)
		if err != nil {
			return err
		}
	}
	slug, err := reviewSlug(root, *spec)
	if err != nil {
		return err
	}
	branch, err := reviewSlug(root, "")
	if err != nil {
		return err
	}
	branchIdentity, err := reviewBranchIdentity(root)
	if err != nil {
		return err
	}
	specSlug := ""
	if *spec != "" {
		specSlug = slug
	}
	key := reviewStateKey(branch, branchIdentity, specSlug, resolvedSpecPath)
	statePath := filepath.Join(root, ".batuta", "reviews", "state", key+".json")
	sessionSlug := reviewNow().Format("2006-01-02") + "-" + slug
	directory := *out
	if directory == "" {
		directory = filepath.Join(root, ".batuta", "reviews", sessionSlug)
	} else if !filepath.IsAbs(directory) {
		directory = filepath.Join(root, directory)
	}
	artifactPaths := append(review.ArtifactPaths(directory), statePath)
	resolvedPaths, err := review.CheckArtifactPaths(root, artifactPaths)
	if err != nil {
		return err
	}
	requestedBase := *base
	if requestedBase == "" {
		requestedBase, err = defaultReviewBase(root)
		if err != nil {
			return err
		}
	}
	state, err := review.LoadIncrementalState(root, statePath, requestedBase, *full)
	if err != nil {
		return err
	}
	manifest, err := review.BuildIncrementalManifest(root, state, review.ManifestOptions{Worktree: *includeWorktree, CohortFiles: *cohortFiles})
	if err != nil {
		return err
	}
	tablePayload, err := os.ReadFile(filepath.Join(root, ".batuta", "routing.md"))
	if err != nil {
		return errors.New("review: .batuta/routing.md is missing — run /batuta-init first")
	}
	table, err := routing.ParseRoutingTable(tablePayload)
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}
	runtime, err := review.ReviewerRuntimeFromTable(table, routing.DomainGeneral, *reviewer)
	if err != nil {
		return err
	}
	options := reviewSessionOptions(root, *parallel)
	cohorts, err := review.RunCohorts(ctx, manifest, runtime, options)
	if err != nil {
		return reviewSessionError(ctx, git, root, baseline, trackedBaseline, err)
	}
	var sweep *review.SpecSweep
	if *spec != "" {
		result, err := review.RunSpecSweep(ctx, manifest, rules, runtime, options)
		if err != nil {
			return reviewSessionError(ctx, git, root, baseline, trackedBaseline, err)
		}
		sweep = &result
	}
	after, stateErr := git.WorktreeState(ctx, root)
	if stateErr != nil || after != baseline {
		paths := changedTrackedPaths(root, baseline.HeadSHA, trackedBaseline)
		if len(paths) == 0 {
			paths = []string{"worktree state changed"}
		}
		return fmt.Errorf("review: source tree changed during review: %s", strings.Join(paths, ", "))
	}
	report := review.BuildReport(manifest, cohorts, sweep)
	state = review.StateAfterReport(report, after.HeadSHA)
	if _, err := review.CheckArtifactPaths(root, artifactPaths); err != nil {
		return err
	}
	publicationGit := git
	publicationGit.Runner = reviewPublicationRunner{paths: resolvedPaths, runner: git.Runner}
	beforePublication, err := publicationGit.WorktreeState(ctx, root)
	if err != nil {
		return err
	}
	if err := review.WriteArtifacts(directory, report, state); err != nil {
		return err
	}
	if err := reviewSessionError(ctx, publicationGit, root, beforePublication, trackedBaseline, nil); err != nil {
		return err
	}
	if err := review.WriteIncrementalState(statePath, state); err != nil {
		return err
	}
	if err := reviewSessionError(ctx, publicationGit, root, beforePublication, trackedBaseline, nil); err != nil {
		return err
	}
	if err := review.PrintReport(stdout, report); err != nil {
		return err
	}
	switch report.Verdict {
	case review.Ship:
		return nil
	case review.FixBeforeShip:
		return &ExitError{Code: 2, State: string(report.Verdict)}
	default:
		return &ExitError{Code: 3, State: string(report.Verdict)}
	}
}

func reviewSessionError(ctx context.Context, git publication.GitClient, root string, baseline publication.WorktreeState, trackedBaseline map[string]string, sessionErr error) error {
	after, stateErr := git.WorktreeState(ctx, root)
	if stateErr == nil && after == baseline {
		return sessionErr
	}
	paths := changedTrackedPaths(root, baseline.HeadSHA, trackedBaseline)
	if len(paths) == 0 {
		paths = []string{"worktree state changed"}
	}
	return fmt.Errorf("review: source tree changed during review: %s", strings.Join(paths, ", "))
}

// Exclude only the authorized artifact files from the publication tree guard;
// other tracked, staged and untracked changes still invalidate the review.
type reviewPublicationRunner struct {
	paths  []string
	runner publication.CommandRunner
}

func (r reviewPublicationRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	if len(command.Args) > 0 {
		switch command.Args[0] {
		case "status", "diff", "ls-files":
			command.Args = append(command.Args, "--", ".")
			for _, filename := range r.paths {
				relative, err := filepath.Rel(command.Directory, filename)
				if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					command.Args = append(command.Args, ":(top,literal,exclude)"+filepath.ToSlash(relative))
				}
			}
		}
	}
	return r.runner.Run(ctx, command)
}

func reviewSlug(root, spec string) (string, error) {
	if spec != "" {
		name := strings.TrimSuffix(filepath.Base(spec), filepath.Ext(spec))
		name = strings.TrimPrefix(name, "plan-")
		return name, nil
	}
	output, err := exec.Command("git", "-C", root, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return "detached-head", nil
	}
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(string(output))) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "branch"
	}
	return slug, nil
}

func reviewBranchIdentity(root string) (string, error) {
	output, err := exec.Command("git", "-C", root, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	output, err = exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("review: determine branch identity: %w", err)
	}
	return "detached-head:" + strings.TrimSpace(string(output)), nil
}

func reviewSpecPath(root, spec string) (string, error) {
	paths := []string{spec}
	if !filepath.IsAbs(spec) && !strings.ContainsAny(spec, "/\\") && filepath.Ext(spec) == "" {
		paths = []string{routing.PlanPath(spec), filepath.Join(".batuta", "plans", "done", spec+".md"), filepath.Join(".batuta", "plan-"+spec+".md")}
	}
	for _, filename := range paths {
		if !filepath.IsAbs(filename) {
			filename = filepath.Join(root, filename)
		}
		if _, err := os.Stat(filename); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("review: resolve spec path: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(filename)
		if err != nil {
			return "", fmt.Errorf("review: resolve spec path: %w", err)
		}
		return filepath.Clean(resolved), nil
	}
	return "", fmt.Errorf("review: plan %q is unavailable", spec)
}

func reviewStateKey(branchSlug, branchIdentity, specSlug, resolvedSpecPath string) string {
	name := branchSlug
	if specSlug != "" {
		name += "-" + specSlug
	}
	digest := sha256.Sum256([]byte(branchIdentity + "\x00" + resolvedSpecPath))
	return fmt.Sprintf("%s-%x", name, digest[:8])
}

func defaultReviewBase(root string) (string, error) {
	for _, candidate := range []string{"main", "origin/main", "master", "origin/master", "@{upstream}", "HEAD^", "HEAD"} {
		cmd := exec.Command("git", "-C", root, "merge-base", "HEAD", candidate)
		if output, err := cmd.Output(); err == nil && strings.TrimSpace(string(output)) != "" {
			return strings.TrimSpace(string(output)), nil
		}
	}
	return "", errors.New("review: cannot determine a default base; pass --base <ref>")
}

func trackedChangeSignatures(root, beforeHead string) (map[string]string, error) {
	output, err := exec.Command("git", "-C", root, "diff", "--name-only", "-z", beforeHead, "--").Output()
	if err != nil {
		return nil, fmt.Errorf("review: list tracked changes: %w", err)
	}
	signatures := make(map[string]string)
	for _, name := range bytes.Split(output, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		diff, err := exec.Command("git", "-C", root, "diff", "--binary", "--no-ext-diff", beforeHead, "--", string(name)).Output()
		if err != nil {
			return nil, fmt.Errorf("review: inspect tracked change %q: %w", name, err)
		}
		signatures[string(name)] = string(diff)
	}
	return signatures, nil
}

func changedTrackedPaths(root, beforeHead string, baseline map[string]string) []string {
	after, err := trackedChangeSignatures(root, beforeHead)
	if err != nil {
		return nil
	}
	var paths []string
	for name, signature := range after {
		if baseline[name] != signature {
			paths = append(paths, name)
		}
	}
	for name := range baseline {
		if _, exists := after[name]; !exists {
			paths = append(paths, name)
		}
	}
	sort.Strings(paths)
	return paths
}

func runWatch(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("watch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	interval := flags.Duration("interval", 500*time.Millisecond, "journal poll interval")
	once := flags.Bool("once", false, "print one dashboard snapshot")
	ascii := flags.Bool("ascii", false, "use ASCII borders and status glyphs")
	var lang string
	flags.Func("lang", "label language (en or pt)", func(value string) error {
		if value != "en" && value != "pt" {
			return errors.New("language must be en or pt")
		}
		lang = value
		return nil
	})
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: batuta watch [<delivery>] [--interval 500ms] [--once] [--lang en|pt] [--ascii]")
		flags.PrintDefaults()
		fmt.Fprintln(stderr, "Keys: Up/Down and PgUp/PgDn scroll; f follows; r opens the answer editor; R shows the answer command; d opens the delivery picker; o opens the log; l changes focus; ? shows the legend; q quits; the mouse wheel scrolls the focused panel. In the editor, ctrl+enter, alt+enter or ctrl+s submits; enter inserts a newline; esc cancels.")
	}
	delivery := ""
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		rest := flags.Args()
		if len(rest) == 0 {
			break
		}
		if delivery != "" {
			return errors.New("watch accepts at most one delivery")
		}
		delivery, args = rest[0], rest[1:]
	}
	if lang != "" {
		if err := os.Setenv("BATUTA_LANG", lang); err != nil {
			return err
		}
	}
	if *ascii {
		if err := os.Setenv("LC_ALL", "C"); err != nil {
			return err
		}
	}
	if *once {
		return loop.Snapshot("", delivery, stdout)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return loop.Watch(ctx, "", delivery, *interval, stdout)
}

func runTrail(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("trail", flag.ContinueOnError)
	workspace := flags.String("workspace", "", "repository root (default: current directory)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	delivery := ""
	if rest := flags.Args(); len(rest) > 0 {
		delivery = rest[0]
	}
	return loop.Trail(*workspace, delivery, stdout)
}

func printDoctor(w io.Writer, report doctorReport) {
	fmt.Fprintf(w, "workspace   %s\n", report.Workspace)
	if report.GitRepository {
		state := "status unknown"
		switch report.GitState {
		case "clean":
			state = "clean tree"
		case "managed":
			state = "managed state only (WORK.md, .batuta/) — fine for /batuta, commit before batuta loop"
		case "dirty":
			state = "dirty tree — commit or stash before delegating"
		default:
			if report.GitClean != nil {
				state = "dirty tree — commit or stash before delegating"
				if *report.GitClean {
					state = "clean tree"
				}
			}
		}
		fmt.Fprintf(w, "git         repository ✓ %s (%s)\n", state, report.GitToplevel)
	} else {
		fmt.Fprintln(w, "git         not a repository — /batuta-init will offer to initialize it")
	}
	if report.SkillsPath != "" {
		fmt.Fprintf(w, "skills      %s\n", report.SkillsPath)
	} else {
		fmt.Fprintln(w, "skills      not found — npx skills add batuta-ai/skills")
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%-13s %-12s %-14s %7s  %s\n", "executor", "available", "version", "models", "notes")
	for _, executor := range report.Executors {
		notes := executor.Credential
		if len(executor.Diagnostics) > 0 {
			notes = strings.Join(executor.Diagnostics, ",")
		}
		fmt.Fprintf(w, "%-13s %-12s %-14s %7d  %s\n", executor.ID, executor.Availability, truncate(executor.Version, 14), executor.Models, notes)
	}
	for _, probe := range report.ProbeDurations {
		if probe.Duration > 5*time.Second {
			fmt.Fprintf(w, "note: %s %s took %.1fs (budget 5s)\n", probe.Executor, probe.Probe, probe.Duration.Seconds())
		}
	}
}

func truncate(value string, width int) string {
	if len(value) <= width {
		return value
	}
	return value[:width-1] + "…"
}
