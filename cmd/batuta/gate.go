package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/publication"
)

func runGate(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("a gate name is required; available gates: tests, tree")
	}
	switch args[0] {
	case "tests":
		return runGateTests(args[1:], stdout)
	case "tree":
		return runGateTree(args[1:], stdout)
	default:
		return fmt.Errorf("unknown gate %q; available gates: tests, tree", args[0])
	}
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
