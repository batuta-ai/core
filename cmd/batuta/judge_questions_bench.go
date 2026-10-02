package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/questions"
)

const questionsBenchDefaultThreshold = 0.9

type questionsBenchRecordResult struct {
	ID              string             `json:"id"`
	Split           string             `json:"split"`
	CodeKind        string             `json:"code_kind"`
	Kind            string             `json:"kind"`
	AnswerInPassage string             `json:"answer_in_passage"`
	Called          bool               `json:"called"`
	Option          string             `json:"option,omitempty"`
	Confidence      float64            `json:"confidence"`
	Probabilities   map[string]float64 `json:"probabilities,omitempty"`
	Status          string             `json:"status"`
	InputTokens     *int               `json:"input_tokens,omitempty"`
	Unavailable     string             `json:"unavailable,omitempty"`
	Reason          string             `json:"reason,omitempty"`
	AnswerRepeats   bool               `json:"answer_repeats_passage"`
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
	Questions   int                       `json:"questions"`
	Excluded    int                       `json:"excluded"`
	Confusion   map[string]map[string]int `json:"confusion"`
	Criterion1  questionsBenchCriterion1  `json:"criterion1"`
	Criterion2  questionsBenchCriterion2  `json:"criterion2"`
	Agreement   questionsCount            `json:"answer_agreement"`
	Calls       int                       `json:"calls"`
	Unavailable int                       `json:"unavailable"`
	InputTokens int                       `json:"input_tokens"`
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
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *corpusPath == "" || *sheetPath == "" {
		return errors.New(questionsUsage)
	}
	if *split != "" && *split != "calibrate" && *split != "test" {
		return fmt.Errorf("invalid --split %q; use calibrate or test", *split)
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
	records, summary := questionsBenchRecords(context.Background(), j, buildReason, corpus.records, labels, *split, threshold)
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
		records = append(records, questionsBenchRecordFor(ctx, j, buildReason, question, label, threshold))
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
	return records, summary
}

func questionsBenchRecordFor(ctx context.Context, j judge.Judge, buildReason string, question questionCorpusRecord, label questionsLabel, threshold float64) questionsBenchRecordResult {
	record := questionsBenchRecordResult{ID: question.ID, Split: question.Split, CodeKind: question.CodeKind, Kind: label.Kind, AnswerInPassage: label.AnswerInPassage, Status: "not_asked"}
	if question.CodeKind != "other" {
		return record
	}
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
	}
	return nil
}
