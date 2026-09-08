package review

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/batuta-ai/core/publication"
)

var lintDiagnostic = regexp.MustCompile(`^(.+?):([0-9]+)(?::([0-9]+))?:[ \t]*(.+)$`)

type LintResult struct {
	Command     string          `json:"command"`
	ExitCode    int             `json:"exit_code"`
	Diagnostics []LinterFinding `json:"diagnostics"`
}

// ProfileLintCommand extracts the first optional Lint line from profile prose.
func ProfileLintCommand(profile string) string {
	for _, line := range strings.Split(profile, "\n") {
		if strings.HasPrefix(line, "Lint:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Lint:"))
		}
	}
	return ""
}

// RunLint invokes the project-authored command through the shell. A nonzero
// exit is retained because linters commonly use it to signal diagnostics.
func RunLint(ctx context.Context, root, command string, manifest Manifest, runner publication.CommandRunner) (LintResult, error) {
	result := LintResult{Command: command}
	if strings.TrimSpace(command) == "" {
		return result, nil
	}
	if runner == nil {
		runner = publication.ExecRunner{}
	}
	outcome, err := runner.Run(ctx, publication.Command{
		Executable:  "/bin/sh",
		Args:        []string{"-c", command},
		Directory:   root,
		Environment: []string{"CI=1", "BATUTA=1"},
		StdoutLimit: 4 << 20,
		StderrLimit: 1 << 20,
	})
	result.ExitCode = outcome.ExitCode
	output := string(outcome.Stdout)
	if len(outcome.Stderr) > 0 {
		output += "\n" + string(outcome.Stderr)
	}
	result.Diagnostics = ParseLintDiagnostics(root, output, manifest)
	if err != nil && outcome.ExitCode < 0 {
		return result, fmt.Errorf("review: run lint: %w", err)
	}
	return result, nil
}

// ParseLintDiagnostics accepts path:line[:col]: message records and retains
// only diagnostics anchored to selected new-side lines in the manifest.
func ParseLintDiagnostics(root, output string, manifest Manifest) []LinterFinding {
	var diagnostics []LinterFinding
	for _, line := range strings.Split(output, "\n") {
		match := lintDiagnostic.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if match == nil {
			continue
		}
		name := filepath.Clean(match[1])
		if filepath.IsAbs(name) {
			relative, err := filepath.Rel(root, name)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			name = relative
		}
		name = path.Clean(filepath.ToSlash(name))
		lineNumber, err := strconv.Atoi(match[2])
		if err != nil || !manifestContainsLine(manifest, name, lineNumber) {
			continue
		}
		diagnostics = append(diagnostics, LinterFinding{File: name, Line: lineNumber, Rule: strings.TrimSpace(match[4])})
	}
	return diagnostics
}

func manifestContainsLine(manifest Manifest, name string, line int) bool {
	for _, file := range manifest.Files {
		if file.Path != name || !file.Selected || file.Ignored {
			continue
		}
		for _, hunk := range file.Hunks {
			if line >= hunk.Start && line < hunk.Start+hunk.Count {
				return true
			}
		}
	}
	return false
}

func suppressLintOverlaps(findings []Finding, diagnostics []LinterFinding) MergeResult {
	var result MergeResult
	for _, finding := range findings {
		matched := false
		for _, diagnostic := range diagnostics {
			if path.Clean(finding.File) == path.Clean(diagnostic.File) && finding.Line <= rangeEnd(diagnostic.Line, diagnostic.EndLine) && diagnostic.Line <= rangeEnd(finding.Line, finding.EndLine) {
				result.Suppressed = append(result.Suppressed, SuppressedFinding{Finding: finding, Linter: diagnostic, Reason: "linter overlap: " + diagnostic.Rule})
				matched = true
				break
			}
		}
		if !matched {
			result.Findings = append(result.Findings, finding)
		}
	}
	return result
}
