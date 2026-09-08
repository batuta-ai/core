package review

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportPrintsWalkthroughAndOrdersFindings(t *testing.T) {
	manifest := Manifest{
		Base: "0123456789abcdef",
		Files: []File{
			{Path: "a.go", Selected: true, Added: 3, Deleted: 1},
			{Path: "b.go", Selected: true, Added: 2},
		},
		Cohorts: []Cohort{{Files: []string{"a.go"}}, {Files: []string{"b.go"}}},
	}
	minor := Finding{Severity: Minor, Kind: Advisory, File: "a.go", Line: 3, Premise: "The name is vague", Path: "Rename it", Fix: "Use a precise name"}
	blocker := Finding{Severity: Blocker, Kind: Defect, File: "b.go", Line: 7, Premise: "The build fails", Path: "Compile the package", Verdict: "Compilation stops", Fix: "Restore the symbol"}
	report := BuildReport(manifest, []CohortResult{
		{Cohort: 0, Files: []string{"a.go"}, Covered: true, Findings: []Finding{minor}, Suppressed: []SuppressedFinding{{}}},
		{Cohort: 1, Files: []string{"b.go"}, Covered: true, Findings: []Finding{blocker}},
	}, &SpecSweep{Covered: true, Results: []SpecResult{{ID: "task-1.1", Status: CriterionViolated, Path: "b.go:7"}}})

	var out bytes.Buffer
	if err := PrintReport(&out, report); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Files: 2 selected of 2 changed (+5 -1)",
		"Cohorts: 2",
		"Coverage: 2/2 cohorts",
		"blocker · b.go:7 · The build fails · Restore the symbol",
		"minor · a.go:3 · The name is vague · Use a precise name",
		"| task-1.1 | violated | b.go:7 |",
		"Suppressed overlaps: 1",
		"Verdict: REWORK",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report is missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "blocker ·") > strings.Index(text, "minor ·") {
		t.Fatalf("findings are not most severe first:\n%s", text)
	}
}

func TestWriteArtifactsPersistsThePrintedReportVerbatim(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	report := BuildReport(Manifest{Base: "base"}, nil, nil)
	var printed bytes.Buffer
	if err := PrintReport(&printed, report); err != nil {
		t.Fatal(err)
	}
	if err := WriteArtifacts(out, report, IncrementalState{Head: "0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(out, "review.md")); err != nil || !bytes.Equal(got, printed.Bytes()) {
		t.Fatalf("review.md = %q, %v; want printed report %q", got, err, printed.Bytes())
	}
	var findings []Finding
	if payload, err := os.ReadFile(filepath.Join(out, "findings.json")); err != nil || json.Unmarshal(payload, &findings) != nil {
		t.Fatalf("findings.json is invalid: %v", err)
	}
}

func TestUncoveredReportCannotShip(t *testing.T) {
	report := BuildReport(Manifest{Cohorts: []Cohort{{Files: []string{"a.go"}}}}, []CohortResult{{Cohort: 0, Covered: false, Reason: "reviewer failed"}}, nil)
	if report.Verdict != Rework {
		t.Fatalf("verdict = %s, want REWORK", report.Verdict)
	}
}
