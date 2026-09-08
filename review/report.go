package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
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
		covered = covered && cohort.Covered
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
		}
		if _, err := fmt.Fprintf(w, "- Cohort %d: %s — %s\n", cohort.Cohort+1, strings.Join(cohort.Files, ", "), status); err != nil {
			return err
		}
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
		if _, err := fmt.Fprintf(w, "%s · %s:%d · %s · %s\n", finding.Severity, finding.File, finding.Line, cleanReportText(finding.Premise), cleanReportText(finding.Fix)); err != nil {
			return err
		}
	}
	if report.Spec != nil {
		if _, err := fmt.Fprintln(w, "\nCriteria:\n| Criterion | Status | Evidence |\n|---|---|---|"); err != nil {
			return err
		}
		for _, result := range report.Spec.Results {
			if _, err := fmt.Fprintf(w, "| %s | %s | %s |\n", markdownCell(result.ID), result.Status, markdownCell(result.Path)); err != nil {
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
func WriteArtifacts(directory string, report Report, state ReviewState) error {
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
	return nil
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
