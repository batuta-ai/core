package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/questions"
	"github.com/batuta-ai/core/routing"
)

const questionsUsage = "usage: batuta judge questions build --journal <dir> [--journal <dir>...] --out <file> | sheet --corpus <file> --out <file> | labels --corpus <file> --sheet <file> [--json] | bench --corpus <file> --sheet <file> [--split calibrate|test] [--threshold <n>] [--config <path>] [--workspace <dir>] [--base-url <url>] [--json]"
const questionsSheetHeader = "id\tkind\tanswer_in_passage\tnote\tcode_kind\tquestion\tpassage"

var questionsKinds = []string{"scope_change", "environment", "continue", "other"}
var questionsSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type questionCorpusRecord struct {
	ID           string   `json:"id"`
	Repo         string   `json:"repo"`
	Delivery     string   `json:"delivery"`
	PlanSlug     string   `json:"plan_slug"`
	Task         string   `json:"task"`
	Execution    int      `json:"execution"`
	Question     string   `json:"question"`
	Answer       string   `json:"answer"`
	Scope        []string `json:"scope"`
	CodeKind     string   `json:"code_kind"`
	Passage      string   `json:"passage"`
	PassageFound bool     `json:"passage_found"`
	PlanFound    bool     `json:"plan_found"`
	Split        string   `json:"split"`
}

type questionCorpusSummary struct {
	Journals    int            `json:"journals"`
	Questions   int            `json:"questions"`
	WithAnswer  int            `json:"with_answer"`
	WithoutPlan int            `json:"without_plan"`
	ByCodeKind  map[string]int `json:"by_code_kind"`
	BySplit     map[string]int `json:"by_split"`
}

type questionsCorpus struct {
	records []questionCorpusRecord
	summary questionCorpusSummary
}

func runJudgeQuestions(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(questionsUsage)
	}
	switch args[0] {
	case "build":
		return runQuestionsBuild(args[1:])
	case "sheet":
		return runQuestionsSheet(args[1:])
	case "labels":
		return runQuestionsLabels(args[1:], stdout)
	case "bench":
		return runQuestionsBench(args[1:], stdout)
	default:
		return fmt.Errorf("unknown questions form %q; %s", args[0], questionsUsage)
	}
}

func runQuestionsBuild(args []string) error {
	flags := flag.NewFlagSet("judge questions build", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var dirs multiFlag
	flags.Var(&dirs, "journal", "journal directory")
	out := flags.String("out", "", "corpus path")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w; %s", err, questionsUsage)
	}
	if flags.NArg() != 0 || len(dirs) == 0 || *out == "" {
		return errors.New(questionsUsage)
	}
	corpus, err := buildQuestionsCorpus(dirs)
	if err != nil {
		return err
	}
	file, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	for _, record := range corpus.records {
		if err := enc.Encode(record); err != nil {
			return err
		}
	}
	return enc.Encode(map[string]any{"summary": corpus.summary})
}

func buildQuestionsCorpus(dirs []string) (questionsCorpus, error) {
	corpus := questionsCorpus{summary: questionCorpusSummary{ByCodeKind: map[string]int{}, BySplit: map[string]int{}}}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			base := filepath.Dir(path)
			if filepath.Base(base) != "journal" || filepath.Base(filepath.Dir(base)) != ".batuta" {
				return fmt.Errorf("%s: journal is not under .batuta/journal", path)
			}
			root := filepath.Dir(filepath.Dir(base))
			records, err := readJournal(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			corpus.summary.Journals++
			built, err := questionsFromJournal(root, strings.TrimSuffix(entry.Name(), ".jsonl"), records)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			for _, record := range built {
				corpus.records = append(corpus.records, record)
				corpus.summary.Questions++
				if record.Answer != "" {
					corpus.summary.WithAnswer++
				}
				if !record.PlanFound {
					corpus.summary.WithoutPlan++
				}
				corpus.summary.ByCodeKind[record.CodeKind]++
				corpus.summary.BySplit[record.Split]++
			}
			return nil
		})
		if err != nil {
			return questionsCorpus{}, fmt.Errorf("judge questions build: %s: %w", dir, err)
		}
	}
	return corpus, nil
}

type journalQuestion struct {
	task            string
	execution       int
	requestID, text string
}
type answerKey struct {
	task      string
	execution int
}

func questionsFromJournal(root, delivery string, records []journal.Record) ([]questionCorpusRecord, error) {
	if len(records) == 0 || records[0].Kind != loop.KindOpened {
		return nil, errors.New("missing delivery_opened record")
	}
	opened, err := questionsDetail(records[0].Detail)
	if err != nil {
		return nil, fmt.Errorf("delivery_opened: %w", err)
	}
	slug, err := questionsString(opened, "slug")
	if err != nil {
		return nil, fmt.Errorf("delivery_opened: %w", err)
	}
	if !questionsSlug.MatchString(slug) {
		return nil, errors.New("delivery_opened: slug: invalid value")
	}
	if _, err := questionsString(opened, "plan_path"); err != nil {
		return nil, fmt.Errorf("delivery_opened: %w", err)
	}
	plan, planOK, err := questionsPlan(root, slug)
	if err != nil {
		return nil, err
	}
	var queued []journalQuestion
	answers := map[answerKey]string{}
	for i, record := range records {
		if record.Kind != loop.KindQuestion && record.Kind != loop.KindAnswer {
			continue
		}
		if record.TaskID == "" {
			return nil, fmt.Errorf("record %d: task_id: empty", i+1)
		}
		detail, err := questionsDetail(record.Detail)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i+1, err)
		}
		execution, err := questionsInt(detail, "execution")
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i+1, err)
		}
		if execution < 1 {
			return nil, fmt.Errorf("record %d: execution: must be positive", i+1)
		}
		text, err := questionsString(detail, "question")
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i+1, err)
		}
		key := answerKey{record.TaskID, execution}
		if record.Kind == loop.KindAnswer {
			answer, err := questionsString(detail, "answer")
			if err != nil {
				return nil, fmt.Errorf("record %d: %w", i+1, err)
			}
			answers[key] = answer
			continue
		}
		requestID, err := questionsString(detail, "request_id")
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i+1, err)
		}
		if requestID == "" {
			return nil, fmt.Errorf("record %d: request_id: empty", i+1)
		}
		for _, field := range []string{"run_id", "ask_path"} {
			if _, err := questionsString(detail, field); err != nil {
				return nil, fmt.Errorf("record %d: %w", i+1, err)
			}
		}
		queued = append(queued, journalQuestion{record.TaskID, execution, requestID, text})
	}
	hash := sha256.Sum256([]byte(delivery))
	split := "test"
	if hash[0]%2 == 0 {
		split = "calibrate"
	}
	result := make([]questionCorpusRecord, 0, len(queued))
	for _, q := range queued {
		r := questionCorpusRecord{ID: fmt.Sprintf("%s/%s/e%d/q%s", delivery, q.task, q.execution, q.requestID), Repo: filepath.Base(root), Delivery: delivery, PlanSlug: slug, Task: q.task, Execution: q.execution, Question: questionsBound(q.text), Answer: questionsBound(answers[answerKey{q.task, q.execution}]), Scope: []string{}, Split: split}
		if planOK {
			for _, task := range plan.Tasks {
				if task.ID != q.task {
					continue
				}
				r.PlanFound = true
				r.Scope = append(r.Scope, task.Scope...)
				r.Passage = questionsBound(questions.Passage(plan, task))
				r.PassageFound = r.Passage != ""
				break
			}
		}
		r.CodeKind = questions.Kind(q.text, r.Scope)
		result = append(result, r)
	}
	return result, nil
}

func questionsPlan(root, slug string) (routing.Plan, bool, error) {
	for _, path := range []string{filepath.Join(root, ".batuta", "plans", slug+".md"), filepath.Join(root, ".batuta", "plans", "done", slug+".md")} {
		payload, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return routing.Plan{}, false, err
		}
		plan, err := routing.ParsePlan(slug, payload)
		if err != nil {
			return routing.Plan{}, false, fmt.Errorf("%s: %w", path, err)
		}
		return plan, true, nil
	}
	return routing.Plan{}, false, nil
}

func questionsBound(s string) string {
	if len(s) > 4000 {
		s = s[:4000]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func questionsDetail(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var detail map[string]json.RawMessage
	if err := json.Unmarshal(raw, &detail); err != nil || detail == nil {
		return nil, errors.New("detail: expected object")
	}
	return detail, nil
}

func questionsString(raw map[string]json.RawMessage, field string) (string, error) {
	value, ok := raw[field]
	if !ok || bytes.Equal(value, []byte("null")) {
		return "", fmt.Errorf("%s: missing or null", field)
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return "", fmt.Errorf("%s: expected string", field)
	}
	return s, nil
}

func questionsInt(raw map[string]json.RawMessage, field string) (int, error) {
	value, ok := raw[field]
	if !ok || bytes.Equal(value, []byte("null")) {
		return 0, fmt.Errorf("%s: missing or null", field)
	}
	var n int
	if err := json.Unmarshal(value, &n); err != nil {
		return 0, fmt.Errorf("%s: expected integer", field)
	}
	return n, nil
}

func questionsBool(raw map[string]json.RawMessage, field string) (bool, error) {
	value, ok := raw[field]
	if !ok || bytes.Equal(value, []byte("null")) {
		return false, fmt.Errorf("%s: missing or null", field)
	}
	var b bool
	if err := json.Unmarshal(value, &b); err != nil {
		return false, fmt.Errorf("%s: expected bool", field)
	}
	return b, nil
}

func questionsStrings(raw map[string]json.RawMessage, field string) ([]string, error) {
	value, ok := raw[field]
	if !ok || bytes.Equal(value, []byte("null")) {
		return nil, fmt.Errorf("%s: missing or null", field)
	}
	var values []string
	if err := json.Unmarshal(value, &values); err != nil || values == nil {
		return nil, fmt.Errorf("%s: expected string array", field)
	}
	for _, entry := range values {
		if entry == "" {
			return nil, fmt.Errorf("%s: empty entry", field)
		}
	}
	return values, nil
}

func readQuestionsCorpus(path string) (questionsCorpus, error) {
	file, err := os.Open(path)
	if err != nil {
		return questionsCorpus{}, err
	}
	defer file.Close()
	var result questionsCorpus
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	line, summaryLine := 0, 0
	for scanner.Scan() {
		line++
		payload := bytes.TrimSpace(scanner.Bytes())
		if len(payload) == 0 {
			continue
		}
		if summaryLine != 0 {
			return questionsCorpus{}, fmt.Errorf("line %d: content after summary on line %d", line, summaryLine)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(payload, &raw); err != nil || raw == nil {
			return questionsCorpus{}, fmt.Errorf("line %d: expected JSON object", line)
		}
		if summaryRaw, ok := raw["summary"]; ok {
			if bytes.Equal(summaryRaw, []byte("null")) {
				return questionsCorpus{}, fmt.Errorf("line %d: summary: null", line)
			}
			var summaryFields map[string]json.RawMessage
			if err := json.Unmarshal(summaryRaw, &summaryFields); err != nil || summaryFields == nil {
				return questionsCorpus{}, fmt.Errorf("line %d: summary: expected object", line)
			}
			count, err := questionsInt(summaryFields, "questions")
			if err != nil {
				return questionsCorpus{}, fmt.Errorf("line %d: summary: %w", line, err)
			}
			if count != len(result.records) {
				return questionsCorpus{}, fmt.Errorf("line %d: summary questions=%d does not match %d records", line, count, len(result.records))
			}
			if err := json.Unmarshal(summaryRaw, &result.summary); err != nil {
				return questionsCorpus{}, fmt.Errorf("line %d: summary: %w", line, err)
			}
			summaryLine = line
			continue
		}
		for _, field := range []string{"id", "repo", "delivery", "plan_slug", "task", "question", "answer", "code_kind", "passage", "split"} {
			if _, err := questionsString(raw, field); err != nil {
				return questionsCorpus{}, fmt.Errorf("line %d: %w", line, err)
			}
		}
		if _, err := questionsInt(raw, "execution"); err != nil {
			return questionsCorpus{}, fmt.Errorf("line %d: %w", line, err)
		}
		if _, err := questionsStrings(raw, "scope"); err != nil {
			return questionsCorpus{}, fmt.Errorf("line %d: %w", line, err)
		}
		for _, field := range []string{"passage_found", "plan_found"} {
			if _, err := questionsBool(raw, field); err != nil {
				return questionsCorpus{}, fmt.Errorf("line %d: %w", line, err)
			}
		}
		var record questionCorpusRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			return questionsCorpus{}, fmt.Errorf("line %d: %w", line, err)
		}
		if record.ID == "" {
			return questionsCorpus{}, fmt.Errorf("line %d: id: empty", line)
		}
		if record.Execution < 1 {
			return questionsCorpus{}, fmt.Errorf("line %d: execution: must be positive", line)
		}
		if !questionsValidKind(record.CodeKind) {
			return questionsCorpus{}, fmt.Errorf("line %d: code_kind: invalid value", line)
		}
		if record.Split != "calibrate" && record.Split != "test" {
			return questionsCorpus{}, fmt.Errorf("line %d: split: invalid value", line)
		}
		result.records = append(result.records, record)
	}
	if err := scanner.Err(); err != nil {
		return questionsCorpus{}, err
	}
	if summaryLine == 0 {
		return questionsCorpus{}, errors.New("no trailing summary object")
	}
	return result, nil
}

func runQuestionsSheet(args []string) error {
	flags := flag.NewFlagSet("judge questions sheet", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	corpusPath := flags.String("corpus", "", "corpus path")
	out := flags.String("out", "", "sheet path")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w; %s", err, questionsUsage)
	}
	if flags.NArg() != 0 || *corpusPath == "" || *out == "" {
		return errors.New(questionsUsage)
	}
	corpus, err := readQuestionsCorpus(*corpusPath)
	if err != nil {
		return err
	}
	file, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := fmt.Fprintln(file, questionsSheetHeader); err != nil {
		return err
	}
	for _, r := range corpus.records {
		if _, err := fmt.Fprintf(file, "%s\t\t\t\t%s\t%s\t%s\n", questionsCell(r.ID), questionsCell(r.CodeKind), questionsCell(r.Question), questionsCell(r.Passage)); err != nil {
			return err
		}
	}
	return nil
}

func questionsCell(s string) string {
	return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(s)
}

type questionsLabel struct{ Kind, AnswerInPassage, Note string }
type questionsCount struct {
	Count int     `json:"count"`
	Total int     `json:"total"`
	Share float64 `json:"share"`
}
type questionsLabelsReport struct {
	Questions       int                       `json:"questions"`
	Confusion       map[string]map[string]int `json:"confusion"`
	Precision       map[string]questionsCount `json:"precision"`
	Recall          map[string]questionsCount `json:"recall"`
	AnswerInPassage map[string]map[string]int `json:"answer_in_passage"`
}

func runQuestionsLabels(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("judge questions labels", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	corpusPath := flags.String("corpus", "", "corpus path")
	sheetPath := flags.String("sheet", "", "label sheet")
	asJSON := flags.Bool("json", false, "JSON output")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w; %s", err, questionsUsage)
	}
	if flags.NArg() != 0 || *corpusPath == "" || *sheetPath == "" {
		return errors.New(questionsUsage)
	}
	corpus, err := readQuestionsCorpus(*corpusPath)
	if err != nil {
		return err
	}
	labels, err := readQuestionsLabels(corpus.records, *sheetPath)
	if err != nil {
		return err
	}
	report := questionsLabelCounts(corpus.records, labels)
	if *asJSON {
		return json.NewEncoder(stdout).Encode(report)
	}
	return printQuestionsLabelCounts(stdout, report)
}

func readQuestionsLabels(records []questionCorpusRecord, path string) (map[string]questionsLabel, error) {
	ids := map[string]bool{}
	for _, r := range records {
		if ids[r.ID] {
			return nil, fmt.Errorf("corpus id %s repeated", r.ID)
		}
		ids[r.ID] = true
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	labels := map[string]questionsLabel{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	row := 0
	for scanner.Scan() {
		row++
		cells := strings.Split(scanner.Text(), "\t")
		if row == 1 {
			if scanner.Text() != questionsSheetHeader {
				return nil, fmt.Errorf("row 1: wrong header")
			}
			continue
		}
		if len(cells) != 7 {
			return nil, fmt.Errorf("row %d: expected seven cells", row)
		}
		id := cells[0]
		if !ids[id] {
			return nil, fmt.Errorf("row %d: id %q not in corpus", row, id)
		}
		if _, exists := labels[id]; exists {
			return nil, fmt.Errorf("row %d: id %q repeated", row, id)
		}
		if !questionsValidKind(cells[1]) {
			return nil, fmt.Errorf("row %d: kind %q invalid", row, cells[1])
		}
		if cells[2] != "yes" && cells[2] != "no" && cells[2] != "unclear" {
			return nil, fmt.Errorf("row %d: answer_in_passage %q invalid", row, cells[2])
		}
		labels[id] = questionsLabel{cells[1], cells[2], cells[3]}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if row == 0 {
		return nil, errors.New("row 1: missing header")
	}
	for _, r := range records {
		if _, ok := labels[r.ID]; !ok {
			return nil, fmt.Errorf("row %d: corpus id %q missing", row+1, r.ID)
		}
	}
	return labels, nil
}

func questionsValidKind(kind string) bool {
	for _, k := range questionsKinds {
		if kind == k {
			return true
		}
	}
	return false
}

func questionsLabelCounts(records []questionCorpusRecord, labels map[string]questionsLabel) questionsLabelsReport {
	report := questionsLabelsReport{Questions: len(records), Confusion: map[string]map[string]int{}, Precision: map[string]questionsCount{}, Recall: map[string]questionsCount{}, AnswerInPassage: map[string]map[string]int{}}
	for _, k := range questionsKinds {
		report.Confusion[k] = map[string]int{}
		report.AnswerInPassage[k] = map[string]int{}
	}
	for _, r := range records {
		label := labels[r.ID]
		report.Confusion[r.CodeKind][label.Kind]++
		report.AnswerInPassage[label.Kind][label.AnswerInPassage]++
	}
	for _, k := range questionsKinds {
		correct, predicted, actual := report.Confusion[k][k], 0, 0
		for _, j := range questionsKinds {
			predicted += report.Confusion[k][j]
			actual += report.Confusion[j][k]
		}
		p, rec := questionsCount{Count: correct, Total: predicted}, questionsCount{Count: correct, Total: actual}
		if predicted > 0 {
			p.Share = float64(correct) / float64(predicted)
		}
		if actual > 0 {
			rec.Share = float64(correct) / float64(actual)
		}
		report.Precision[k], report.Recall[k] = p, rec
	}
	return report
}

func printQuestionsLabelCounts(w io.Writer, report questionsLabelsReport) error {
	if _, err := fmt.Fprintln(w, "code_kind\tkind\tcount"); err != nil {
		return err
	}
	for _, code := range questionsKinds {
		for _, label := range questionsKinds {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%d\n", code, label, report.Confusion[code][label]); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(w, "measure\tkind\tcount\ttotal\tshare"); err != nil {
		return err
	}
	for _, k := range questionsKinds {
		for _, metric := range []struct {
			name  string
			count questionsCount
		}{{"precision", report.Precision[k]}, {"recall", report.Recall[k]}} {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n", metric.name, k, metric.count.Count, metric.count.Total, strconv.FormatFloat(metric.count.Share, 'f', 4, 64)); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(w, "kind\tanswer_in_passage\tcount"); err != nil {
		return err
	}
	for _, k := range questionsKinds {
		for _, answer := range []string{"yes", "no", "unclear"} {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%d\n", k, answer, report.AnswerInPassage[k][answer]); err != nil {
				return err
			}
		}
	}
	return nil
}
