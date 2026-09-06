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
	"strings"
	"time"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/worktree"
)

var stdin io.Reader = os.Stdin

func runGate(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("a gate name is required; available gates: proofs, scope, tests, tree, verifier")
	}
	switch args[0] {
	case "proofs":
		return runGateProofs(args[1:], stdout)
	case "scope":
		return runGateScope(args[1:], stdout)
	case "tests":
		return runGateTests(args[1:], stdout)
	case "tree":
		return runGateTree(args[1:], stdout)
	case "verifier":
		return runGateVerifier(args[1:], stdout)
	default:
		return fmt.Errorf("unknown gate %q; available gates: proofs, scope, tests, tree, verifier", args[0])
	}
}

func runGateProofs(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("gate proofs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	accept := flags.String("accept", "", "semicolon-separated acceptance criteria and proof commands")
	dir := flags.String("dir", "", "workspace (default: current directory)")
	timeout := flags.Duration("timeout", 15*time.Minute, "proof command timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*accept) == "" {
		return errors.New("usage: batuta gate proofs --accept \"<criterion → proof>;...\" [--dir <d>] [--timeout <duration>]")
	}

	root, err := workspaceRoot(*dir)
	if err != nil {
		return err
	}
	shell, err := gates.NewShellRunner(*timeout)
	if err != nil {
		return err
	}
	criteria := gates.ParseCriteria(strings.Split(*accept, ";"))
	verdicts := gates.Proofs(context.Background(), shell, root, criteria)
	if err := json.NewEncoder(stdout).Encode(verdicts); err != nil {
		return err
	}
	for _, verdict := range verdicts {
		if !verdict.Pass {
			return &ExitError{Code: 2, State: "proofs failed"}
		}
	}
	return nil
}

func runGateScope(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("gate scope", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	base := flags.String("base", "", "base commit or ref")
	scopeFlag := flags.String("scope", "", "comma-separated Scope paths")
	dir := flags.String("dir", "", "git repository (default: current directory)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	scopeSet := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "scope" {
			scopeSet = true
		}
	})
	if flags.NArg() != 0 || strings.TrimSpace(*base) == "" || !scopeSet {
		return errors.New("usage: batuta gate scope --base <sha-or-ref> --scope <a,b,c> [--dir <d>]")
	}

	root, err := workspaceRoot(*dir)
	if err != nil {
		return err
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("find git: %w", err)
	}
	command := exec.CommandContext(context.Background(), git, "rev-parse", "--verify", *base+"^{commit}")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("resolve base %q: %w", *base, err)
	}
	sha := strings.TrimSpace(string(output))

	provider, err := worktree.New(context.Background(), root)
	if err != nil {
		return err
	}
	changed, err := provider.ChangedPaths(context.Background(), root, sha)
	if err != nil {
		return err
	}
	scope := splitScope(*scopeFlag)
	verdict := gates.Scope(changed, scope)
	outside := make([]string, 0)
	managed := make([]string, 0)
	for _, changedPath := range changed {
		if worktree.IsManaged(changedPath) {
			managed = append(managed, changedPath)
		} else if !gates.InScope(changedPath, scope) {
			outside = append(outside, changedPath)
		}
	}
	if err := json.NewEncoder(stdout).Encode(struct {
		Name    string   `json:"name"`
		Pass    bool     `json:"pass"`
		Signal  string   `json:"signal,omitempty"`
		Outside []string `json:"outside"`
		Managed []string `json:"managed"`
	}{
		Name: verdict.Name, Pass: verdict.Pass, Signal: verdict.Signal, Outside: outside, Managed: managed,
	}); err != nil {
		return err
	}
	if !verdict.Pass {
		return &ExitError{Code: 2, State: "scope failed"}
	}
	return nil
}

func splitScope(value string) []string {
	var scope []string
	for _, entry := range strings.Split(value, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			scope = append(scope, entry)
		}
	}
	return scope
}

func runGateTests(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("gate tests", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	command := flags.String("command", "", "test command")
	dir := flags.String("dir", "", "workspace (default: current directory)")
	timeout := flags.Duration("timeout", 15*time.Minute, "command timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*command) == "" {
		return errors.New("usage: batuta gate tests --command \"<cmd>\" [--dir <d>] [--timeout <duration>]")
	}

	root, err := workspaceRoot(*dir)
	if err != nil {
		return err
	}
	shell, err := gates.NewShellRunner(*timeout)
	if err != nil {
		return err
	}
	verdict := gates.Tests(context.Background(), shell, root, *command)
	if err := json.NewEncoder(stdout).Encode(verdict); err != nil {
		return err
	}
	if !verdict.Pass {
		return &ExitError{Code: 2, State: "tests failed"}
	}
	return nil
}

func runGateVerifier(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("gate verifier", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	criteria := flags.Int("criteria", 0, "number of acceptance criteria")
	proofsJSON := flags.String("proofs", "", "JSON array of proof verdicts")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *criteria < 1 {
		return errors.New("usage: batuta gate verifier --criteria <n> [--proofs '<json array of gates.Verdict>'] < output")
	}

	var proofs []gates.Verdict
	proofsSet := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "proofs" {
			proofsSet = true
		}
	})
	if proofsSet {
		if err := json.Unmarshal([]byte(*proofsJSON), &proofs); err != nil {
			return fmt.Errorf("invalid --proofs JSON: %w", err)
		}
	}
	output, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("read verifier output: %w", err)
	}
	verdict := gates.Verifier(string(output), *criteria, proofs)
	if err := json.NewEncoder(stdout).Encode(verdict); err != nil {
		return err
	}
	if !verdict.Pass {
		return &ExitError{Code: 2, State: "verifier failed"}
	}
	return nil
}

func runGateTree(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("gate tree", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshot := flags.Bool("snapshot", false, "print the current tree signature")
	beforeJSON := flags.String("before", "", "compare against a tree signature")
	dir := flags.String("dir", "", "git repository (default: current directory)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: batuta gate tree --snapshot [--dir <d>] or batuta gate tree --before '<json>' [--dir <d>]")
	}
	beforeSet := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "before" {
			beforeSet = true
		}
	})
	if *snapshot == beforeSet {
		return errors.New("exactly one form is required: --snapshot or --before '<json>'")
	}

	root, err := workspaceRoot(*dir)
	if err != nil {
		return err
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("find git: %w", err)
	}
	state, err := (publication.GitClient{Executable: git, Runner: publication.ExecRunner{}}).WorktreeState(context.Background(), root)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	if *snapshot {
		return encoder.Encode(state)
	}

	var before publication.WorktreeState
	if err := json.Unmarshal([]byte(*beforeJSON), &before); err != nil {
		return fmt.Errorf("invalid --before tree signature: %w", err)
	}
	verdict := gates.Tree(before, state)
	return encoder.Encode(struct {
		Name    string `json:"name"`
		Pass    bool   `json:"pass"`
		Changed bool   `json:"changed"`
		Signal  string `json:"signal"`
	}{
		Name: verdict.Name, Pass: verdict.Pass, Changed: before != state, Signal: verdict.Signal,
	})
}
