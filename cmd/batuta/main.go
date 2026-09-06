// Command batuta is the deterministic side of the Batuta conducting cycle.
//
// Subcommands: version, capabilities, inventory, doctor, loop, trail. The
// standalone verification gates (`gate`) land in a later release; hosts
// probe `batuta capabilities` before relying on any of them.
package main

import (
	"bytes"
	"context"
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
	"strings"
	"syscall"
	"time"

	"github.com/batuta-ai/core/inventory"
	"github.com/batuta-ai/core/inventory/adapters"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
)

const usage = `batuta — the conductor's deterministic tools

Usage:
  batuta version
  batuta capabilities
  batuta inventory [--workspace <dir>] [--timeout <duration>]
  batuta doctor    [--workspace <dir>] [--json] [--timeout <duration>]
  batuta loop      [--dry-run] [--parallel N] [--skills <dir>] [<plan>]
  batuta loop      --resume <delivery> | --answer <task> "<text>" | --abandon <delivery>
  batuta loop      --dashboard [<delivery>]
  batuta trail     [<delivery>]

capabilities  The subcommands this binary ships, as JSON. Skills probe it
           before calling gate or loop; an older binary fails the probe.

loop       The mechanical conductor over an approved plan
           (.batuta/plan-<slug>.md): routing from .batuta/routing.md, one
           executor session per task in its own worktree through the
           adapter, the four gates, retry then escalation, one commit per
           task integrated onto the checked-out branch, everything
           journaled under .batuta/journal/. Exit 0 when every task
           integrated; 2 blocked; 3 waiting for an answer; 130 canceled.
trail      One line per journal record of a delivery (the latest by
           default).

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
	case "trail":
		return runTrail(args[1:], stdout)
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

// commands lists every subcommand this binary ships. Append gate here when
// it lands; skills read this list, never the usage text.
var commands = []string{"capabilities", "doctor", "inventory", "loop", "trail", "version"}

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
	collector, err := adapters.NewCollector(publication.ExecRunner{}, adapters.CollectorOptions{
		TrustedWorkspace: root, WorkspaceID: "local",
		CompozyExecutable: found.Compozy, CodexExecutable: found.Codex,
		OpenCodeExecutable: found.OpenCode, CursorExecutable: found.Cursor,
		ClaudeExecutable: found.Claude, AgyExecutable: found.Agy,
		ProbeParallelism: 8,
	})
	if err != nil {
		return inventory.InventorySnapshot{}, found, err
	}
	snapshot, err := collector.Collect(ctx)
	return snapshot, found, err
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
	Workspace     string           `json:"workspace"`
	GitRepository bool             `json:"git_repository"`
	GitToplevel   string           `json:"git_toplevel,omitempty"`
	GitClean      *bool            `json:"git_clean,omitempty"`
	GitExecutable string           `json:"git_executable,omitempty"`
	Commands      []string         `json:"commands"`
	SkillsPath    string           `json:"skills_path,omitempty"`
	Executors     []doctorExecutor `json:"executors"`
	Digest        string           `json:"inventory_digest"`
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
	snapshot, found, err := collect(ctx, root)
	if err != nil {
		return err
	}
	report := doctorReport{Workspace: root, Digest: snapshot.Digest, Commands: commands}
	if git, err := exec.LookPath("git"); err == nil {
		report.GitExecutable = git
		// The probe context may already be spent by collect; git gets its own.
		gitCtx, cancelGit := context.WithTimeout(context.Background(), 5*time.Second)
		report.GitToplevel, report.GitClean = inspectGit(gitCtx, git, root)
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
// whether the working tree is clean, which the conducting preflight needs.
func inspectGit(ctx context.Context, git, root string) (toplevel string, clean *bool) {
	out, err := exec.CommandContext(ctx, git, "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", nil
	}
	toplevel = strings.TrimSpace(string(out))
	status, err := exec.CommandContext(ctx, git, "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return toplevel, nil
	}
	isClean := len(bytes.TrimSpace(status)) == 0
	return toplevel, &isClean
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

func runLoop(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("loop", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "repository root (default: current directory)")
	skills := flags.String("skills", "", "batuta skill directory holding adapters/ and templates/ (default: auto-detected)")
	dryRun := flags.Bool("dry-run", false, "show the waves, executors and worktrees; run nothing")
	resume := flags.String("resume", "", "continue a delivery from its journal")
	abandon := flags.String("abandon", "", "close a delivery that will not continue; ticks what integrated")
	answer := flags.String("answer", "", "task (task_N or N) to answer; the text follows as the next argument")
	dashboard := flags.Bool("dashboard", false, "print the state of the open deliveries as TSV")
	parallel := flags.Int("parallel", 0, "executors per wave, at most 4 (default: the profile's Execution line)")
	taskTimeout := flags.Duration("task-timeout", 45*time.Minute, "time budget per executor session")
	testTimeout := flags.Duration("test-timeout", 15*time.Minute, "time budget for the test command and each proof")
	maxWaves := flags.Int("max-waves", 0, "stop after N waves (the delivery stays resumable)")
	keep := flags.Bool("keep-worktrees", false, "keep task worktrees after integration or abort")
	maxLimitWaits := flags.Int("max-limit-waits", 20, "consecutive usage-limit waits one attempt may take")
	limitWait := flags.Duration("limit-wait", 30*time.Minute, "wait when a usage-limit message names no reset time")
	if err := flags.Parse(args); err != nil {
		return err
	}
	rest := flags.Args()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *dashboard {
		delivery := ""
		if len(rest) > 0 {
			delivery = rest[0]
		}
		return loop.Dashboard(*workspace, delivery, stdout)
	}
	opts := loop.Options{
		Workspace: *workspace, Skills: *skills, Parallel: *parallel, TaskTimeout: *taskTimeout, TestTimeout: *testTimeout,
		MaxWaves: *maxWaves, KeepWorktrees: *keep, MaxLimitWaits: *maxLimitWaits, LimitWaitDefault: *limitWait,
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
	switch state {
	case loop.StateDone:
		return nil
	case loop.StateWaitingInput:
		return &ExitError{Code: 3, State: state}
	case loop.StateCanceled:
		return &ExitError{Code: 130, State: state}
	default:
		return &ExitError{Code: 2, State: state}
	}
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
		if report.GitClean != nil {
			state = "dirty tree — commit or stash before delegating"
			if *report.GitClean {
				state = "clean tree"
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
}

func truncate(value string, width int) string {
	if len(value) <= width {
		return value
	}
	return value[:width-1] + "…"
}
