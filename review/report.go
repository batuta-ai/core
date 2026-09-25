package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/batuta-ai/core/executor"
)

// Report is the deterministic reduction of cohort and optional spec results.
type Report struct {
	Manifest   Manifest       `json:"manifest"`
	Cohorts    []CohortResult `json:"cohorts"`
	Findings   []Finding      `json:"findings"`
	Spec       *SpecSweep     `json:"spec,omitempty"`
	Suppressed int            `json:"suppressed_overlaps"`
	Verdict    Decision       `json:"verdict"`
}

// BuildReport merges duplicate findings, orders them by severity and derives
// the verdict. Incomplete reviewer coverage is never allowed to ship.
func BuildReport(manifest Manifest, cohorts []CohortResult, spec *SpecSweep) Report {
	report := Report{Manifest: manifest, Cohorts: append([]CohortResult(nil), cohorts...), Spec: spec}
	var findings []Finding
	covered := len(cohorts) == len(manifest.Cohorts)
	for _, cohort := range cohorts {
		findings = append(findings, cohort.Findings...)
		report.Suppressed += len(cohort.Suppressed)
		covered = covered && cohort.Covered && (cohort.Lint == nil || cohort.Lint.Error == "")
	}
	report.Findings = Merge(findings, nil).Findings
	slices.SortStableFunc(report.Findings, func(a, b Finding) int {
		if rank := severityRank(b.Severity) - severityRank(a.Severity); rank != 0 {
			return rank
		}
		return findingOrder(a, b)
	})
	var criteria []Criterion
	if spec != nil {
		covered = covered && spec.Covered
		criteria = spec.VerdictCriteria()
	}
	report.Verdict = Verdict(report.Findings, criteria)
	if !covered {
		report.Verdict = Rework
	}
	return report
}

// PrintReport writes the complete human walkthrough. WriteArtifacts uses this
// same function so review.md is byte-for-byte identical to stdout.
func PrintReport(w io.Writer, report Report) error {
	selected, added, deleted := 0, 0, 0
	for _, file := range report.Manifest.Files {
		if file.Selected {
			selected++
			added += file.Added
			deleted += file.Deleted
		}
	}
	covered := 0
	for _, cohort := range report.Cohorts {
		if cohort.Covered {
			covered++
		}
	}
	if _, err := fmt.Fprintf(w, "Review walkthrough\n\nBase: %s\nFiles: %d selected of %d changed (+%d -%d)\nCohorts: %d\nCoverage: %d/%d cohorts\n",
		report.Manifest.Base, selected, len(report.Manifest.Files), added, deleted, len(report.Manifest.Cohorts), covered, len(report.Manifest.Cohorts)); err != nil {
		return err
	}
	for _, cohort := range report.Cohorts {
		status := "covered"
		if !cohort.Covered {
			status = "uncovered: " + cleanReportText(cohort.Reason)
			if len(cohort.Attempts) > 0 {
				status += fmt.Sprintf(" (tail: cohort-%d.tail.txt)", cohort.Cohort+1)
			}
		}
		names := make([]string, len(cohort.Files))
		for i, name := range cohort.Files {
			names[i] = escapeReportPath(name)
		}
		details := strings.Join(names, ", ")
		if cohort.Cohort >= 0 && cohort.Cohort < len(report.Manifest.Cohorts) {
			manifestCohort := report.Manifest.Cohorts[cohort.Cohort]
			if manifestCohort.Oversized {
				details = fmt.Sprintf("%s (%d changed lines, oversized)", details, manifestCohort.ChangedLines)
			}
		}
		if _, err := fmt.Fprintf(w, "- Cohort %d: %s — %s\n", cohort.Cohort+1, details, status); err != nil {
			return err
		}
	}
	for _, cohort := range report.Cohorts {
		if cohort.Lint == nil {
			continue
		}
		lint := cohort.Lint
		status := "not configured"
		if strings.TrimSpace(lint.Command) != "" {
			status = fmt.Sprintf("completed (exit %d)", lint.ExitCode)
			if lint.Error != "" {
				status = fmt.Sprintf("failed (exit %d): %s", lint.ExitCode, cleanReportText(lint.Error))
			}
		}
		if _, err := fmt.Fprintf(w, "Lint: %s\n", status); err != nil {
			return err
		}
		break // Lint runs once for the entire cohort batch.
	}
	if _, err := fmt.Fprintln(w, "\nFindings:"); err != nil {
		return err
	}
	if len(report.Findings) == 0 {
		if _, err := fmt.Fprintln(w, "None."); err != nil {
			return err
		}
	}
	for _, finding := range report.Findings {
		if _, err := fmt.Fprintf(w, "%s · %s:%d · %s · %s\n", finding.Severity, escapeReportPath(finding.File), finding.Line, cleanReportText(finding.Premise), cleanReportText(finding.Fix)); err != nil {
			return err
		}
	}
	if report.Spec != nil {
		if _, err := fmt.Fprintln(w, "\nCriteria:\n| Criterion | Status | Evidence |\n|---|---|---|"); err != nil {
			return err
		}
		for _, result := range report.Spec.Results {
			if _, err := fmt.Fprintf(w, "| %s | %s | %s |\n", markdownCell(result.ID), result.Status, markdownCell(escapeReportPath(result.Path))); err != nil {
				return err
			}
		}
		if !report.Spec.Covered {
			if _, err := fmt.Fprintf(w, "\nSpec coverage: uncovered — %s\n", cleanReportText(report.Spec.Reason)); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "\nSuppressed overlaps: %d\nVerdict: %s\n", report.Suppressed, report.Verdict)
	return err
}

// WriteArtifacts persists all machine and human outputs for manual PR attachment.
func WriteArtifacts(directory string, report Report, state IncrementalState) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("review: create artifact directory: %w", err)
	}
	manifest, err := jsonPayload(report.Manifest)
	if err != nil {
		return err
	}
	findings, err := jsonPayload(report.Findings)
	if err != nil {
		return err
	}
	statePayload, err := jsonPayload(state)
	if err != nil {
		return err
	}
	var printed bytes.Buffer
	if err := PrintReport(&printed, report); err != nil {
		return err
	}
	artifacts := []struct {
		name    string
		payload []byte
	}{
		{"manifest.json", manifest},
		{"findings.json", findings},
		{"review.md", printed.Bytes()},
		{"state.json", statePayload},
	}
	for _, artifact := range artifacts {
		if err := writeArtifact(directory, artifact.name, artifact.payload); err != nil {
			return fmt.Errorf("review: write %s: %w", artifact.name, err)
		}
	}
	for _, cohort := range report.Cohorts {
		if cohort.Covered || len(cohort.Attempts) == 0 {
			continue
		}
		last := cohort.Attempts[len(cohort.Attempts)-1].Result
		payload := []byte("stdout:\n" + reviewOutputTail(last.Stdout) + "\nstderr:\n" + reviewOutputTail(last.Stderr) + "\n")
		name := fmt.Sprintf("cohort-%d.tail.txt", cohort.Cohort+1)
		if err := writeArtifact(directory, name, payload); err != nil {
			return fmt.Errorf("review: write %s: %w", name, err)
		}
	}
	return nil
}

var (
	reviewSecretLine   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*=`)
	reviewAbsolutePath = regexp.MustCompile(`(?:[A-Za-z]:)?(?:/|\\)[^\s"'=]+`)
)

func reviewOutputTail(payload []byte) string {
	lines := strings.Split(executor.Tail(payload, 40), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if reviewSecretLine.MatchString(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	redacted := strings.Join(kept, "\n")
	redacted = reviewAbsolutePath.ReplaceAllStringFunc(redacted, func(match string) string {
		cleaned := strings.TrimRight(match, ".,;:)")
		return filepath.Base(cleaned) + match[len(cleaned):]
	})
	if len(redacted) <= 4096 {
		return redacted
	}
	cut := redacted[len(redacted)-4096:]
	for len(cut) > 0 && cut[0]&0xc0 == 0x80 {
		cut = cut[1:]
	}
	if redacted[len(redacted)-len(cut)-1] != '\n' {
		if index := strings.IndexByte(cut, '\n'); index >= 0 {
			cut = cut[index+1:]
		}
	}
	return cut
}

func writeArtifact(directory, name string, payload []byte) error {
	temporary, err := os.CreateTemp(directory, "."+name+"-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, filepath.Join(directory, name))
}

func jsonPayload(value any) ([]byte, error) {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("review: encode artifact: %w", err)
	}
	return append(payload, '\n'), nil
}

func cleanReportText(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

func markdownCell(value string) string {
	return strings.ReplaceAll(cleanReportText(value), "|", "\\|")
}

// ArtifactPaths lists every destination before publication can create anything.
func ArtifactPaths(directory string) []string {
	var paths []string
	for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
		paths = append(paths, filepath.Join(directory, name))
	}
	return paths
}

// CheckArtifactPaths resolves directory symlinks and refuses tracked destinations
// or parents, including a tracked symlink at the original destination.
func CheckArtifactPaths(root string, paths []string) ([]string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	var resolved []string
	for _, filename := range paths {
		absolute, err := filepath.Abs(filename)
		if err != nil {
			return nil, err
		}
		canonical, err := resolveArtifactPath(absolute)
		if err != nil {
			return nil, fmt.Errorf("review: resolve artifact destination: %w", err)
		}
		for _, destination := range []string{absolute, canonical} {
			for current := destination; current != filepath.Dir(current); current = filepath.Dir(current) {
				relative, err := filepath.Rel(root, current)
				if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					break
				}
				if relative == "." {
					break
				}
				entries, err := gitOutput(root, "ls-files", "--stage", "-z", "--", filepath.ToSlash(relative))
				if err != nil {
					return nil, fmt.Errorf("review: check tracked artifact destination: %w", err)
				}
				for record := range bytes.SplitSeq(entries, []byte{0}) {
					_, name, ok := bytes.Cut(record, []byte{'\t'})
					if ok && (string(name) == filepath.ToSlash(relative) || current == destination) {
						// Exact index entries include gitlinks, even when their
						// working-tree representation is a directory.
						return nil, fmt.Errorf("review: artifact destination overlaps tracked path %q", relative)
					}
				}
			}
		}
		resolved = append(resolved, canonical)
	}
	return resolved, nil
}

func resolveArtifactPath(filename string) (string, error) {
	if _, err := os.Lstat(filename); err == nil {
		return filepath.EvalSymlinks(filename)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := resolveArtifactPath(filepath.Dir(filename))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(filename)), nil
}

func escapeReportPath(value string) string {
	var escaped strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			quoted := strconv.QuoteRune(r)
			escaped.WriteString(quoted[1 : len(quoted)-1])
		} else {
			escaped.WriteRune(r)
		}
	}
	return escaped.String()
}
