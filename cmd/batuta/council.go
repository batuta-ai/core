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
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/batuta-ai/core/council"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/review"
	"github.com/batuta-ai/core/routing"
)

var councilRunOptions = func(root string, parallel int, timeout time.Duration) council.RunOptions {
	return council.RunOptions{Root: root, Parallel: parallel, Timeout: timeout}
}

// councilArtifact is council.json: everything the report derives from.
type councilArtifact struct {
	PlanDigest   string                          `json:"plan_digest"`
	Critiques    []council.Critique              `json:"critiques"`
	CrossReviews []council.CrossReview           `json:"cross_reviews"`
	Aggregate    council.AggregateResult         `json:"aggregate"`
	Synthesis    string                          `json:"synthesis"`
	Failures     []council.CouncilFailure        `json:"failures"`
	Labels       map[string]routing.RuntimeValue `json:"labels"`
}

func runCouncil(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("council", flag.ContinueOnError)
	flags.SetOutput(stderr)
	planFile := flags.String("plan", "", "plan file to judge")
	parallel := flags.Int("parallel", 1, "counsellor sessions to run in parallel")
	timeout := flags.Duration("timeout", 10*time.Minute, "timeout of each session")
	out := flags.String("out", "", "artifact directory (default: .batuta/councils/<date>-<plan slug>)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("council accepts flags only")
	}
	if *planFile == "" {
		return errors.New("council requires --plan <file>")
	}
	if *parallel < 1 || *timeout <= 0 {
		return errors.New("council requires positive --parallel and --timeout values")
	}
	root, err := workspaceRoot("")
	if err != nil {
		return err
	}
	plan, err := os.ReadFile(*planFile)
	if err != nil {
		return fmt.Errorf("council: read plan: %w", err)
	}
	slug, err := reviewSlug(root, *planFile)
	if err != nil {
		return err
	}
	directory := *out
	if directory == "" {
		directory = filepath.Join(root, ".batuta", "councils", reviewNow().Format("2006-01-02")+"-"+slug)
	} else if !filepath.IsAbs(directory) {
		directory = filepath.Join(root, directory)
	}
	artifacts := []string{filepath.Join(directory, "council.json"), filepath.Join(directory, "council.md")}
	if _, err := review.CheckArtifactPaths(root, artifacts); err != nil {
		return err
	}
	tablePayload, err := os.ReadFile(filepath.Join(root, ".batuta", "routing.md"))
	if err != nil {
		return errors.New("council: .batuta/routing.md is missing — run /batuta-init first")
	}
	table, err := routing.ParseRoutingTable(tablePayload)
	if err != nil {
		return fmt.Errorf("council: %w", err)
	}
	profile, err := loop.LoadProfile(root)
	if err != nil {
		return fmt.Errorf("council: %w", err)
	}
	options := councilRunOptions(root, *parallel, *timeout)
	if options.Skills == "" {
		if options.Skills, err = loop.FindSkills(root, ""); err != nil {
			return err
		}
	}
	conventions, missing := loop.Conventions(options.Skills, profile.Template)
	if len(missing) > 0 {
		return fmt.Errorf("council: missing rubric templates: %s", strings.Join(missing, ", "))
	}
	if own := profile.ConventionsSection(); own != "" {
		conventions = append([]string{"### From .batuta/profile.md\n\n" + own}, conventions...)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, runErr := council.Run(ctx, string(plan), conventions, table, options)
	// Run returns a report-bearing result only after the sessions started; any
	// other error leaves the label map empty and has nothing to write.
	if runErr != nil && len(result.Labels) == 0 {
		return runErr
	}
	artifact := councilArtifact{
		PlanDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(plan)),
		Critiques:  nonNil(result.Critiques), CrossReviews: nonNil(result.Reviews),
		Aggregate: result.Aggregate, Synthesis: result.Synthesis,
		Failures: nonNil(result.Failures), Labels: result.Labels,
	}
	if artifact.Aggregate.Findings == nil {
		artifact.Aggregate.Findings = []council.AggregatedFinding{}
	}
	if artifact.Aggregate.Rankings == nil {
		artifact.Aggregate.Rankings = []council.Ranking{}
	}
	complete := runErr == nil && artifact.Aggregate.Recommendation != council.Incomplete
	if !complete {
		artifact.Aggregate.Recommendation = council.Incomplete
	}
	report := councilReport(*planFile, artifact, complete)
	payload, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("council: create artifact directory: %w", err)
	}
	if err := writeCouncilArtifacts(directory, map[string][]byte{
		"council.json": append(payload, '\n'),
		"council.md":   []byte(report),
	}); err != nil {
		return fmt.Errorf("council: %w", err)
	}
	if _, err := io.WriteString(stdout, report); err != nil {
		return err
	}
	if runErr != nil {
		fmt.Fprintln(stderr, "council:", runErr)
		return &ExitError{Code: 4, State: "incomplete"}
	}
	if !complete {
		fmt.Fprintln(stderr, "council: fewer than two cross-reviews parsed")
		return &ExitError{Code: 4, State: "incomplete"}
	}
	if artifact.Aggregate.Recommendation == council.Revise {
		return &ExitError{Code: 2, State: "revise"}
	}
	return nil
}

func writeCouncilArtifacts(directory string, files map[string][]byte) error {
	return writeCouncilArtifactsWithRename(directory, files, os.Rename)
}

type councilArtifactInstall struct {
	name      string
	path      string
	staged    string
	backup    string
	installed bool
	previous  bool
}

func writeCouncilArtifactsWithRename(directory string, files map[string][]byte, rename func(string, string) error) (result error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	installs := make([]councilArtifactInstall, 0, len(names))
	for _, name := range names {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check %s: %w", name, err)
		}
		if err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("check %s: destination is not a regular file", name)
		}
		installs = append(installs, councilArtifactInstall{name: name, path: path, previous: err == nil})
	}
	cleanup := true
	defer func() {
		if cleanup {
			result = errors.Join(result, cleanupCouncilArtifacts(installs))
		}
	}()
	for i := range installs {
		install := &installs[i]
		staged, err := stageCouncilArtifact(directory, install.name, files[install.name])
		if err != nil {
			return fmt.Errorf("write %s: %w", install.name, err)
		}
		install.staged = staged
	}
	for i := range installs {
		install := &installs[i]
		if !install.previous {
			continue
		}
		previous, err := os.ReadFile(install.path)
		if err != nil {
			return fmt.Errorf("backup %s: %w", install.name, err)
		}
		backup, err := stageCouncilArtifact(directory, install.name+"-backup", previous)
		if err != nil {
			return fmt.Errorf("backup %s: %w", install.name, err)
		}
		install.backup = backup
	}
	for i := range installs {
		install := &installs[i]
		if err := rename(install.staged, install.path); err != nil {
			rollbackErr := rollbackCouncilArtifacts(installs[:i], rename)
			if rollbackErr != nil {
				cleanup = false
			}
			return errors.Join(fmt.Errorf("write %s: %w", install.name, err), rollbackErr)
		}
		install.staged = ""
		install.installed = true
	}
	return nil
}

func rollbackCouncilArtifacts(installs []councilArtifactInstall, rename func(string, string) error) error {
	var rollbackErr error
	for i := len(installs) - 1; i >= 0; i-- {
		install := installs[i]
		if !install.installed {
			continue
		}
		if install.previous {
			if err := rename(install.backup, install.path); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore %s: %w", install.name, err))
			}
		} else if err := os.Remove(install.path); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove %s: %w", install.name, err))
		}
	}
	return rollbackErr
}

func cleanupCouncilArtifacts(installs []councilArtifactInstall) error {
	var cleanupErr error
	for _, install := range installs {
		for _, path := range []string{install.staged, install.backup} {
			if path == "" {
				continue
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("clean up %s: %w", path, err))
			}
		}
	}
	return cleanupErr
}

func stageCouncilArtifact(directory, name string, payload []byte) (string, error) {
	temporary, err := os.CreateTemp(directory, "."+name+"-*")
	if err != nil {
		return "", err
	}
	temporaryName := temporary.Name()
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		os.Remove(temporaryName)
		return "", err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		os.Remove(temporaryName)
		return "", err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryName)
		return "", err
	}
	return temporaryName, nil
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func councilReport(planFile string, artifact councilArtifact, complete bool) string {
	var b bytes.Buffer
	recommendation := string(artifact.Aggregate.Recommendation)
	if !complete {
		recommendation = "INCOMPLETE"
	}
	fmt.Fprintf(&b, "# Council report\n\nPlan: %s (%s)\nRecommendation: %s\n\n", planFile, artifact.PlanDigest, recommendation)
	b.WriteString("The council is advice for the maintainer; it never approves a plan.\n\n")
	if artifact.Aggregate.Recommendation == council.Incomplete {
		b.WriteString("Incomplete: fewer than two critiques or cross-reviews parsed, so the findings and ranking below are not cross-checked.\n\n")
	}
	b.WriteString("## Chairman synthesis\n\n")
	if strings.TrimSpace(artifact.Synthesis) == "" {
		b.WriteString("No synthesis was produced.\n")
	} else {
		b.WriteString(strings.TrimSpace(artifact.Synthesis) + "\n")
	}
	b.WriteString("\n## Findings\n\n")
	if len(artifact.Aggregate.Findings) == 0 {
		b.WriteString("None.\n")
	}
	for _, finding := range artifact.Aggregate.Findings {
		fmt.Fprintf(&b, "- Task %d · %s · %s — fix: %s (support %d/%d: %s)\n", finding.Task, finding.Severity, finding.Claim, finding.Fix, finding.Support, len(artifact.Critiques), strings.Join(finding.IDs, ", "))
	}
	b.WriteString("\n## Ranking\n\n")
	if len(artifact.Aggregate.Rankings) == 0 {
		b.WriteString("None.\n")
	} else {
		b.WriteString("| Position | Label | Average rank | Rankings | Counsellor |\n|---|---|---|---|---|\n")
	}
	for i, rank := range artifact.Aggregate.Rankings {
		runtime := artifact.Labels[rank.Label]
		fmt.Fprintf(&b, "| %d | %s | %.2f | %d | %s/%s |\n", i+1, rank.Label, rank.Average, rank.Count, runtime.Provider, runtime.Model)
	}
	b.WriteString("\n## Failures\n\n")
	if len(artifact.Failures) == 0 {
		b.WriteString("None.\n")
	}
	for _, failure := range artifact.Failures {
		fmt.Fprintf(&b, "- %s · %s · %s\n", failure.Label, failure.Stage, failure.Reason)
	}
	return b.String()
}
