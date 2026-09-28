package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/batuta-ai/core/classify"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

// calibrateRequiredFields are the top-level keys a task record must carry:
// without them the record's downstream fields silently fall back to zero
// values, corrupting the grid and threshold search rather than failing loud.
var calibrateRequiredFields = []string{"split", "plan_lane", "code_lane", "judge_lane", "outcome", "scope", "packets", "answers"}

// calibrateSplit is the only split a calibrate run's records may carry: the
// procedure of judge-research.md section 14 selects the rule from the
// calibrate half alone, before the test half is read.
const calibrateSplit = "calibrate"

// The grids are the ones frozen in section 14 and validated by
// classify.RuleV3.Validate; they are repeated here because classify keeps
// its own grids private.
var (
	calibrateFHighGrid     = []int{4, 5, 6, 7, 8}
	calibrateFLowGrid      = []int{1, 2}
	calibrateDocsLowGrid   = []bool{false, true}
	calibrateThresholdGrid = []float64{0.7, 0.8, 0.9}
)

func runJudgeClassifyCalibrate(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge classify calibrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runPath := flags.String("run", "", "JSON records of a v3 bench calibrate run")
	outPath := flags.String("out", "", "file to write the selected rule as JSON")
	asJSON := flags.Bool("json", false, "print the grid, the selections and the rule as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *runPath == "" {
		return errors.New("usage: batuta judge classify calibrate --run <file> [--out <file>] [--json]")
	}
	records, err := readCalibrateRecords(*runPath)
	if err != nil {
		return fmt.Errorf("judge classify calibrate: %s: %w", *runPath, err)
	}
	if len(records) == 0 {
		return fmt.Errorf("judge classify calibrate: %s: holds no v3 record", *runPath)
	}
	for _, record := range records {
		if record.Split != calibrateSplit {
			return fmt.Errorf("judge classify calibrate: %s: task %s is not the calibrate half", *runPath, record.Task)
		}
	}
	selectedC := selectCalibrateC(records)
	_, codeLanes := calibrateGridSummary(records, selectedC.Selected.FHigh, selectedC.Selected.FLow, selectedC.Selected.DocsLow)
	thresholds := selectCalibrateT(records, codeLanes)
	selectedT := bestCalibrateT(thresholds)
	rule := classify.RuleV3{
		FHigh: selectedC.Selected.FHigh, FLow: selectedC.Selected.FLow,
		DocsLow: selectedC.Selected.DocsLow, Threshold: selectedT.Threshold,
	}
	if err := rule.Validate(); err != nil {
		return fmt.Errorf("judge classify calibrate: %w", err)
	}
	if *outPath != "" {
		payload, err := json.Marshal(rule)
		if err != nil {
			return err
		}
		if err := os.WriteFile(*outPath, payload, 0o600); err != nil {
			return fmt.Errorf("judge classify calibrate: %s: %w", *outPath, err)
		}
	}
	if *asJSON {
		return printCalibrateJSON(stdout, len(records), selectedC, thresholds, selectedT, rule)
	}
	return printCalibrateText(stdout, len(records), selectedC, thresholds, selectedT, rule)
}

// readCalibrateRecords decodes the concatenated JSON objects a v3 bench
// `--json` run writes: one benchV3Record per task, then the summary object
// under the key "summary" that classifyBenchTasksV3 writes last. Each line
// is decoded into a raw map first so a record missing a required field is
// rejected rather than silently completed with zero values, and the file is
// rejected unless the summary is present and its tasks count matches the
// number of task records read.
func readCalibrateRecords(path string) ([]benchV3Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var records []benchV3Record
	var summary *benchV3Summary
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if summaryRaw, ok := raw["summary"]; ok {
			var s benchV3Summary
			if err := json.Unmarshal(summaryRaw, &s); err != nil {
				return nil, fmt.Errorf("line %d: summary: %w", line, err)
			}
			summary = &s
			continue
		}
		taskRaw, ok := raw["task"]
		if !ok {
			continue
		}
		var task string
		if err := json.Unmarshal(taskRaw, &task); err != nil {
			return nil, fmt.Errorf("line %d: task: %w", line, err)
		}
		if task == "" {
			continue
		}
		if err := validateCalibrateRecordFields(raw); err != nil {
			return nil, fmt.Errorf("line %d: task %s: %w", line, task, err)
		}
		var record benchV3Record
		if err := json.Unmarshal([]byte(text), &record); err != nil {
			return nil, fmt.Errorf("line %d: task %s: %w", line, task, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if summary == nil {
		return nil, fmt.Errorf("%s: no trailing summary object", path)
	}
	if summary.Tasks != len(records) {
		return nil, fmt.Errorf("%s: summary tasks=%d does not match %d task records", path, summary.Tasks, len(records))
	}
	return records, nil
}

// validateCalibrateRecordFields checks that a task record carries every
// field the calibrate grid and threshold search read: split, plan_lane,
// code_lane, judge_lane, outcome and scope directly, plus a packets and an
// answers entry for both questions in benchV3Questions.
func validateCalibrateRecordFields(raw map[string]json.RawMessage) error {
	for _, field := range calibrateRequiredFields {
		if _, ok := raw[field]; !ok {
			return fmt.Errorf("missing %q", field)
		}
	}
	for _, field := range []string{"packets", "answers"} {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw[field], &entries); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		for _, question := range benchV3Questions {
			if _, ok := entries[question]; !ok {
				return fmt.Errorf("%s: missing %q entry", field, question)
			}
		}
	}
	return nil
}

// calibrateCodeLane mirrors classify.CodeLaneV3's first-match order without
// needing the task text: the open marker and Scope features are already
// recorded per task, so no judge or parser call is made offline.
func calibrateCodeLane(openMarker bool, features classify.ScopeFeatures, fHigh, fLow int, docsLow bool) routing.Complexity {
	switch {
	case openMarker:
		return routing.ComplexityCritical
	case features.Files >= fHigh || features.Directories >= 3:
		return routing.ComplexityHigh
	case features.Files <= fLow && !(features.DocsOnly && !docsLow):
		return routing.ComplexityLow
	default:
		return routing.ComplexityMedium
	}
}

// calibrateGridPoint is one point of the twenty-point grid for lane C:
// section 14's four measures, computed the same way the bench computes
// them, from the same benchV3LaneCounts.
type calibrateGridPoint struct {
	FHigh         int     `json:"f_high"`
	FLow          int     `json:"f_low"`
	DocsLow       bool    `json:"docs_low"`
	Economy       float64 `json:"economy"`
	Safety        float64 `json:"safety"`
	LargestLane   string  `json:"largest_lane"`
	LargestShare  float64 `json:"largest_share"`
	Lanes         int     `json:"lanes"`
	Balance       float64 `json:"balance"`
	Discriminates bool    `json:"discriminates"`
}

func (p calibrateGridPoint) text() string {
	return fmt.Sprintf("f_high=%d f_low=%d docs_low=%t economy=%.4f safety=%.4f largest_lane=%s largest_share=%.4f lanes=%d balance=%.4f discriminates=%t",
		p.FHigh, p.FLow, p.DocsLow, p.Economy, p.Safety, p.LargestLane, p.LargestShare, p.Lanes, p.Balance, p.Discriminates)
}

// calibrateGridSummary recomputes lane C for every record at one grid point
// and folds the results with the same counts the bench uses, returning both
// the point and the per-record lane so the selected point's lanes can be
// reused when lane J is recomputed.
func calibrateGridSummary(records []benchV3Record, fHigh, fLow int, docsLow bool) (calibrateGridPoint, []routing.Complexity) {
	counts := &benchV3LaneCounts{distribution: map[string]int{}}
	lanes := make([]routing.Complexity, len(records))
	for i, record := range records {
		lane := calibrateCodeLane(record.OpenMarker, record.Scope, fHigh, fLow, docsLow)
		lanes[i] = lane
		counts.add(string(lane), record.PlanLane, record.Outcome)
	}
	summary := counts.summary(len(records))
	point := calibrateGridPoint{
		FHigh: fHigh, FLow: fLow, DocsLow: docsLow,
		Economy: summary.Economy.Share, Safety: summary.Safety.Share,
		LargestLane: summary.Discrimination.LargestLane, LargestShare: summary.Discrimination.LargestShare,
		Lanes: summary.Discrimination.Lanes, Balance: summary.Balance.Share,
		Discriminates: summary.Discrimination.Pass,
	}
	return point, lanes
}

// calibrateCSelection is the twenty-point grid plus the point selection.
type calibrateCSelection struct {
	Grid          []calibrateGridPoint `json:"grid"`
	Selected      calibrateGridPoint   `json:"selected"`
	Discriminated bool                 `json:"discriminated"`
}

// selectCalibrateC computes the twenty-point grid and applies section 14's
// selection: among the points where no lane holds more than 70% and at
// least 3 lanes are used, the highest balance, ties to the higher economy,
// then the lower FHigh, then the lower FLow, then DocsLow false. When no
// point discriminates, the highest balance is taken over the whole grid and
// Discriminated is reported false.
func selectCalibrateC(records []benchV3Record) calibrateCSelection {
	grid := make([]calibrateGridPoint, 0, len(calibrateFHighGrid)*len(calibrateFLowGrid)*len(calibrateDocsLowGrid))
	for _, fHigh := range calibrateFHighGrid {
		for _, fLow := range calibrateFLowGrid {
			for _, docsLow := range calibrateDocsLowGrid {
				point, _ := calibrateGridSummary(records, fHigh, fLow, docsLow)
				grid = append(grid, point)
			}
		}
	}
	best, discriminated := -1, false
	for i := range grid {
		if !grid[i].Discriminates {
			continue
		}
		if best == -1 || betterCalibrateC(grid[i], grid[best]) {
			best = i
			discriminated = true
		}
	}
	if best == -1 {
		for i := range grid {
			if best == -1 || betterCalibrateC(grid[i], grid[best]) {
				best = i
			}
		}
	}
	return calibrateCSelection{Grid: grid, Selected: grid[best], Discriminated: discriminated}
}

// betterCalibrateC reports whether a is preferred over b by section 14's
// tie-break order for lane C: highest balance, then higher economy, then
// lower FHigh, then lower FLow, then DocsLow false.
func betterCalibrateC(a, b calibrateGridPoint) bool {
	if a.Balance != b.Balance {
		return a.Balance > b.Balance
	}
	if a.Economy != b.Economy {
		return a.Economy > b.Economy
	}
	if a.FHigh != b.FHigh {
		return a.FHigh < b.FHigh
	}
	if a.FLow != b.FLow {
		return a.FLow < b.FLow
	}
	return !a.DocsLow && b.DocsLow
}

// calibrateThresholdPoint is one point of lane J's threshold grid, C fixed.
type calibrateThresholdPoint struct {
	Threshold float64 `json:"threshold"`
	Economy   float64 `json:"economy"`
	Safety    float64 `json:"safety"`
	Balance   float64 `json:"balance"`
}

func (p calibrateThresholdPoint) text() string {
	return fmt.Sprintf("threshold=%.2f economy=%.4f safety=%.4f balance=%.4f", p.Threshold, p.Economy, p.Safety, p.Balance)
}

// calibrateAnswers rebuilds the answers map DecideV3 expects from what the
// record kept: a firm, insufficient or below-threshold answer kept its
// choice and confidence, so a different threshold can be applied to it
// offline; not-asked and unavailable answers carry nothing and change
// nothing, exactly as an unavailable answer would live.
func calibrateAnswers(record benchV3Record) (map[string]bool, map[string]judge.Answer) {
	asked := make(map[string]bool, len(benchV3Questions))
	answers := make(map[string]judge.Answer, len(benchV3Questions))
	for _, key := range benchV3Questions {
		if !record.Packets[key].Found {
			continue
		}
		asked[key] = true
		answer := record.Answers[key]
		if answer.Status == "unavailable" || answer.Status == "not_asked" {
			continue
		}
		answers[key] = judge.Answer{
			Type: judge.QuestionChoice, Choice: answer.Choice,
			Confidence: answer.Confidence, Probabilities: answer.Probabilities,
		}
	}
	return asked, answers
}

// selectCalibrateT recomputes lane J for every threshold of the frozen grid,
// C fixed, and folds the results with the same counts the bench uses.
func selectCalibrateT(records []benchV3Record, codeLanes []routing.Complexity) []calibrateThresholdPoint {
	points := make([]calibrateThresholdPoint, 0, len(calibrateThresholdGrid))
	for _, threshold := range calibrateThresholdGrid {
		counts := &benchV3LaneCounts{distribution: map[string]int{}}
		for i, record := range records {
			asked, answers := calibrateAnswers(record)
			decision := classify.DecideV3(codeLanes[i], asked, answers, threshold)
			counts.add(string(decision.Complexity), record.PlanLane, record.Outcome)
		}
		summary := counts.summary(len(records))
		points = append(points, calibrateThresholdPoint{
			Threshold: threshold, Economy: summary.Economy.Share,
			Safety: summary.Safety.Share, Balance: summary.Balance.Share,
		})
	}
	return points
}

// bestCalibrateT selects the highest balance, ties to the higher threshold.
func bestCalibrateT(points []calibrateThresholdPoint) calibrateThresholdPoint {
	best := points[0]
	for _, point := range points[1:] {
		if point.Balance > best.Balance || (point.Balance == best.Balance && point.Threshold > best.Threshold) {
			best = point
		}
	}
	return best
}

func printCalibrateText(stdout io.Writer, records int, c calibrateCSelection, t []calibrateThresholdPoint, bestT calibrateThresholdPoint, rule classify.RuleV3) error {
	if _, err := fmt.Fprintf(stdout, "records=%d\n", records); err != nil {
		return err
	}
	for _, point := range c.Grid {
		if _, err := fmt.Fprintln(stdout, "grid "+point.text()); err != nil {
			return err
		}
	}
	if !c.Discriminated {
		if _, err := fmt.Fprintln(stdout, "c discrimination=FAIL selecting the highest balance anyway"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout, "selected "+c.Selected.text()); err != nil {
		return err
	}
	for _, point := range t {
		if _, err := fmt.Fprintln(stdout, point.text()); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "selected threshold=%.2f\n", bestT.Threshold); err != nil {
		return err
	}
	payload, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "rule=%s\n", payload)
	return err
}

type calibrateJSON struct {
	Records           int                       `json:"records"`
	C                 calibrateCSelection       `json:"c"`
	T                 []calibrateThresholdPoint `json:"t"`
	SelectedThreshold float64                   `json:"selected_threshold"`
	Rule              classify.RuleV3           `json:"rule"`
}

func printCalibrateJSON(stdout io.Writer, records int, c calibrateCSelection, t []calibrateThresholdPoint, bestT calibrateThresholdPoint, rule classify.RuleV3) error {
	return json.NewEncoder(stdout).Encode(calibrateJSON{
		Records: records, C: c, T: t, SelectedThreshold: bestT.Threshold, Rule: rule,
	})
}
