package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/classify"
	"github.com/batuta-ai/core/routing"
)

func writeCalibrateRun(t *testing.T, records []benchV3Record, trailingSummary bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if trailingSummary {
		if err := encoder.Encode(map[string]any{"summary": map[string]any{"tasks": len(records)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// calibrateDiscriminatingFixture is a four-task calibrate run whose lane C
// depends on FLow alone: FHigh (4-8) never applies, since every Scope's
// files count is either 0, 2 or 10 files (well below the lowest FHigh or at
// or above the highest); DocsLow never applies, since no task is docs-only.
// FLow=1 keeps task c at medium (3 lanes, discriminates); FLow=2 drops it to
// low, merging with task d (2 lanes, fails discrimination).
func calibrateDiscriminatingFixture() []benchV3Record {
	return []benchV3Record{
		{Task: "a", Split: "calibrate", PlanLane: "high", Outcome: benchOutcomeCandidate, Scope: classify.ScopeFeatures{Files: 10, Directories: 1}},
		{Task: "b", Split: "calibrate", PlanLane: "medium", Outcome: benchOutcomeEscalated, Scope: classify.ScopeFeatures{Files: 10, Directories: 1}},
		{Task: "c", Split: "calibrate", PlanLane: "low", Outcome: benchOutcomeCandidate, Scope: classify.ScopeFeatures{Files: 2, Directories: 1}},
		{Task: "d", Split: "calibrate", PlanLane: "low", Outcome: benchOutcomeCandidate, Scope: classify.ScopeFeatures{Files: 0, Directories: 1}},
	}
}

func TestClassifyCalibrateOffline(t *testing.T) {
	t.Parallel()
	path := writeCalibrateRun(t, calibrateDiscriminatingFixture(), true)
	var stdout, stderr strings.Builder
	if err := run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr); err != nil {
		t.Fatalf("calibrate: %v (stderr %s)", err, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "records=4") {
		t.Fatalf("expected records=4 (summary object must be skipped), got %s", output)
	}
	if strings.Count(output, "grid f_high=") != 20 {
		t.Fatalf("expected 20 grid lines, got %s", output)
	}
	if !strings.Contains(output, "rule=") {
		t.Fatalf("expected a rule line, got %s", output)
	}
}

func TestClassifyCalibrateRefusesTest(t *testing.T) {
	t.Parallel()
	t.Run("mixed half", func(t *testing.T) {
		t.Parallel()
		records := calibrateDiscriminatingFixture()
		records[1].Split = "test"
		path := writeCalibrateRun(t, records, false)
		var stdout, stderr strings.Builder
		if err := run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr); err == nil {
			t.Fatal("expected an error for a mixed-half run")
		}
	})
	t.Run("no v3 record", func(t *testing.T) {
		t.Parallel()
		path := writeCalibrateRun(t, nil, true)
		var stdout, stderr strings.Builder
		if err := run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr); err == nil {
			t.Fatal("expected an error for a run with no v3 record")
		}
	})
}

func TestClassifyCalibrateGrid(t *testing.T) {
	t.Parallel()
	records := []benchV3Record{
		{Task: "r1", Split: "calibrate", PlanLane: "low", Outcome: benchOutcomeCandidate, Scope: classify.ScopeFeatures{Files: 1, Directories: 1}},
		{Task: "r2", Split: "calibrate", PlanLane: "medium", Outcome: benchOutcomeCandidate, Scope: classify.ScopeFeatures{Files: 3, Directories: 1}},
		{Task: "r3", Split: "calibrate", PlanLane: "high", Outcome: benchOutcomeEscalated, Scope: classify.ScopeFeatures{Files: 6, Directories: 1}},
		{Task: "r4", Split: "calibrate", PlanLane: "low", Outcome: benchOutcomeFailed, Scope: classify.ScopeFeatures{Files: 1, Directories: 1, DocsOnly: true}},
	}
	selection := selectCalibrateC(records)
	if len(selection.Grid) != 20 {
		t.Fatalf("grid points = %d, want 20", len(selection.Grid))
	}
	var found *calibrateGridPoint
	for i := range selection.Grid {
		point := selection.Grid[i]
		if point.FHigh == 6 && point.FLow == 2 && !point.DocsLow {
			found = &selection.Grid[i]
		}
	}
	if found == nil {
		t.Fatal("grid point FHigh=6 FLow=2 DocsLow=false not found")
	}
	if found.Economy != 1.0 || found.Safety != 0.5 || found.LargestLane != "medium" ||
		found.LargestShare != 0.5 || found.Lanes != 3 || found.Balance != 0.75 || !found.Discriminates {
		t.Fatalf("grid point = %+v", found)
	}
}

func TestClassifyCalibrateSelectC(t *testing.T) {
	t.Parallel()
	t.Run("discriminates and ties break on economy, FHigh, FLow, docs_low", func(t *testing.T) {
		t.Parallel()
		selection := selectCalibrateC(calibrateDiscriminatingFixture())
		if !selection.Discriminated {
			t.Fatalf("expected a discriminating point, got %+v", selection)
		}
		// economy is computed through a runtime float64 variable, not an
		// untyped constant expression: Go folds constants at arbitrary
		// precision, so "(2.0/3.0 + 1.0) / 2" rounds once and differs in
		// its last bit from the runtime double-rounded value the bench's
		// own summary() produces (round 2/3 to float64, then add, then
		// halve) — the value this test must match.
		economy := 2.0 / 3.0
		want := calibrateGridPoint{FHigh: 4, FLow: 1, DocsLow: false, Economy: economy, Safety: 1.0, LargestLane: "high", LargestShare: 0.5, Lanes: 3, Balance: (economy + 1.0) / 2, Discriminates: true}
		if selection.Selected != want {
			t.Fatalf("selected = %+v, want %+v", selection.Selected, want)
		}
	})
	t.Run("no point discriminates: highest balance taken anyway", func(t *testing.T) {
		t.Parallel()
		records := []benchV3Record{
			{Task: "e", Split: "calibrate", PlanLane: "critical", Outcome: benchOutcomeCandidate, OpenMarker: true},
			{Task: "f", Split: "calibrate", PlanLane: "low", Outcome: benchOutcomeFailed, OpenMarker: true},
		}
		selection := selectCalibrateC(records)
		if selection.Discriminated {
			t.Fatalf("expected no point to discriminate, got %+v", selection)
		}
		want := calibrateGridPoint{FHigh: 4, FLow: 1, DocsLow: false, Economy: 1.0, Safety: 1.0, LargestLane: "critical", LargestShare: 1.0, Lanes: 1, Balance: 1.0, Discriminates: false}
		if selection.Selected != want {
			t.Fatalf("selected = %+v, want %+v", selection.Selected, want)
		}
	})
	t.Run("tie-break order", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			a, b calibrateGridPoint
		}{
			{"higher balance wins", calibrateGridPoint{Balance: 0.8}, calibrateGridPoint{Balance: 0.6}},
			{"balance tie, higher economy wins", calibrateGridPoint{Balance: 0.8, Economy: 0.9}, calibrateGridPoint{Balance: 0.8, Economy: 0.7}},
			{"balance and economy tie, lower FHigh wins", calibrateGridPoint{Balance: 0.8, Economy: 0.8, FHigh: 4}, calibrateGridPoint{Balance: 0.8, Economy: 0.8, FHigh: 6}},
			{"through FHigh tie, lower FLow wins", calibrateGridPoint{Balance: 0.8, Economy: 0.8, FHigh: 4, FLow: 1}, calibrateGridPoint{Balance: 0.8, Economy: 0.8, FHigh: 4, FLow: 2}},
			{"through FLow tie, docs_low false wins", calibrateGridPoint{Balance: 0.8, Economy: 0.8, FHigh: 4, FLow: 1, DocsLow: false}, calibrateGridPoint{Balance: 0.8, Economy: 0.8, FHigh: 4, FLow: 1, DocsLow: true}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if !betterCalibrateC(tc.a, tc.b) {
					t.Fatalf("%+v should beat %+v", tc.a, tc.b)
				}
				if betterCalibrateC(tc.b, tc.a) {
					t.Fatalf("%+v should not beat %+v", tc.b, tc.a)
				}
			})
		}
	})
}

func TestClassifyCalibrateSelectT(t *testing.T) {
	t.Parallel()
	t.Run("recomputes J per threshold from the recorded answers", func(t *testing.T) {
		t.Parallel()
		records := []benchV3Record{
			{
				Task: "r1", Split: "calibrate", PlanLane: "medium", Outcome: benchOutcomeCandidate,
				Packets: map[string]benchV3Packet{"contract": {Found: true, Size: 1}},
				Answers: map[string]benchV3Answer{"contract": {Choice: "exported_change", Confidence: 0.75, Status: "firm"}},
			},
			{
				Task: "r2", Split: "calibrate", PlanLane: "low", Outcome: benchOutcomeFailed,
				Packets: map[string]benchV3Packet{"security": {Found: true, Size: 1}},
				Answers: map[string]benchV3Answer{"security": {Choice: "incidental", Confidence: 0.85, Status: "firm"}},
			},
		}
		codeLanes := []routing.Complexity{routing.ComplexityMedium, routing.ComplexityMedium}
		points := selectCalibrateT(records, codeLanes)
		if len(points) != 3 {
			t.Fatalf("threshold points = %d, want 3", len(points))
		}
		want := map[float64]calibrateThresholdPoint{
			0.7: {Threshold: 0.7, Economy: 0.0, Safety: 0.0, Balance: 0.0},
			0.8: {Threshold: 0.8, Economy: 1.0, Safety: 0.0, Balance: 0.5},
			0.9: {Threshold: 0.9, Economy: 1.0, Safety: 1.0, Balance: 1.0},
		}
		for _, point := range points {
			if point != want[point.Threshold] {
				t.Fatalf("threshold %.2f = %+v, want %+v", point.Threshold, point, want[point.Threshold])
			}
		}
		best := bestCalibrateT(points)
		if best.Threshold != 0.9 {
			t.Fatalf("best threshold = %.2f, want 0.9", best.Threshold)
		}
	})
	t.Run("ties break on the higher threshold", func(t *testing.T) {
		t.Parallel()
		points := []calibrateThresholdPoint{
			{Threshold: 0.7, Balance: 0.5},
			{Threshold: 0.8, Balance: 0.5},
			{Threshold: 0.9, Balance: 0.3},
		}
		if best := bestCalibrateT(points); best.Threshold != 0.8 {
			t.Fatalf("best threshold = %.2f, want 0.8", best.Threshold)
		}
	})
}

func TestClassifyCalibrateRuleRoundTrip(t *testing.T) {
	t.Parallel()
	path := writeCalibrateRun(t, calibrateDiscriminatingFixture(), false)
	outPath := filepath.Join(t.TempDir(), "rule.json")
	var stdout, stderr strings.Builder
	if err := run([]string{"judge", "classify", "calibrate", "--run", path, "--out", outPath}, &stdout, &stderr); err != nil {
		t.Fatalf("calibrate: %v (stderr %s)", err, stderr.String())
	}
	rule, err := loadBenchV3Rule(outPath)
	if err != nil {
		t.Fatalf("bench --rule rejected the written rule: %v", err)
	}
	want := classify.RuleV3{FHigh: 4, FLow: 1, DocsLow: false, Threshold: 0.9}
	if rule != want {
		t.Fatalf("rule = %+v, want %+v", rule, want)
	}
}

// TestClassifyCalibrateNoJudgeCall pins that the calibrate form takes no
// judge or network flags at all: --run, --out and --json are all it parses,
// so it has nothing to build a judge from and makes no call.
func TestClassifyCalibrateNoJudgeCall(t *testing.T) {
	t.Parallel()
	path := writeCalibrateRun(t, calibrateDiscriminatingFixture(), false)
	var stdout, stderr strings.Builder
	err := run([]string{"judge", "classify", "calibrate", "--run", path, "--base-url", "http://127.0.0.1:1"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "base-url") {
		t.Fatalf("expected an undefined-flag error naming base-url, got %v", err)
	}
}
