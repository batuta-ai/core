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

func TestReportMarksOversizedCohort(t *testing.T) {
	manifest := Manifest{
		Base:  "0123456789abcdef",
		Files: []File{{Path: "loop/loop_test.go", Selected: true, Added: 1443}},
		Cohorts: []Cohort{{
			Files:        []string{"loop/loop_test.go"},
			ChangedLines: 1443,
			Oversized:    true,
		}},
	}
	report := BuildReport(manifest, []CohortResult{{Cohort: 0, Files: []string{"loop/loop_test.go"}, Covered: true}}, nil)
	var out bytes.Buffer
	if err := PrintReport(&out, report); err != nil {
		t.Fatal(err)
	}
	if want := "Cohort 1: loop/loop_test.go (1443 changed lines, oversized)"; !strings.Contains(out.String(), want) {
		t.Fatalf("report is missing %q:\n%s", want, out.String())
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

func TestReportEscapesControlCharacters(t *testing.T) {
	name := "bad\x1b[2J\n\t\r\x7f\u009b.go"
	report := BuildReport(Manifest{Files: []File{{Path: name, OldPath: name, Selected: true}}, Cohorts: []Cohort{{Files: []string{name}}}},
		[]CohortResult{{Files: []string{name}, Covered: true, Findings: []Finding{{File: name, Line: 1, Severity: Major}}}},
		&SpecSweep{Covered: true, Results: []SpecResult{{ID: "task-1.1", Status: CriterionSatisfied, Path: name + ":1"}}})
	var out bytes.Buffer
	if err := PrintReport(&out, report); err != nil {
		t.Fatal(err)
	}
	escaped := `bad\x1b[2J\n\t\r\x7f\u009b.go`
	if strings.Count(out.String(), escaped) != 3 || strings.ContainsAny(out.String(), "\x1b\r\t\x7f\u009b") {
		t.Fatalf("unsafe or ambiguous report: %q", out.String())
	}
	dir := t.TempDir()
	if err := WriteArtifacts(dir, report, IncrementalState{}); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Files[0].Path != name || manifest.Files[0].OldPath != name || manifest.Cohorts[0].Files[0] != name {
		t.Fatal("manifest paths changed")
	}
	payload, err = os.ReadFile(filepath.Join(dir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var findings []Finding
	if err := json.Unmarshal(payload, &findings); err != nil {
		t.Fatal(err)
	}
	if findings[0].File != name {
		t.Fatal("finding path changed")
	}
}

func TestArtifactsRefuseGitlinkDestination(t *testing.T) {
	root := reviewRepo(t)
	source := reviewRepo(t)
	for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
		writeTestFile(t, source, "reports/"+name, "tracked artifact\n")
	}
	gitTest(t, source, "add", ".")
	gitTest(t, source, "commit", "-qm", "artifacts")
	gitTest(t, root, "-c", "protocol.file.allow=always", "submodule", "add", source, "module")
	gitTest(t, root, "commit", "-qm", "submodule")
	for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
		writeTestFile(t, root, "module/reports/"+name, "local edits\n")
	}
	for _, directory := range []string{"module/reports", "module/new-reports"} {
		out := filepath.Join(root, directory)
		_, err := CheckArtifactPaths(root, ArtifactPaths(out))
		if err == nil {
			t.Errorf("accepted gitlink destination %s", directory)
		}
	}
	for _, filename := range ArtifactPaths(filepath.Join(root, "module/reports")) {
		payload, err := os.ReadFile(filename)
		if err != nil || string(payload) != "local edits\n" {
			t.Fatalf("artifact changed: %q, %v", payload, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "module/new-reports")); !os.IsNotExist(err) {
		t.Fatalf("created destination: %v", err)
	}
}
