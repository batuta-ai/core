package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/batuta-ai/core/classify"
	"github.com/batuta-ai/core/routing"
)

func writeCalibrateRun(t *testing.T, records []benchV3Record, trailingSummary bool) string {
	t.Helper()
	tasks := len(records)
	if !trailingSummary {
		return writeCalibrateRunWithSummary(t, records, nil)
	}
	return writeCalibrateRunWithSummary(t, records, &tasks)
}

// writeCalibrateRunWithSummary writes a calibrate run file with an explicit
// summary tasks count, or none at all when summaryTasks is nil, so a test
// can exercise a summary that disagrees with the number of task records.
func writeCalibrateRunWithSummary(t *testing.T, records []benchV3Record, summaryTasks *int) string {
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
	if summaryTasks != nil {
		if err := encoder.Encode(map[string]any{"summary": map[string]any{"tasks": *summaryTasks}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeRawCalibrateRun writes each given raw JSON line verbatim, letting a
// test omit a field readCalibrateRecords must reject rather than shaping
// the line through benchV3Record's own json tags.
func writeRawCalibrateRun(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.json")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// calibrateRecordMap is a complete raw task record: every field
// readCalibrateRecords requires is present, so a test can delete exactly
// one to prove its absence is rejected.
func calibrateRecordMap() map[string]any {
	return map[string]any{
		"task":        "a",
		"split":       "calibrate",
		"plan_lane":   "low",
		"code_lane":   "low",
		"judge_lane":  "low",
		"outcome":     benchOutcomeCandidate,
		"open_marker": false,
		"scope":       map[string]any{"Files": 1, "Directories": 1, "TestOnly": false, "DocsOnly": false},
		"packets": map[string]any{
			"contract": map[string]any{"found": false, "size": 0},
			"security": map[string]any{"found": false, "size": 0},
		},
		"answers": map[string]any{
			"contract": map[string]any{"status": "not_asked", "confidence": 0},
			"security": map[string]any{"status": "not_asked", "confidence": 0},
		},
	}
}

// calibrateAnsweredRecordMap is a complete raw task record whose contract
// question was asked and answered: security stays not_asked, matching what
// a real bench run leaves for a question with no packet.
func calibrateAnsweredRecordMap(status, choice string, confidence float64) map[string]any {
	record := calibrateRecordMap()
	record["packets"] = map[string]any{
		"contract": map[string]any{"found": true, "size": 1},
		"security": map[string]any{"found": false, "size": 0},
	}
	answer := map[string]any{"status": status, "confidence": confidence}
	if choice != "" {
		answer["choice"] = choice
	}
	record["answers"] = map[string]any{
		"contract": answer,
		"security": map[string]any{"status": "not_asked", "confidence": 0},
	}
	return record
}

// calibrateUnansweredRecordMap is a complete raw task record whose contract
// question carries status, with a packets found value that matches it the
// way the bench always pairs them: not_asked with found false, unavailable
// with found true.
func calibrateUnansweredRecordMap(status string) map[string]any {
	record := calibrateRecordMap()
	found := status != "not_asked"
	size := 0
	if found {
		size = 1
	}
	record["packets"] = map[string]any{
		"contract": map[string]any{"found": found, "size": size},
		"security": map[string]any{"found": false, "size": 0},
	}
	record["answers"] = map[string]any{
		"contract": map[string]any{"status": status, "confidence": 0},
		"security": map[string]any{"status": "not_asked", "confidence": 0},
	}
	return record
}

// calibrateNotAskedAnswers is the packets/answers pair a real bench run
// writes for a task where neither question got a packet: both questions
// present, "not_asked", exactly as benchV3RecordFor leaves them.
func calibrateNotAskedAnswers() (map[string]benchV3Packet, map[string]benchV3Answer) {
	return map[string]benchV3Packet{"contract": {}, "security": {}},
		map[string]benchV3Answer{"contract": {Status: "not_asked"}, "security": {Status: "not_asked"}}
}

// calibrateDiscriminatingFixture is a four-task calibrate run whose lane C
// depends on FLow alone: FHigh (4-8) never applies, since every Scope's
// files count is either 0, 2 or 10 files (well below the lowest FHigh or at
// or above the highest); DocsLow never applies, since no task is docs-only.
// FLow=1 keeps task c at medium (3 lanes, discriminates); FLow=2 drops it to
// low, merging with task d (2 lanes, fails discrimination).
func calibrateDiscriminatingFixture() []benchV3Record {
	packets, answers := calibrateNotAskedAnswers()
	return []benchV3Record{
		{Task: "a", Split: "calibrate", PlanLane: "high", CodeLane: "high", JudgeLane: "high", Outcome: benchOutcomeCandidate, Scope: classify.ScopeFeatures{Files: 10, Directories: 1}, Packets: packets, Answers: answers},
		{Task: "b", Split: "calibrate", PlanLane: "medium", CodeLane: "medium", JudgeLane: "medium", Outcome: benchOutcomeEscalated, Scope: classify.ScopeFeatures{Files: 10, Directories: 1}, Packets: packets, Answers: answers},
		{Task: "c", Split: "calibrate", PlanLane: "low", CodeLane: "low", JudgeLane: "low", Outcome: benchOutcomeCandidate, Scope: classify.ScopeFeatures{Files: 2, Directories: 1}, Packets: packets, Answers: answers},
		{Task: "d", Split: "calibrate", PlanLane: "low", CodeLane: "low", JudgeLane: "low", Outcome: benchOutcomeCandidate, Scope: classify.ScopeFeatures{Files: 0, Directories: 1}, Packets: packets, Answers: answers},
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
		path := writeCalibrateRun(t, records, true)
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
	path := writeCalibrateRun(t, calibrateDiscriminatingFixture(), true)
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

// TestClassifyCalibrateRejectsMalformedRecord proves that a task record
// missing any field the grid and threshold search read is rejected by name
// and by line, instead of silently reading as a zero value.
func TestClassifyCalibrateRejectsMalformedRecord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		remove func(map[string]any)
	}{
		{"split", func(m map[string]any) { delete(m, "split") }},
		{"plan_lane", func(m map[string]any) { delete(m, "plan_lane") }},
		{"code_lane", func(m map[string]any) { delete(m, "code_lane") }},
		{"judge_lane", func(m map[string]any) { delete(m, "judge_lane") }},
		{"outcome", func(m map[string]any) { delete(m, "outcome") }},
		{"scope", func(m map[string]any) { delete(m, "scope") }},
		{"packets", func(m map[string]any) { delete(m, "packets") }},
		{"answers", func(m map[string]any) { delete(m, "answers") }},
		{"packets contract entry", func(m map[string]any) {
			delete(m["packets"].(map[string]any), "contract")
		}},
		{"answers security entry", func(m map[string]any) {
			delete(m["answers"].(map[string]any), "security")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := calibrateRecordMap()
			tc.remove(record)
			recordJSON, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			summaryJSON, err := json.Marshal(map[string]any{"summary": map[string]any{"tasks": 1}})
			if err != nil {
				t.Fatal(err)
			}
			path := writeRawCalibrateRun(t, []string{string(recordJSON), string(summaryJSON)})
			var stdout, stderr strings.Builder
			err = run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr)
			if err == nil {
				t.Fatalf("expected an error for a record missing %s", tc.name)
			}
			if !strings.Contains(err.Error(), "line 1") {
				t.Fatalf("expected the error to name line 1, got %v", err)
			}
		})
	}
}

// TestClassifyCalibrateSummaryMustBeLast proves that the trailing summary
// object must be the file's last non-blank line: a task record after it, a
// second summary, and a JSON object that is neither a task record nor the
// summary are all rejected by line number.
func TestClassifyCalibrateSummaryMustBeLast(t *testing.T) {
	t.Parallel()
	recordJSON, err := json.Marshal(calibrateRecordMap())
	if err != nil {
		t.Fatal(err)
	}
	summaryLine := func(tasks int) string {
		b, err := json.Marshal(map[string]any{"summary": map[string]any{"tasks": tasks}})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	t.Run("summary followed by a task record", func(t *testing.T) {
		t.Parallel()
		path := writeRawCalibrateRun(t, []string{summaryLine(1), string(recordJSON)})
		var stdout, stderr strings.Builder
		err := run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected an error for a summary followed by a task record")
		}
		if !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("expected the error to name line 2, got %v", err)
		}
	})
	t.Run("two summaries", func(t *testing.T) {
		t.Parallel()
		path := writeRawCalibrateRun(t, []string{summaryLine(0), summaryLine(0)})
		var stdout, stderr strings.Builder
		err := run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected an error for a run with two summaries")
		}
		if !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("expected the error to name line 2, got %v", err)
		}
	})
	t.Run("neither a task record nor the summary", func(t *testing.T) {
		t.Parallel()
		path := writeRawCalibrateRun(t, []string{`{"foo":"bar"}`, summaryLine(0)})
		var stdout, stderr strings.Builder
		err := run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected an error for a JSON object that is neither a task record nor the summary")
		}
		if !strings.Contains(err.Error(), "line 1") {
			t.Fatalf("expected the error to name line 1, got %v", err)
		}
	})
}

// TestClassifyCalibrateRejectsNullOrMistyped proves that a task record whose
// value for a required field is null, mistyped, or otherwise ill-shaped is
// rejected by line and by field name, instead of silently decoding into a
// zero value.
func TestClassifyCalibrateRejectsNullOrMistyped(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		field  string
		mutate func(map[string]any)
	}{
		{"split null", "split", func(m map[string]any) { m["split"] = nil }},
		{"split not a string", "split", func(m map[string]any) { m["split"] = 5 }},
		{"split empty", "split", func(m map[string]any) { m["split"] = "" }},
		{"plan_lane null", "plan_lane", func(m map[string]any) { m["plan_lane"] = nil }},
		{"plan_lane not a string", "plan_lane", func(m map[string]any) { m["plan_lane"] = 5 }},
		{"plan_lane empty", "plan_lane", func(m map[string]any) { m["plan_lane"] = "" }},
		{"code_lane null", "code_lane", func(m map[string]any) { m["code_lane"] = nil }},
		{"code_lane not a string", "code_lane", func(m map[string]any) { m["code_lane"] = 5 }},
		{"code_lane empty", "code_lane", func(m map[string]any) { m["code_lane"] = "" }},
		{"judge_lane null", "judge_lane", func(m map[string]any) { m["judge_lane"] = nil }},
		{"judge_lane not a string", "judge_lane", func(m map[string]any) { m["judge_lane"] = 5 }},
		{"judge_lane empty", "judge_lane", func(m map[string]any) { m["judge_lane"] = "" }},
		{"outcome null", "outcome", func(m map[string]any) { m["outcome"] = nil }},
		{"outcome not a string", "outcome", func(m map[string]any) { m["outcome"] = 5 }},
		{"outcome empty", "outcome", func(m map[string]any) { m["outcome"] = "" }},
		{"scope null", "scope", func(m map[string]any) { m["scope"] = nil }},
		{"scope not an object", "scope", func(m map[string]any) { m["scope"] = "nope" }},
		{"scope Files not numeric", "scope", func(m map[string]any) {
			m["scope"] = map[string]any{"Files": "x", "Directories": 1, "TestOnly": false, "DocsOnly": false}
		}},
		{"scope Directories null", "scope", func(m map[string]any) {
			m["scope"] = map[string]any{"Files": 1, "Directories": nil, "TestOnly": false, "DocsOnly": false}
		}},
		{"scope TestOnly not boolean", "scope", func(m map[string]any) {
			m["scope"] = map[string]any{"Files": 1, "Directories": 1, "TestOnly": "no", "DocsOnly": false}
		}},
		{"scope DocsOnly null", "scope", func(m map[string]any) {
			m["scope"] = map[string]any{"Files": 1, "Directories": 1, "TestOnly": false, "DocsOnly": nil}
		}},
		{"packets entry null", "packets", func(m map[string]any) {
			m["packets"] = map[string]any{"contract": nil, "security": map[string]any{"found": false}}
		}},
		{"packets entry lacks found", "packets", func(m map[string]any) {
			m["packets"] = map[string]any{"contract": map[string]any{"size": 0}, "security": map[string]any{"found": false}}
		}},
		{"answers entry null", "answers", func(m map[string]any) {
			m["answers"] = map[string]any{"contract": nil, "security": map[string]any{"status": "not_asked"}}
		}},
		{"answers entry lacks status", "answers", func(m map[string]any) {
			m["answers"] = map[string]any{"contract": map[string]any{"confidence": 0}, "security": map[string]any{"status": "not_asked"}}
		}},
		{"answers entry status not one of the valid values", "answers", func(m map[string]any) {
			m["answers"] = map[string]any{"contract": map[string]any{"status": "bogus"}, "security": map[string]any{"status": "not_asked"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := calibrateRecordMap()
			tc.mutate(record)
			recordJSON, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			summaryJSON, err := json.Marshal(map[string]any{"summary": map[string]any{"tasks": 1}})
			if err != nil {
				t.Fatal(err)
			}
			path := writeRawCalibrateRun(t, []string{string(recordJSON), string(summaryJSON)})
			var stdout, stderr strings.Builder
			err = run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr)
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), "line 1") {
				t.Fatalf("expected the error to name line 1, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("expected the error to name field %q, got %v", tc.field, err)
			}
		})
	}
}

// TestClassifyCalibrateRequiresSummary proves that a run file is rejected
// when it lacks the trailing summary object, or when the summary's tasks
// count disagrees with the number of task records actually read.
func TestClassifyCalibrateRequiresSummary(t *testing.T) {
	t.Parallel()
	t.Run("no trailing summary", func(t *testing.T) {
		t.Parallel()
		path := writeCalibrateRun(t, calibrateDiscriminatingFixture(), false)
		var stdout, stderr strings.Builder
		if err := run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr); err == nil {
			t.Fatal("expected an error for a run file with no trailing summary")
		}
	})
	t.Run("summary tasks count mismatch", func(t *testing.T) {
		t.Parallel()
		mismatched := len(calibrateDiscriminatingFixture()) + 1
		path := writeCalibrateRunWithSummary(t, calibrateDiscriminatingFixture(), &mismatched)
		var stdout, stderr strings.Builder
		if err := run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr); err == nil {
			t.Fatal("expected an error for a summary tasks count that disagrees with the record count")
		}
	})
}

// calibrateRunSingleRecord writes one raw record and a matching trailing
// summary, then runs judge classify calibrate against it, returning the
// error so a test can assert on its message.
func calibrateRunSingleRecord(t *testing.T, record map[string]any) error {
	t.Helper()
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	summaryJSON, err := json.Marshal(map[string]any{"summary": map[string]any{"tasks": 1}})
	if err != nil {
		t.Fatal(err)
	}
	path := writeRawCalibrateRun(t, []string{string(recordJSON), string(summaryJSON)})
	var stdout, stderr strings.Builder
	return run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr)
}

// TestClassifyCalibrateOpenMarker proves that a record whose open_marker is
// missing, null or not a boolean is rejected by line and by field.
func TestClassifyCalibrateOpenMarker(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing", func(m map[string]any) { delete(m, "open_marker") }},
		{"null", func(m map[string]any) { m["open_marker"] = nil }},
		{"not a boolean", func(m map[string]any) { m["open_marker"] = "yes" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := calibrateRecordMap()
			tc.mutate(record)
			err := calibrateRunSingleRecord(t, record)
			if err == nil {
				t.Fatalf("expected an error for open_marker %s", tc.name)
			}
			if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "open_marker") {
				t.Fatalf("expected the error to name line 1 and open_marker, got %v", err)
			}
		})
	}
}

// TestClassifyCalibrateAllowedValues proves that split, plan_lane, code_lane,
// judge_lane and outcome are rejected when they hold a value outside their
// allowed set, by line and by field.
func TestClassifyCalibrateAllowedValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		field  string
		mutate func(map[string]any)
	}{
		{"split not calibrate or test", "split", func(m map[string]any) { m["split"] = "bogus" }},
		{"plan_lane not a lane", "plan_lane", func(m map[string]any) { m["plan_lane"] = "urgent" }},
		{"code_lane not a lane", "code_lane", func(m map[string]any) { m["code_lane"] = "urgent" }},
		{"judge_lane not a lane", "judge_lane", func(m map[string]any) { m["judge_lane"] = "urgent" }},
		{"outcome not an outcome", "outcome", func(m map[string]any) { m["outcome"] = "bogus" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := calibrateRecordMap()
			tc.mutate(record)
			err := calibrateRunSingleRecord(t, record)
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("expected the error to name line 1 and %s, got %v", tc.field, err)
			}
		})
	}
}

// TestClassifyCalibrateCounts proves that Scope's Files and Directories, and
// a packet's size, are rejected when negative or non-integer, and that a
// packet with found false and size above zero is rejected too.
func TestClassifyCalibrateCounts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		field  string
		mutate func(map[string]any)
	}{
		{"scope Files negative", "scope", func(m map[string]any) {
			m["scope"] = map[string]any{"Files": -1, "Directories": 1, "TestOnly": false, "DocsOnly": false}
		}},
		{"scope Files not an integer", "scope", func(m map[string]any) {
			m["scope"] = map[string]any{"Files": 1.5, "Directories": 1, "TestOnly": false, "DocsOnly": false}
		}},
		{"scope Directories negative", "scope", func(m map[string]any) {
			m["scope"] = map[string]any{"Files": 1, "Directories": -1, "TestOnly": false, "DocsOnly": false}
		}},
		{"scope Directories not an integer", "scope", func(m map[string]any) {
			m["scope"] = map[string]any{"Files": 1, "Directories": 2.5, "TestOnly": false, "DocsOnly": false}
		}},
		{"packet size missing", "packets", func(m map[string]any) {
			m["packets"] = map[string]any{"contract": map[string]any{"found": false}, "security": map[string]any{"found": false, "size": 0}}
		}},
		{"packet size null", "packets", func(m map[string]any) {
			m["packets"] = map[string]any{"contract": map[string]any{"found": false, "size": nil}, "security": map[string]any{"found": false, "size": 0}}
		}},
		{"packet size negative", "packets", func(m map[string]any) {
			m["packets"] = map[string]any{"contract": map[string]any{"found": false, "size": -1}, "security": map[string]any{"found": false, "size": 0}}
		}},
		{"packet size not an integer", "packets", func(m map[string]any) {
			m["packets"] = map[string]any{"contract": map[string]any{"found": false, "size": 1.5}, "security": map[string]any{"found": false, "size": 0}}
		}},
		{"packet found false with size above zero", "packets", func(m map[string]any) {
			m["packets"] = map[string]any{"contract": map[string]any{"found": false, "size": 3}, "security": map[string]any{"found": false, "size": 0}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := calibrateRecordMap()
			tc.mutate(record)
			err := calibrateRunSingleRecord(t, record)
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("expected the error to name line 1 and %s, got %v", tc.field, err)
			}
		})
	}
}

// TestClassifyCalibrateAnswerValues proves that, for a question actually
// asked and answered, choice, confidence and probabilities are each
// rejected when malformed, for every status that carries them.
func TestClassifyCalibrateAnswerValues(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"firm", "insufficient", "below_threshold"} {
		for _, tc := range []struct {
			name   string
			mutate func(map[string]any)
		}{
			{"choice missing", func(m map[string]any) {
				delete(m["answers"].(map[string]any)["contract"].(map[string]any), "choice")
			}},
			{"choice null", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["choice"] = nil
			}},
			{"choice empty", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["choice"] = ""
			}},
			{"choice not an option", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["choice"] = "bogus"
			}},
			{"confidence missing", func(m map[string]any) {
				delete(m["answers"].(map[string]any)["contract"].(map[string]any), "confidence")
			}},
			{"confidence null", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["confidence"] = nil
			}},
			{"confidence not a number", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["confidence"] = "high"
			}},
			{"confidence below zero", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["confidence"] = -0.1
			}},
			{"confidence above one", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["confidence"] = 1.1
			}},
			{"probabilities not an object", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["probabilities"] = "nope"
			}},
			{"probabilities value below zero", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["probabilities"] = map[string]any{"exported_change": -0.1}
			}},
			{"probabilities value above one", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["probabilities"] = map[string]any{"exported_change": 1.1}
			}},
			{"probabilities value not a number", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["probabilities"] = map[string]any{"exported_change": "high"}
			}},
		} {
			t.Run(status+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				record := calibrateAnsweredRecordMap(status, "exported_change", 0.95)
				tc.mutate(record)
				err := calibrateRunSingleRecord(t, record)
				if err == nil {
					t.Fatalf("expected an error for status %s, %s", status, tc.name)
				}
				if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "answers") {
					t.Fatalf("expected the error to name line 1 and answers, got %v", err)
				}
			})
		}
	}
}

// TestClassifyCalibrateUnansweredEntry proves that a not_asked or
// unavailable answer is rejected when its confidence is missing, null, not a
// number or not zero, and when it carries a choice or a probabilities key:
// the bench never writes either for these two statuses, and always writes
// confidence as the literal zero, so an entry that disagrees cannot have
// come from a real bench run.
func TestClassifyCalibrateUnansweredEntry(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"not_asked", "unavailable"} {
		for _, tc := range []struct {
			name   string
			mutate func(map[string]any)
		}{
			{"confidence missing", func(m map[string]any) {
				delete(m["answers"].(map[string]any)["contract"].(map[string]any), "confidence")
			}},
			{"confidence null", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["confidence"] = nil
			}},
			{"confidence not a number", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["confidence"] = "high"
			}},
			{"confidence not zero", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["confidence"] = 0.5
			}},
			{"choice present", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["choice"] = "exported_change"
			}},
			{"probabilities present", func(m map[string]any) {
				m["answers"].(map[string]any)["contract"].(map[string]any)["probabilities"] = map[string]any{"exported_change": 0.5}
			}},
		} {
			t.Run(status+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				record := calibrateUnansweredRecordMap(status)
				tc.mutate(record)
				err := calibrateRunSingleRecord(t, record)
				if err == nil {
					t.Fatalf("expected an error for status %s, %s", status, tc.name)
				}
				if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "answers") {
					t.Fatalf("expected the error to name line 1 and answers, got %v", err)
				}
			})
		}
	}
}

// TestClassifyCalibrateProbabilityValues proves that, for a question
// actually asked and answered, a value in its probabilities is rejected when
// null, not a number, or outside 0 to 1, and a key in its probabilities is
// rejected when it is not one of the question's three options.
func TestClassifyCalibrateProbabilityValues(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"firm", "insufficient", "below_threshold"} {
		choice := "exported_change"
		if status == "insufficient" {
			choice = "insufficient"
		}
		for _, tc := range []struct {
			name  string
			value any
		}{
			{"value null", nil},
			{"value not a number", "high"},
			{"value below zero", -0.1},
			{"value above one", 1.1},
		} {
			t.Run(status+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				record := calibrateAnsweredRecordMap(status, choice, 0.95)
				record["answers"].(map[string]any)["contract"].(map[string]any)["probabilities"] = map[string]any{"exported_change": tc.value}
				err := calibrateRunSingleRecord(t, record)
				if err == nil {
					t.Fatalf("expected an error for status %s, probabilities %s", status, tc.name)
				}
				if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "probabilities") {
					t.Fatalf("expected the error to name line 1 and probabilities, got %v", err)
				}
			})
		}
		t.Run(status+"/key not one of the question's options", func(t *testing.T) {
			t.Parallel()
			record := calibrateAnsweredRecordMap(status, choice, 0.95)
			record["answers"].(map[string]any)["contract"].(map[string]any)["probabilities"] = map[string]any{"bogus": 0.5}
			err := calibrateRunSingleRecord(t, record)
			if err == nil {
				t.Fatalf("expected an error for status %s, probabilities key not an option", status)
			}
			if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "probabilities") {
				t.Fatalf("expected the error to name line 1 and probabilities, got %v", err)
			}
		})
	}
}

// TestClassifyCalibrateStatusChoiceConsistency proves that an answer is
// rejected when its status is insufficient and its choice is not
// insufficient, or when its status is firm or below_threshold and its choice
// is insufficient: DecideV3 never produces the mismatched pairing, so a
// record that carries one cannot have come from a real bench run.
func TestClassifyCalibrateStatusChoiceConsistency(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status string
		choice string
	}{
		{"insufficient status, non-insufficient choice", "insufficient", "exported_change"},
		{"firm status, insufficient choice", "firm", "insufficient"},
		{"below_threshold status, insufficient choice", "below_threshold", "insufficient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := calibrateAnsweredRecordMap(tc.status, tc.choice, 0.95)
			err := calibrateRunSingleRecord(t, record)
			if err == nil {
				t.Fatalf("expected an error for status %s, choice %s", tc.status, tc.choice)
			}
			if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "answers") {
				t.Fatalf("expected the error to name line 1 and answers, got %v", err)
			}
		})
	}
}

// TestClassifyCalibratePacketAnswerConsistency proves that a packet's found
// and its question's answer status must agree: found false pairs only with
// not_asked, and found true pairs only with something other than not_asked.
func TestClassifyCalibratePacketAnswerConsistency(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"found false but status firm", func(m map[string]any) {
			m["packets"] = map[string]any{"contract": map[string]any{"found": false, "size": 0}, "security": map[string]any{"found": false, "size": 0}}
			m["answers"] = map[string]any{
				"contract": map[string]any{"status": "firm", "choice": "exported_change", "confidence": 0.95},
				"security": map[string]any{"status": "not_asked", "confidence": 0},
			}
		}},
		{"found true but status not_asked", func(m map[string]any) {
			m["packets"] = map[string]any{"contract": map[string]any{"found": true, "size": 1}, "security": map[string]any{"found": false, "size": 0}}
			m["answers"] = map[string]any{
				"contract": map[string]any{"status": "not_asked", "confidence": 0},
				"security": map[string]any{"status": "not_asked", "confidence": 0},
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := calibrateRecordMap()
			tc.mutate(record)
			err := calibrateRunSingleRecord(t, record)
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "answers") {
				t.Fatalf("expected the error to name line 1 and answers, got %v", err)
			}
		})
	}
}

// TestClassifyCalibrateSummaryTasks proves that the trailing summary's tasks
// field is rejected when missing, null, non-integer or negative.
func TestClassifyCalibrateSummaryTasks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		summary map[string]any
	}{
		{"missing", map[string]any{}},
		{"null", map[string]any{"tasks": nil}},
		{"not an integer", map[string]any{"tasks": 1.5}},
		{"negative", map[string]any{"tasks": -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recordJSON, err := json.Marshal(calibrateRecordMap())
			if err != nil {
				t.Fatal(err)
			}
			summaryJSON, err := json.Marshal(map[string]any{"summary": tc.summary})
			if err != nil {
				t.Fatal(err)
			}
			path := writeRawCalibrateRun(t, []string{string(recordJSON), string(summaryJSON)})
			var stdout, stderr strings.Builder
			err = run([]string{"judge", "classify", "calibrate", "--run", path}, &stdout, &stderr)
			if err == nil {
				t.Fatalf("expected an error for summary tasks %s", tc.name)
			}
			if !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "tasks") {
				t.Fatalf("expected the error to name line 2 and tasks, got %v", err)
			}
		})
	}
}

// calibrateBenchTestPlan is a five-task plan whose slug hashes to the
// calibrate half (classify.SplitV3("calibrate-fixture-a")): four tasks
// reference the same Go file, so their contract question is asked, and one
// references a doc path that is never created, so it never gets a packet.
const calibrateBenchTestPlan = `# Plan — calibrate statuses
<!-- inputs: profile.md@sha256:1a2b3c4d5e6f routing.md@sha256:0f0e0d0c0b0a -->

**Goal:** Exercise every answer status a v3 bench run can write.
**Created:** 2026-09-27 · **Status:** approved

## Tasks
- [ ] 1. Change firm — backend/low
      Scope: api/contract.go
      Accept: Go tests pass → go test ./...
- [ ] 2. Change insufficient — backend/low
      Scope: api/contract.go
      Accept: Go tests pass → go test ./...
- [ ] 3. Change below threshold — backend/low
      Scope: api/contract.go
      Accept: Go tests pass → go test ./...
- [ ] 4. Change unavailable — backend/low
      Scope: api/contract.go
      Accept: Go tests pass → go test ./...
- [ ] 5. Document behavior — docs/medium
      Scope: docs/readme.md
      Accept: documentation exists → test -f docs/readme.md

## Decisions and context
Tasks 1-4 change Exported.
`

// calibrateBenchFixture is calibrateBenchTestPlan on disk: one Go file for
// the four code tasks, and no docs/readme.md at all, so the fifth task's
// question is never asked, exactly as benchV3Fixture leaves its docs task.
func calibrateBenchFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".batuta", "judge.json"), []byte(`{"provider":"typesafe","model":"jev-test","key_env":"PATH"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "contract.go"), []byte("package api\nfunc Exported() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planDir := filepath.Join(root, ".batuta", "plans", "done")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(planDir, "calibrate-fixture-a.md")
	if err := os.WriteFile(plan, []byte(calibrateBenchTestPlan), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, plan
}

// calibrateBenchStatusServer answers the bench's four contract-question
// calls (one per code task) in order, one canned body per target status:
// firm, insufficient, below_threshold, then an unknown choice that Ask
// rejects, leaving that answer's status at its unavailable default.
func calibrateBenchStatusServer(t *testing.T) *httptest.Server {
	t.Helper()
	bodies := []string{
		`{"model":"jev-test","answers":{"contract":{"type":"choice","choice":"exported_change","confidence":0.95}},"usage":{"input_tokens":10}}`,
		`{"model":"jev-test","answers":{"contract":{"type":"choice","choice":"insufficient","confidence":0.99}},"usage":{"input_tokens":10}}`,
		`{"model":"jev-test","answers":{"contract":{"type":"choice","choice":"internal_only","confidence":0.5}},"usage":{"input_tokens":10}}`,
		`{"model":"jev-test","answers":{"contract":{"type":"choice","choice":"bogus","confidence":0.9}},"usage":{"input_tokens":10}}`,
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		if index < 0 || index >= len(bodies) {
			t.Fatalf("unexpected call %d", index+1)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodies[index]))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestClassifyCalibrateAcceptsBenchOutput proves that a calibrate run
// written by the real bench, covering every answer status, is read and
// validated without complaint: the reader is proven against what the bench
// actually writes, not a hand-written fixture.
func TestClassifyCalibrateAcceptsBenchOutput(t *testing.T) {
	t.Parallel()
	root, plan := calibrateBenchFixture(t)
	server := calibrateBenchStatusServer(t)
	var benchOut, benchErr strings.Builder
	benchArgs := []string{
		"judge", "classify", "bench", "--plan", plan, "--rubric", "v3",
		"--workspace", root, "--base-url", server.URL, "--split", "calibrate", "--json",
	}
	if err := run(benchArgs, &benchOut, &benchErr); err != nil {
		t.Fatalf("bench: %v (stderr %s)", err, benchErr.String())
	}
	output := benchOut.String()
	for _, status := range []string{
		`"status":"firm"`, `"status":"insufficient"`, `"status":"below_threshold"`,
		`"status":"unavailable"`, `"status":"not_asked"`,
	} {
		if !strings.Contains(output, status) {
			t.Fatalf("expected bench output to contain %s, got %s", status, output)
		}
	}
	runPath := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(runPath, []byte(output), 0o600); err != nil {
		t.Fatal(err)
	}
	var calibrateOut, calibrateErr strings.Builder
	if err := run([]string{"judge", "classify", "calibrate", "--run", runPath}, &calibrateOut, &calibrateErr); err != nil {
		t.Fatalf("calibrate rejected a real bench run: %v (stderr %s)", err, calibrateErr.String())
	}
}
