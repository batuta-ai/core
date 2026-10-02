package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/questions"
)

const questionsBenchDefaultThreshold = 0.9

type questionsBenchRecordResult struct {
	ID               string                     `json:"id"`
	Split            string                     `json:"split"`
	CodeKind         string                     `json:"code_kind"`
	Kind             string                     `json:"kind"`
	AnswerInPassage  string                     `json:"answer_in_passage"`
	Called           bool                       `json:"called"`
	Option           string                     `json:"option,omitempty"`
	Confidence       float64                    `json:"confidence"`
	Probabilities    map[string]float64         `json:"probabilities,omitempty"`
	Status           string                     `json:"status"`
	InputTokens      *int                       `json:"input_tokens,omitempty"`
	Unavailable      string                     `json:"unavailable,omitempty"`
	Reason           string                     `json:"reason,omitempty"`
	AnswerRepeats    bool                       `json:"answer_repeats_passage"`
	PassageMode      string                     `json:"passage_mode,omitempty"`
	Units            *int                       `json:"units,omitempty"`
	BestUnit         *int                       `json:"best_unit,omitempty"`
	BestProbability  *float64                   `json:"best_probability,omitempty"`
	UnitsAtThreshold *int                       `json:"units_at_threshold,omitempty"`
	UnitBytesMax     *int                       `json:"unit_bytes_max,omitempty"`
	Calls            *int                       `json:"calls,omitempty"`
	UnavailableCalls *int                       `json:"unavailable_calls,omitempty"`
	Retrieval        string                     `json:"retrieval,omitempty"`
	UnitResults      []questionsBenchUnitResult `json:"unit_results,omitempty"`
	wallTime         time.Duration
}

type questionsBenchUnitResult struct {
	Option      string  `json:"option"`
	Confidence  float64 `json:"confidence"`
	Probability float64 `json:"probability"`
	Status      string  `json:"status"`
}

type questionsBenchKindScore struct {
	Precision questionsCount `json:"precision"`
	Recall    questionsCount `json:"recall"`
	Status    string         `json:"status"`
}

type questionsBenchCriterion1 struct {
	Kinds  map[string]questionsBenchKindScore `json:"kinds"`
	Status string                             `json:"status"`
}

type questionsBenchCriterion2 struct {
	Yes       questionsCount `json:"yes"`
	NoUnclear questionsCount `json:"no_or_unclear"`
	Status    string         `json:"status"`
}

type questionsBenchCounts struct {
	Questions                    int                                 `json:"questions"`
	Excluded                     int                                 `json:"excluded"`
	Confusion                    map[string]map[string]int           `json:"confusion"`
	Criterion1                   questionsBenchCriterion1            `json:"criterion1"`
	Criterion2                   questionsBenchCriterion2            `json:"criterion2"`
	Agreement                    questionsCount                      `json:"answer_agreement"`
	Calls                        int                                 `json:"calls"`
	Unavailable                  int                                 `json:"unavailable"`
	InputTokens                  int                                 `json:"input_tokens"`
	UnavailableCalls             *int                                `json:"unavailable_calls,omitempty"`
	QuestionsExcluded            *int                                `json:"questions_excluded,omitempty"`
	Retrieval                    map[string]int                      `json:"retrieval,omitempty"`
	FirmByCodeKind               map[string]questionsBenchFirmCounts `json:"firm_by_code_kind,omitempty"`
	UnitsDistribution            *questionsBenchDistribution         `json:"units_distribution,omitempty"`
	UnitsAtThresholdDistribution *questionsBenchDistribution         `json:"units_at_threshold_distribution,omitempty"`
	WallTimeMs                   *int64                              `json:"wall_time_ms,omitempty"`
}

type questionsBenchFirmCounts struct {
	Yes     questionsCount `json:"yes"`
	No      questionsCount `json:"no"`
	Unclear questionsCount `json:"unclear"`
}
type questionsBenchDistribution struct {
	Min    int     `json:"min"`
	Median float64 `json:"median"`
	Max    int     `json:"max"`
}

type questionsBenchSummary struct {
	questionsBenchCounts
	BySplit map[string]questionsBenchCounts `json:"by_split"`
}

func runQuestionsBench(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("judge questions bench", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	corpusPath := flags.String("corpus", "", "corpus path")
	sheetPath := flags.String("sheet", "", "label sheet")
	split := flags.String("split", "", "calibrate or test")
	thresholdFlag := flags.String("threshold", "", "firm confidence threshold")
	configPath := flags.String("config", "", "judge config path")
	workspace := flags.String("workspace", "", "workspace directory")
	baseURL := flags.String("base-url", "", "judge base URL")
	asJSON := flags.Bool("json", false, "JSON output")
	passageMode := flags.String("passage", "full", "full or units")
	all := flags.Bool("all", false, "ask every code kind")
	if err := flags.Parse(args); err != nil {
		return err
	}
	passageExplicit := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "passage" {
			passageExplicit = true
		}
	})
	if flags.NArg() != 0 || *corpusPath == "" || *sheetPath == "" {
		return errors.New(questionsUsage)
	}
	if *split != "" && *split != "calibrate" && *split != "test" {
		return fmt.Errorf("invalid --split %q; use calibrate or test", *split)
	}
	if *passageMode != "full" && *passageMode != "units" {
		return fmt.Errorf("invalid --passage %q; use full or units", *passageMode)
	}
	corpus, err := readQuestionsCorpus(*corpusPath)
	if err != nil {
		return err
	}
	labels, err := readQuestionsLabels(corpus.records, *sheetPath)
	if err != nil {
		return err
	}
	config, err := loadJudgeConfig(*configPath, *workspace)
	if err != nil {
		return err
	}
	threshold := config.Decision("question_match").Threshold
	if threshold == 0 {
		threshold = questionsBenchDefaultThreshold
	}
	if *thresholdFlag != "" {
		threshold, err = strconv.ParseFloat(*thresholdFlag, 64)
		if err != nil {
			return fmt.Errorf("invalid --threshold %q: %w", *thresholdFlag, err)
		}
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
		return fmt.Errorf("threshold %g must be between 0 and 1", threshold)
	}
	if *baseURL != "" {
		config.BaseURL = *baseURL
	}
	j, buildReason, err := corpusRunJudge(config)
	if err != nil {
		return err
	}
	records, summary := questionsBenchRecordsMode(context.Background(), j, buildReason, corpus.records, labels, *split, threshold, *passageMode, *all)
	if *passageMode == "full" && (passageExplicit || *all) {
		for i := range records {
			records[i].PassageMode = "full"
		}
	}
	if *asJSON {
		encoder := json.NewEncoder(stdout)
		for _, record := range records {
			if err := encoder.Encode(record); err != nil {
				return err
			}
		}
		if err := encoder.Encode(map[string]any{"summary": summary, "threshold": threshold}); err != nil {
			return err
		}
	} else if err := printQuestionsBench(stdout, records, summary, threshold); err != nil {
		return err
	}
	if summary.Unavailable > 0 {
		return &ExitError{Code: 2, State: "unavailable"}
	}
	return nil
}

func questionsBenchRecords(ctx context.Context, j judge.Judge, buildReason string, corpus []questionCorpusRecord, labels map[string]questionsLabel, split string, threshold float64) ([]questionsBenchRecordResult, questionsBenchSummary) {
	return questionsBenchRecordsMode(ctx, j, buildReason, corpus, labels, split, threshold, "full", false)
}

func questionsBenchRecordsMode(ctx context.Context, j judge.Judge, buildReason string, corpus []questionCorpusRecord, labels map[string]questionsLabel, split string, threshold float64, passageMode string, all bool) ([]questionsBenchRecordResult, questionsBenchSummary) {
	var records []questionsBenchRecordResult
	var selected []questionCorpusRecord
	excluded := map[string]int{"calibrate": 0, "test": 0}
	for _, question := range corpus {
		if split != "" && question.Split != split {
			continue
		}
		if !question.PlanFound {
			excluded[question.Split]++
			continue
		}
		selected = append(selected, question)
		label := labels[question.ID]
		if passageMode == "units" {
			records = append(records, questionsBenchUnitRecordFor(ctx, j, buildReason, question, label, threshold, all))
		} else {
			record := questionsBenchRecordFor(ctx, j, buildReason, question, label, threshold)
			if all && question.CodeKind != "other" {
				record = questionsBenchFullRecordFor(ctx, j, buildReason, question, label, threshold)
			}
			records = append(records, record)
		}
	}
	summary := questionsBenchSummary{questionsBenchCounts: questionsBenchCountRecords(selected, records), BySplit: map[string]questionsBenchCounts{}}
	summary.Excluded = excluded["calibrate"] + excluded["test"]
	for _, name := range []string{"calibrate", "test"} {
		var splitCorpus []questionCorpusRecord
		var splitRecords []questionsBenchRecordResult
		for i, question := range selected {
			if question.Split == name {
				splitCorpus = append(splitCorpus, question)
				splitRecords = append(splitRecords, records[i])
			}
		}
		counts := questionsBenchCountRecords(splitCorpus, splitRecords)
		counts.Excluded = excluded[name]
		summary.BySplit[name] = counts
	}
	if passageMode == "units" {
		summary.questionsBenchCounts = questionsBenchCountRecordsV2(selected, records)
		for _, name := range []string{"calibrate", "test"} {
			var partCorpus []questionCorpusRecord
			var partRecords []questionsBenchRecordResult
			for i, question := range selected {
				if question.Split == name {
					partCorpus = append(partCorpus, question)
					partRecords = append(partRecords, records[i])
				}
			}
			counts := questionsBenchCountRecordsV2(partCorpus, partRecords)
			counts.Excluded = excluded[name]
			summary.BySplit[name] = counts
		}
		summary.Excluded = excluded["calibrate"] + excluded["test"]
	}
	return records, summary
}

func questionsBenchRecordFor(ctx context.Context, j judge.Judge, buildReason string, question questionCorpusRecord, label questionsLabel, threshold float64) questionsBenchRecordResult {
	record := questionsBenchRecordResult{ID: question.ID, Split: question.Split, CodeKind: question.CodeKind, Kind: label.Kind, AnswerInPassage: label.AnswerInPassage, Status: "not_asked"}
	if question.CodeKind != "other" {
		return record
	}
	return questionsBenchFullRecordFor(ctx, j, buildReason, question, label, threshold)
}

func questionsBenchFullRecordFor(ctx context.Context, j judge.Judge, buildReason string, question questionCorpusRecord, label questionsLabel, threshold float64) questionsBenchRecordResult {
	record := questionsBenchRecordResult{ID: question.ID, Split: question.Split, CodeKind: question.CodeKind, Kind: label.Kind, AnswerInPassage: label.AnswerInPassage, Status: "not_asked"}
	if j == nil {
		record.Status = "unavailable"
		record.Unavailable = buildReason
		return record
	}
	record.Called = true
	response, err := j.Ask(ctx, questions.BuildRequest(question.Question, question.Passage))
	if response.Usage.InputTokens != 0 {
		tokens := response.Usage.InputTokens
		record.InputTokens = &tokens
	}
	answer, usable := response.Answers["answer"]
	decision := questions.Decide(answer, threshold)
	if err != nil && (!benchV2AnswerMismatch(err) || !usable || decision.Status == "unavailable") {
		record.Status = "unavailable"
		record.Unavailable = judgeReplayReason(err)
		return record
	}
	if err != nil {
		record.Reason = judge.ReasonAnswerMismatch
	}
	if decision.Status == "unavailable" {
		record.Status = "unavailable"
		record.Unavailable = judge.ReasonAnswerMismatch
		return record
	}
	record.Option = answer.Choice
	record.Confidence = answer.Confidence
	record.Probabilities = answer.Probabilities
	record.Status = decision.Status
	record.AnswerRepeats = decision.Status == "firm" && decision.Option == "answered_here" && question.Answer != "" && strings.Contains(strings.ToLower(question.Passage), strings.ToLower(question.Answer))
	return record
}

func questionsBenchUnitRecordFor(ctx context.Context, j judge.Judge, buildReason string, question questionCorpusRecord, label questionsLabel, threshold float64, all bool) questionsBenchRecordResult {
	record := questionsBenchRecordResult{ID: question.ID, Split: question.Split, CodeKind: question.CodeKind, Kind: label.Kind, AnswerInPassage: label.AnswerInPassage, Status: "not_asked", PassageMode: "units", Retrieval: "none"}
	units := questions.Units(question.Passage)
	record.Units = benchInt(len(units))
	record.BestUnit = benchInt(-1)
	record.BestProbability = benchFloat(0)
	record.UnitsAtThreshold = benchInt(0)
	record.UnitBytesMax = benchInt(0)
	record.UnavailableCalls = benchInt(0)
	record.Calls = benchInt(0)
	for _, unit := range units {
		if len(unit) > *record.UnitBytesMax {
			*record.UnitBytesMax = len(unit)
		}
	}
	if !all && question.CodeKind != "other" {
		return record
	}
	if j == nil {
		record.Status = "unavailable"
		record.Unavailable = buildReason
		return record
	}
	start := time.Now()
	record.UnitResults = make([]questionsBenchUnitResult, 0, len(units))
	var tokens int
	bestValid := false
	for i, unit := range units {
		record.Called = true
		*record.Calls++
		response, err := j.Ask(ctx, questions.BuildRequest(question.Question, questions.UnitPacket(question.Passage, unit)))
		tokens += response.Usage.InputTokens
		answer := response.Answers["answer"]
		decision := questions.Decide(answer, threshold)
		result := questionsBenchUnitResult{Status: decision.Status}
		if err != nil || decision.Status == "unavailable" {
			result.Status = "unavailable"
			*record.UnavailableCalls++
			if threshold == 0 {
				*record.UnitsAtThreshold++
			}
			if *record.BestUnit == -1 {
				*record.BestUnit = i
				record.Status = "unavailable"
			}
			if record.Unavailable == "" {
				if err != nil {
					record.Unavailable = judgeReplayReason(err)
				} else {
					record.Unavailable = judge.ReasonAnswerMismatch
				}
			}
		} else {
			result.Option = answer.Choice
			result.Confidence = answer.Confidence
			result.Probability = answer.Probabilities["answered_here"]
			if result.Probability >= threshold {
				*record.UnitsAtThreshold++
			}
			if !bestValid || result.Probability > *record.BestProbability {
				bestValid = true
				*record.BestUnit = i
				*record.BestProbability = result.Probability
				record.Option = answer.Choice
				record.Confidence = answer.Confidence
				record.Probabilities = answer.Probabilities
				record.Status = decision.Status
				if decision.Status == "firm" && answer.Choice == "not_addressed" {
					record.Status = "not_addressed"
				}
				record.Reason = ""
			}
		}
		record.UnitResults = append(record.UnitResults, result)
	}
	record.wallTime = time.Since(start)
	if tokens != 0 {
		record.InputTokens = benchInt(tokens)
	}
	if len(units) == 0 || float64(*record.UnavailableCalls) > .1*float64(len(units)) {
		record.Status = "unavailable"
		if record.Unavailable == "" {
			record.Unavailable = "no_units"
		}
	} else {
		if record.Status != "unavailable" {
			record.Unavailable = ""
		}
	}
	if label.AnswerInPassage == "yes" {
		if fragment := questionsQuotedFragment(label.Note); fragment != "" && *record.BestUnit >= 0 {
			record.Retrieval = "dropped"
			if strings.Contains(strings.ToLower(units[*record.BestUnit]), strings.ToLower(fragment)) {
				record.Retrieval = "kept"
			}
		}
	}
	record.AnswerRepeats = record.Status == "firm" && record.Option == "answered_here" && question.Answer != "" && strings.Contains(strings.ToLower(question.Passage), strings.ToLower(question.Answer))
	return record
}

func questionsQuotedFragment(note string) string {
	for _, marks := range [][2]string{{"\"", "\""}, {"“", "”"}} {
		_, after, found := strings.Cut(note, marks[0])
		if found {
			fragment, _, closed := strings.Cut(after, marks[1])
			if closed && fragment != "" {
				return fragment
			}
		}
	}
	return ""
}

func benchInt(value int) *int           { return &value }
func benchFloat(value float64) *float64 { return &value }

func questionsBenchCountRecords(corpus []questionCorpusRecord, records []questionsBenchRecordResult) questionsBenchCounts {
	counts := questionsBenchCounts{Questions: len(records), Confusion: map[string]map[string]int{}, Criterion1: questionsBenchCriterion1{Kinds: map[string]questionsBenchKindScore{}, Status: "PASS"}}
	for _, kind := range questionsKinds {
		counts.Confusion[kind] = map[string]int{}
	}
	for i, record := range records {
		counts.Confusion[record.CodeKind][record.Kind]++
		if record.Called {
			counts.Calls++
		}
		if record.Unavailable != "" {
			counts.Unavailable++
		}
		if record.InputTokens != nil {
			counts.InputTokens += *record.InputTokens
		}
		if record.CodeKind != "other" {
			continue
		}
		firmHere := record.Status == "firm" && record.Option == "answered_here"
		if record.AnswerInPassage == "yes" {
			counts.Criterion2.Yes.Total++
			if firmHere {
				counts.Criterion2.Yes.Count++
			}
		} else {
			counts.Criterion2.NoUnclear.Total++
			if firmHere {
				counts.Criterion2.NoUnclear.Count++
			}
		}
		if firmHere && corpus[i].Answer != "" {
			counts.Agreement.Total++
			if record.AnswerRepeats {
				counts.Agreement.Count++
			}
		}
	}
	for _, kind := range []string{"scope_change", "environment", "continue"} {
		var predicted, actual int
		for _, other := range questionsKinds {
			predicted += counts.Confusion[kind][other]
			actual += counts.Confusion[other][kind]
		}
		correct := counts.Confusion[kind][kind]
		precision := questionsRatio(correct, predicted)
		recall := questionsRatio(correct, actual)
		status := "PASS"
		if precision.Share < .9 || recall.Share < .7 || predicted == 0 || actual == 0 {
			status = "FAIL"
			counts.Criterion1.Status = "FAIL"
		}
		counts.Criterion1.Kinds[kind] = questionsBenchKindScore{Precision: precision, Recall: recall, Status: status}
	}
	counts.Criterion2.Yes = questionsRatio(counts.Criterion2.Yes.Count, counts.Criterion2.Yes.Total)
	counts.Criterion2.NoUnclear = questionsRatio(counts.Criterion2.NoUnclear.Count, counts.Criterion2.NoUnclear.Total)
	counts.Agreement = questionsRatio(counts.Agreement.Count, counts.Agreement.Total)
	counts.Criterion2.Status = "reported only"
	if counts.Criterion2.Yes.Total >= 6 {
		counts.Criterion2.Status = "PASS"
		if counts.Criterion2.Yes.Share < .5 || counts.Criterion2.NoUnclear.Count > 0 {
			counts.Criterion2.Status = "FAIL"
		}
	}
	return counts
}

func questionsBenchCountRecordsV2(corpus []questionCorpusRecord, records []questionsBenchRecordResult) questionsBenchCounts {
	var eligibleCorpus []questionCorpusRecord
	var eligibleRecords []questionsBenchRecordResult
	for i, record := range records {
		if record.Status != "unavailable" {
			eligibleCorpus = append(eligibleCorpus, corpus[i])
			eligibleRecords = append(eligibleRecords, record)
		}
	}
	counts := questionsBenchCountRecords(eligibleCorpus, eligibleRecords)
	all := questionsBenchCountRecords(corpus, records)
	counts.Questions = len(records)
	counts.Confusion = all.Confusion
	counts.InputTokens = all.InputTokens
	counts.Unavailable = all.Unavailable
	counts.Calls = 0
	counts.UnavailableCalls = benchInt(0)
	counts.QuestionsExcluded = benchInt(len(records) - len(eligibleRecords))
	counts.Retrieval = map[string]int{"kept": 0, "dropped": 0, "none": 0}
	counts.FirmByCodeKind = map[string]questionsBenchFirmCounts{}
	for _, kind := range questionsKinds {
		counts.FirmByCodeKind[kind] = questionsBenchFirmCounts{}
	}
	var unitCounts, thresholdCounts []int
	var wall time.Duration
	for _, record := range records {
		if record.Calls != nil {
			counts.Calls += *record.Calls
		}
		if record.UnavailableCalls != nil {
			*counts.UnavailableCalls += *record.UnavailableCalls
		}
		wall += record.wallTime
		unitCounts = append(unitCounts, *record.Units)
		thresholdCounts = append(thresholdCounts, *record.UnitsAtThreshold)
		if record.AnswerInPassage == "yes" {
			counts.Retrieval[record.Retrieval]++
		}
		if record.Status != "unavailable" {
			firm := counts.FirmByCodeKind[record.CodeKind]
			firmHere := record.Status == "firm" && record.Option == "answered_here"
			switch record.AnswerInPassage {
			case "yes":
				firm.Yes.Total++
				if firmHere {
					firm.Yes.Count++
				}
			case "no":
				firm.No.Total++
				if firmHere {
					firm.No.Count++
				}
			case "unclear":
				firm.Unclear.Total++
				if firmHere {
					firm.Unclear.Count++
				}
			}
			counts.FirmByCodeKind[record.CodeKind] = firm
		}
	}
	for kind, firm := range counts.FirmByCodeKind {
		firm.Yes = questionsRatio(firm.Yes.Count, firm.Yes.Total)
		firm.No = questionsRatio(firm.No.Count, firm.No.Total)
		firm.Unclear = questionsRatio(firm.Unclear.Count, firm.Unclear.Total)
		counts.FirmByCodeKind[kind] = firm
	}
	counts.UnitsDistribution = questionsBenchDistributionFor(unitCounts)
	counts.UnitsAtThresholdDistribution = questionsBenchDistributionFor(thresholdCounts)
	ms := wall.Milliseconds()
	counts.WallTimeMs = &ms
	return counts
}

func questionsBenchDistributionFor(values []int) *questionsBenchDistribution {
	result := &questionsBenchDistribution{}
	if len(values) == 0 {
		return result
	}
	sort.Ints(values)
	result.Min, result.Max = values[0], values[len(values)-1]
	if len(values)%2 == 1 {
		result.Median = float64(values[len(values)/2])
	} else {
		result.Median = float64(values[len(values)/2-1]+values[len(values)/2]) / 2
	}
	return result
}

func questionsRatio(count, total int) questionsCount {
	ratio := questionsCount{Count: count, Total: total}
	if total > 0 {
		ratio.Share = float64(count) / float64(total)
	}
	return ratio
}

func printQuestionsBench(w io.Writer, records []questionsBenchRecordResult, summary questionsBenchSummary, threshold float64) error {
	if _, err := fmt.Fprintf(w, "threshold\t%s\n", strconv.FormatFloat(threshold, 'f', -1, 64)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "id\tsplit\tcode_kind\tkind\tanswer_in_passage\tcalled\toption\tconfidence\tprobabilities\tstatus\tunavailable\treason\tinput_tokens\tanswer_repeats_passage"); err != nil {
		return err
	}
	for _, record := range records {
		probabilities, err := json.Marshal(record.Probabilities)
		if err != nil {
			return err
		}
		tokens := ""
		if record.InputTokens != nil {
			tokens = strconv.Itoa(*record.InputTokens)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%t\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\n", questionsCell(record.ID), record.Split, record.CodeKind, record.Kind, record.AnswerInPassage, record.Called, record.Option, strconv.FormatFloat(record.Confidence, 'f', -1, 64), probabilities, record.Status, questionsCell(record.Unavailable), record.Reason, tokens, record.AnswerRepeats); err != nil {
			return err
		}
		if record.PassageMode == "units" {
			unitResults, err := json.Marshal(record.UnitResults)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "units\t%s\t%s\t%d\t%d\t%s\t%d\t%d\t%d\t%d\t%s\t%s\n", questionsCell(record.ID), record.PassageMode, *record.Units, *record.BestUnit, strconv.FormatFloat(*record.BestProbability, 'f', -1, 64), *record.UnitsAtThreshold, *record.UnitBytesMax, *record.Calls, *record.UnavailableCalls, record.Retrieval, unitResults); err != nil {
				return err
			}
		}
	}
	for _, part := range []struct {
		name   string
		counts questionsBenchCounts
	}{{"all", summary.questionsBenchCounts}, {"calibrate", summary.BySplit["calibrate"]}, {"test", summary.BySplit["test"]}} {
		if _, err := fmt.Fprintf(w, "summary\t%s\tquestions\t%d\texcluded\t%d\tcalls\t%d\tunavailable\t%d\tinput_tokens\t%d\n", part.name, part.counts.Questions, part.counts.Excluded, part.counts.Calls, part.counts.Unavailable, part.counts.InputTokens); err != nil {
			return err
		}
		for _, code := range questionsKinds {
			for _, label := range questionsKinds {
				if _, err := fmt.Fprintf(w, "confusion\t%s\t%s\t%s\t%d\n", part.name, code, label, part.counts.Confusion[code][label]); err != nil {
					return err
				}
			}
		}
		for _, kind := range []string{"scope_change", "environment", "continue"} {
			score := part.counts.Criterion1.Kinds[kind]
			if _, err := fmt.Fprintf(w, "criterion1\t%s\t%s\tprecision\t%d\t%d\t%.4f\trecall\t%d\t%d\t%.4f\t%s\n", part.name, kind, score.Precision.Count, score.Precision.Total, score.Precision.Share, score.Recall.Count, score.Recall.Total, score.Recall.Share, score.Status); err != nil {
				return err
			}
		}
		c2 := part.counts.Criterion2
		if _, err := fmt.Fprintf(w, "criterion1\t%s\t%s\ncriterion2\t%s\tyes\t%d\t%d\t%.4f\tno_or_unclear\t%d\t%d\t%.4f\t%s\n", part.name, part.counts.Criterion1.Status, part.name, c2.Yes.Count, c2.Yes.Total, c2.Yes.Share, c2.NoUnclear.Count, c2.NoUnclear.Total, c2.NoUnclear.Share, c2.Status); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "answer_agreement\t%s\t%d\t%d\t%.4f\n", part.name, part.counts.Agreement.Count, part.counts.Agreement.Total, part.counts.Agreement.Share); err != nil {
			return err
		}
		if part.counts.UnavailableCalls != nil {
			firm, err := json.Marshal(part.counts.FirmByCodeKind)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "units_summary\t%s\tunavailable_calls\t%d\tquestions_excluded\t%d\tretrieval_kept\t%d\tretrieval_dropped\t%d\tretrieval_none\t%d\tunits_min\t%d\tunits_median\t%s\tunits_max\t%d\tunits_at_threshold_min\t%d\tunits_at_threshold_median\t%s\tunits_at_threshold_max\t%d\twall_time_ms\t%d\tfirm_by_code_kind\t%s\n", part.name, *part.counts.UnavailableCalls, *part.counts.QuestionsExcluded, part.counts.Retrieval["kept"], part.counts.Retrieval["dropped"], part.counts.Retrieval["none"], part.counts.UnitsDistribution.Min, strconv.FormatFloat(part.counts.UnitsDistribution.Median, 'f', -1, 64), part.counts.UnitsDistribution.Max, part.counts.UnitsAtThresholdDistribution.Min, strconv.FormatFloat(part.counts.UnitsAtThresholdDistribution.Median, 'f', -1, 64), part.counts.UnitsAtThresholdDistribution.Max, *part.counts.WallTimeMs, firm); err != nil {
				return err
			}
		}
	}
	return nil
}
