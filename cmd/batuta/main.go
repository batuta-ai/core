// Command batuta is the deterministic side of the Batuta conducting cycle.
//
// Subcommands available today: version, inventory, doctor. The verification
// gates, run trails and the unattended loop land in later releases.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/batuta-ai/core/inventory"
	"github.com/batuta-ai/core/inventory/adapters"
	"github.com/batuta-ai/core/publication"
)

const usage = `batuta — the conductor's deterministic tools

Usage:
  batuta version
  batuta inventory [--workspace <dir>] [--timeout <duration>]
  batuta doctor    [--workspace <dir>] [--json]

inventory  Redacted snapshot of the executor CLIs installed on this machine
           (codex, opencode, cursor-agent, claude, agy, compozy): versions,
           provider/model bindings, credential state, diagnostics. JSON.
doctor     What a conductor needs to know before the first cycle: which
           executors run, whether the workspace is a git repository, and
           whether the Batuta skills are installed. Human text, or --json.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
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
	case "inventory":
		return runInventory(args[1:], stdout)
	case "doctor":
		return runDoctor(args[1:], stdout)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
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
	GitExecutable string           `json:"git_executable,omitempty"`
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
	report := doctorReport{Workspace: root, Digest: snapshot.Digest}
	if git, err := exec.LookPath("git"); err == nil {
		report.GitExecutable = git
		_, statErr := os.Stat(filepath.Join(root, ".git"))
		report.GitRepository = statErr == nil
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

func findSkills(root string) string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(root, ".agents", "skills", "batuta"),
		filepath.Join(root, ".claude", "skills", "batuta"),
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".agents", "skills", "batuta"),
			filepath.Join(home, ".claude", "skills", "batuta"),
			filepath.Join(home, ".codex", "skills", "batuta"),
			filepath.Join(home, ".config", "opencode", "skills", "batuta"),
		)
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "SKILL.md")); err == nil {
			return candidate
		}
	}
	return ""
}

func printDoctor(w io.Writer, report doctorReport) {
	fmt.Fprintf(w, "workspace   %s\n", report.Workspace)
	if report.GitRepository {
		fmt.Fprintln(w, "git         repository ✓")
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
