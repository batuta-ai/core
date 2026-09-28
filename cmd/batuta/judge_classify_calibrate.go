package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/batuta-ai/core/classify"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

// calibrateRequiredFields are the top-level keys a task record must carry:
// without them the record's downstream fields silently fall back to zero
// values, corrupting the grid and threshold search rather than failing loud.
var calibrateRequiredFields = []string{"split", "plan_lane", "code_lane", "judge_lane", "outcome", "open_marker", "scope", "packets", "answers"}

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
// is decoded into a raw map first so a record missing a required field, or
// carrying a null or mistyped one, is rejected rather than silently
// completed with zero values. The summary must be the last non-blank line:
// any further content after it, a second summary, or a JSON object that is
// neither a task record nor the summary is rejected by line number, and the
// file is rejected unless the summary is present and its tasks count
// matches the number of task records read.
func readCalibrateRecords(path string) ([]benchV3Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var records []benchV3Record
	var summary *benchV3Summary
	summaryLine := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if summaryLine != 0 {
			return nil, fmt.Errorf("line %d: content after the summary on line %d", line, summaryLine)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		switch _, hasSummary := raw["summary"]; {
		case hasSummary:
			summaryRaw := raw["summary"]
			if err := calibrateValidateSummaryTasks(summaryRaw); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			var s benchV3Summary
			if err := json.Unmarshal(summaryRaw, &s); err != nil {
				return nil, fmt.Errorf("line %d: summary: %w", line, err)
			}
			summary = &s
			summaryLine = line
		default:
			taskRaw, hasTask := raw["task"]
			if !hasTask {
				return nil, fmt.Errorf("line %d: neither a task record nor the summary", line)
			}
			var task string
			if err := json.Unmarshal(taskRaw, &task); err != nil {
				return nil, fmt.Errorf("line %d: task: %w", line, err)
			}
			if task == "" {
				return nil, fmt.Errorf("line %d: task: empty", line)
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

// calibrateValidAnswerStatuses are the only values validateCalibrateRecordFields
// accepts for an answers entry's status: the ones DecideV3 and calibrateAnswers
// know how to interpret.
var calibrateValidAnswerStatuses = map[string]bool{
	"firm": true, "insufficient": true, "below_threshold": true,
	"not_asked": true, "unavailable": true,
}

// calibrateAnsweredStatuses are the statuses a found packet's answer may
// carry once the judge has actually answered it: the ones that write choice
// and probabilities and a nonzero confidence, as opposed to not_asked and
// unavailable, which never do.
var calibrateAnsweredStatuses = map[string]bool{
	"firm": true, "insufficient": true, "below_threshold": true,
}

// calibrateValidSplits are the only values a record's split may carry: the
// runJudgeClassifyCalibrate mixed-half check rejects "test" by name once
// every record has been read, but a validly typed value must still be one
// of the two halves a v3 bench run ever writes.
var calibrateValidSplits = map[string]bool{"calibrate": true, "test": true}

// calibrateValidLanes are the only values plan_lane, code_lane and
// judge_lane may carry: routing.Complexity's own labels.
var calibrateValidLanes = calibrateLabelSet(benchComplexityLabels)

// calibrateValidOutcomes are the only values outcome may carry: the
// benchOutcome* constants benchOutcome itself can produce.
var calibrateValidOutcomes = calibrateLabelSet(benchOutcomeLabels)

func calibrateLabelSet(labels []string) map[string]bool {
	set := make(map[string]bool, len(labels))
	for _, label := range labels {
		set[label] = true
	}
	return set
}

// calibrateQuestionChoices are the options an answered question's choice may
// hold: classify's own v3ContractCriteria and v3SecurityCriteria keys,
// repeated here because classify keeps them private.
var calibrateQuestionChoices = map[string]map[string]bool{
	"contract": {"exported_change": true, "internal_only": true, "insufficient": true},
	"security": {"security_behaviour": true, "incidental": true, "insufficient": true},
}

// validateCalibrateRecordFields checks that a task record carries every
// field the calibrate grid and threshold search read, and that each holds a
// well-typed, in-range value rather than merely existing: split, plan_lane,
// code_lane and judge_lane must each be one of calibrateValidSplits or
// calibrateValidLanes, outcome one of calibrateValidOutcomes, open_marker a
// boolean, scope an object with non-negative integer Files and Directories
// and boolean TestOnly and DocsOnly, and packets and answers must each carry
// an entry for both questions in benchV3Questions. A packet's found must be
// a boolean and its size a non-negative integer that is zero when found is
// false. An answer's status must be one of calibrateValidAnswerStatuses and
// consistent with its packet's found: not_asked exactly when found is
// false. When the status is one of calibrateAnsweredStatuses, choice must be
// one of the question's options, confidence a number in [0,1], and
// probabilities, when present, an object of numbers in [0,1].
func validateCalibrateRecordFields(raw map[string]json.RawMessage) error {
	for _, field := range calibrateRequiredFields {
		if _, ok := raw[field]; !ok {
			return fmt.Errorf("missing %q", field)
		}
	}
	if value, err := calibrateRequireString(raw, "split"); err != nil {
		return err
	} else if !calibrateValidSplits[value] {
		return fmt.Errorf("split: %q is not one of calibrate, test", value)
	}
	for _, field := range []string{"plan_lane", "code_lane", "judge_lane"} {
		value, err := calibrateRequireString(raw, field)
		if err != nil {
			return err
		}
		if !calibrateValidLanes[value] {
			return fmt.Errorf("%s: %q is not one of low, medium, high, critical", field, value)
		}
	}
	if value, err := calibrateRequireString(raw, "outcome"); err != nil {
		return err
	} else if !calibrateValidOutcomes[value] {
		return fmt.Errorf("outcome: %q is not one of %s", value, strings.Join(benchOutcomeLabels, ", "))
	}
	if _, err := calibrateRequireBool(raw, "open_marker"); err != nil {
		return err
	}
	if err := calibrateValidateScope(raw["scope"]); err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	var packetsRaw, answersRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw["packets"], &packetsRaw); err != nil {
		return fmt.Errorf("packets: %w", err)
	}
	if err := json.Unmarshal(raw["answers"], &answersRaw); err != nil {
		return fmt.Errorf("answers: %w", err)
	}
	found := make(map[string]bool, len(benchV3Questions))
	for _, question := range benchV3Questions {
		entry, err := calibrateRequireEntry(packetsRaw, question, "packets")
		if err != nil {
			return err
		}
		isFound, err := calibrateRequireBool(entry, "found")
		if err != nil {
			return fmt.Errorf("packets: %q entry: %w", question, err)
		}
		size, err := calibrateRequireNonNegativeInt(entry, "size")
		if err != nil {
			return fmt.Errorf("packets: %q entry: %w", question, err)
		}
		if !isFound && size > 0 {
			return fmt.Errorf("packets: %q entry: size %d is above zero but found is false", question, size)
		}
		found[question] = isFound
	}
	for _, question := range benchV3Questions {
		entry, err := calibrateRequireEntry(answersRaw, question, "answers")
		if err != nil {
			return err
		}
		status, err := calibrateRequireString(entry, "status")
		if err != nil {
			return fmt.Errorf("answers: %q entry: %w", question, err)
		}
		if !calibrateValidAnswerStatuses[status] {
			return fmt.Errorf("answers: %q entry: status %q is not one of firm, insufficient, below_threshold, not_asked, unavailable", question, status)
		}
		switch {
		case found[question] && status == "not_asked":
			return fmt.Errorf("answers: %q entry: status is not_asked but packets found is true", question)
		case !found[question] && status != "not_asked":
			return fmt.Errorf("answers: %q entry: status %q but packets found is false", question, status)
		}
		if !calibrateAnsweredStatuses[status] {
			continue
		}
		choice, err := calibrateRequireString(entry, "choice")
		if err != nil {
			return fmt.Errorf("answers: %q entry: %w", question, err)
		}
		if !calibrateQuestionChoices[question][choice] {
			return fmt.Errorf("answers: %q entry: choice %q is not one of %s's options", question, choice, question)
		}
		if err := calibrateRequireRange01(entry, "confidence"); err != nil {
			return fmt.Errorf("answers: %q entry: %w", question, err)
		}
		if err := calibrateValidateProbabilities(entry); err != nil {
			return fmt.Errorf("answers: %q entry: %w", question, err)
		}
	}
	return nil
}

// calibrateRequireEntry decodes a required, non-null object entry for a
// question out of a packets or answers map, naming the field and the
// question on failure.
func calibrateRequireEntry(entries map[string]json.RawMessage, question, field string) (map[string]json.RawMessage, error) {
	entryRaw, ok := entries[question]
	if !ok {
		return nil, fmt.Errorf("%s: missing %q entry", field, question)
	}
	if calibrateIsJSONNull(entryRaw) {
		return nil, fmt.Errorf("%s: %q entry is null", field, question)
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(entryRaw, &entry); err != nil {
		return nil, fmt.Errorf("%s: %q entry: %w", field, question, err)
	}
	return entry, nil
}

// calibrateIsJSONNull reports whether a raw JSON value is the literal null,
// the one case json.Unmarshal accepts silently into a struct or map field
// without changing it, letting a null value pass a presence check unnoticed.
func calibrateIsJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// calibrateRequireString reads a required, non-null, non-empty string field
// from a raw JSON object's decoded key/value map.
func calibrateRequireString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok || calibrateIsJSONNull(raw) {
		return "", fmt.Errorf("%s: missing or null", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s: not a string: %w", key, err)
	}
	if value == "" {
		return "", fmt.Errorf("%s: empty", key)
	}
	return value, nil
}

// calibrateRequireBool reads a required, non-null boolean field from a raw
// JSON object's decoded key/value map.
func calibrateRequireBool(fields map[string]json.RawMessage, key string) (bool, error) {
	raw, ok := fields[key]
	if !ok || calibrateIsJSONNull(raw) {
		return false, fmt.Errorf("%s: missing or null", key)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("%s: not a boolean: %w", key, err)
	}
	return value, nil
}

// calibrateRequireNumber reads a required, non-null numeric field from a raw
// JSON object's decoded key/value map.
func calibrateRequireNumber(fields map[string]json.RawMessage, key string) (float64, error) {
	raw, ok := fields[key]
	if !ok || calibrateIsJSONNull(raw) {
		return 0, fmt.Errorf("%s: missing or null", key)
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%s: not a number: %w", key, err)
	}
	return value, nil
}

// calibrateRequireNonNegativeInt reads a required, non-null field that must
// hold an integer at or above zero: the counts (Files, Directories, a
// packet's size) that a negative or fractional value could never truthfully
// represent.
func calibrateRequireNonNegativeInt(fields map[string]json.RawMessage, key string) (int, error) {
	value, err := calibrateRequireNumber(fields, key)
	if err != nil {
		return 0, err
	}
	if value != math.Trunc(value) {
		return 0, fmt.Errorf("%s: %v is not an integer", key, value)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s: %v is negative", key, value)
	}
	return int(value), nil
}

// calibrateRequireRange01 reads a required, non-null numeric field and
// checks it falls within [0,1], the range every confidence and probability
// value in a bench v3 record is defined over.
func calibrateRequireRange01(fields map[string]json.RawMessage, key string) error {
	value, err := calibrateRequireNumber(fields, key)
	if err != nil {
		return err
	}
	if value < 0 || value > 1 {
		return fmt.Errorf("%s: %v is not between 0 and 1", key, value)
	}
	return nil
}

// calibrateValidateProbabilities checks an answered entry's probabilities
// field when present: the bench writes it with omitempty, so it is entirely
// absent for not_asked and unavailable answers, but when present it must be
// an object of numbers in [0,1].
func calibrateValidateProbabilities(entry map[string]json.RawMessage) error {
	raw, ok := entry["probabilities"]
	if !ok {
		return nil
	}
	if calibrateIsJSONNull(raw) {
		return errors.New("probabilities: not an object")
	}
	var probabilities map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probabilities); err != nil {
		return fmt.Errorf("probabilities: not an object: %w", err)
	}
	for option, valueRaw := range probabilities {
		var value float64
		if err := json.Unmarshal(valueRaw, &value); err != nil {
			return fmt.Errorf("probabilities: %s: not a number: %w", option, err)
		}
		if value < 0 || value > 1 {
			return fmt.Errorf("probabilities: %s: %v is not between 0 and 1", option, value)
		}
	}
	return nil
}

// calibrateValidateSummaryTasks checks that the trailing summary object's
// tasks field is present, non-null and a non-negative integer: unmarshaling
// straight into benchV3Summary's int field would silently accept a missing,
// null or negative value as zero or as itself, rather than rejecting it.
func calibrateValidateSummaryTasks(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("summary: not an object: %w", err)
	}
	if _, err := calibrateRequireNonNegativeInt(fields, "tasks"); err != nil {
		return fmt.Errorf("summary: %w", err)
	}
	return nil
}

// calibrateValidateScope checks that a record's scope field is a non-null
// object carrying classify.ScopeFeatures' fields with their declared types:
// non-negative integer Files and Directories, boolean TestOnly and DocsOnly.
// classify.ScopeFeatures has no JSON tags, so its keys are exactly those Go
// field names.
func calibrateValidateScope(raw json.RawMessage) error {
	if calibrateIsJSONNull(raw) {
		return errors.New("missing or null")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("not an object: %w", err)
	}
	for _, key := range []string{"Files", "Directories"} {
		if _, err := calibrateRequireNonNegativeInt(fields, key); err != nil {
			return err
		}
	}
	for _, key := range []string{"TestOnly", "DocsOnly"} {
		if _, err := calibrateRequireBool(fields, key); err != nil {
			return err
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
