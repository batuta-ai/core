package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/batuta-ai/core/judge"
)

func questionsBenchFixture(t *testing.T, records []questionCorpusRecord, labels []questionsLabel) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"provider":"typesafe","model":"jev-test","key_env":"PATH","decisions":{"question_match":{"mode":"shadow","threshold":0.9}}}`
	if err := os.WriteFile(filepath.Join(root, ".batuta", "judge.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	corpusPath, sheetPath := filepath.Join(root, "corpus.jsonl"), filepath.Join(root, "labels.tsv")
	var corpus bytes.Buffer
	for _, record := range records {
		if err := json.NewEncoder(&corpus).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := json.NewEncoder(&corpus).Encode(map[string]any{"summary": questionCorpusSummary{Questions: len(records)}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corpusPath, corpus.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var sheet strings.Builder
	sheet.WriteString(questionsSheetHeader + "\n")
	for i, record := range records {
		label := labels[i]
		sheet.WriteString(strings.Join([]string{record.ID, label.Kind, label.AnswerInPassage, label.Note, record.CodeKind, questionsCell(record.Question), questionsCell(record.Passage)}, "\t") + "\n")
	}
	if err := os.WriteFile(sheetPath, []byte(sheet.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, corpusPath, sheetPath
}

func questionsBenchRecord(id, kind, split string) questionCorpusRecord {
	return questionCorpusRecord{ID: id, Repo: "repo", Delivery: "delivery", PlanSlug: "plan", Task: "task_1", Execution: 1, Question: "Which format?", Answer: "private recorded answer", Scope: []string{}, CodeKind: kind, Passage: "Use JSON.", PassageFound: true, PlanFound: true, Split: split}
}

func questionsBenchServer(t *testing.T, response string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	bodies := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return server, &bodies
}

func questionsBenchRun(t *testing.T, root, corpus, sheet, url string, extra ...string) (string, error) {
	t.Helper()
	args := []string{"questions", "bench", "--corpus", corpus, "--sheet", sheet, "--workspace", root, "--base-url", url}
	args = append(args, extra...)
	var out bytes.Buffer
	err := runJudge(args, &out, &bytes.Buffer{})
	return out.String(), err
}

const questionsBenchReply = `{"model":"jev-test","answers":{"answer":{"type":"choice","choice":"answered_here","confidence":0.95,"probabilities":{"answered_here":0.95,"not_addressed":0.03,"insufficient":0.02}}},"usage":{"input_tokens":17}}`

func questionsUnitReply(option string, confidence, probability float64) string {
	return fmt.Sprintf(`{"model":"jev-test","answers":{"answer":{"type":"choice","choice":%q,"confidence":%g,"probabilities":{"answered_here":%g}}},"usage":{"input_tokens":7}}`, option, confidence, probability)
}

func questionsUnitServer(t *testing.T, reply func(string) string) (*httptest.Server, *[]string) {
	t.Helper()
	var passages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State struct{ Question, Passage string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		passages = append(passages, body.State.Passage)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply(body.State.Passage)))
	}))
	t.Cleanup(server.Close)
	return server, &passages
}

func questionsUnitFixture(t *testing.T, kind, label, note string) (string, string, string) {
	t.Helper()
	r := questionsBenchRecord("u", kind, "test")
	r.Passage = "Title\nScope: Alpha\nAccept: Beta; Gamma\n\nDelta. Epsilon."
	return questionsBenchFixture(t, []questionCorpusRecord{r}, []questionsLabel{{Kind: label, AnswerInPassage: "yes", Note: note}})
}

func TestQuestionsBenchUnitsCalls(t *testing.T) {
	t.Parallel()
	root, corpus, sheet := questionsUnitFixture(t, "other", "other", "")
	server, passages := questionsUnitServer(t, func(string) string { return questionsUnitReply("not_addressed", .96, .04) })
	_, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", "units", "--json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Title\nScope: Alpha", "Title\nAccept: Beta", "Title\nAccept: Gamma", "Title\nDelta.", "Title\nEpsilon."}
	if !reflect.DeepEqual(*passages, want) {
		t.Fatalf("passages=%q want=%q", *passages, want)
	}
}

func TestQuestionsBenchAll(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"full", "units"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			records := []questionCorpusRecord{questionsBenchRecord("e", "environment", "test"), questionsBenchRecord("o", "other", "test")}
			for i := range records {
				records[i].Passage = "Title\nScope: Use JSON."
			}
			root, corpus, sheet := questionsBenchFixture(t, records, []questionsLabel{{Kind: "environment", AnswerInPassage: "no"}, {Kind: "other", AnswerInPassage: "yes"}})
			server, passages := questionsUnitServer(t, func(string) string { return questionsBenchReply })
			_, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", mode, "--json")
			if err != nil {
				t.Fatal(err)
			}
			without := len(*passages)
			*passages = nil
			_, err = questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", mode, "--all", "--json")
			if err != nil || len(*passages) != 2*without {
				t.Fatalf("err=%v without=%d with=%d", err, without, len(*passages))
			}
		})
	}
}

func TestQuestionsBenchBestUnit(t *testing.T) {
	t.Parallel()
	root, corpus, sheet := questionsUnitFixture(t, "other", "other", "")
	server, _ := questionsUnitServer(t, func(p string) string {
		switch {
		case strings.Contains(p, "Alpha"):
			return questionsUnitReply("answered_here", .91, .8)
		case strings.Contains(p, "Beta"):
			return questionsUnitReply("not_addressed", .95, .9)
		case strings.Contains(p, "Gamma"):
			return questionsUnitReply("answered_here", .99, .9)
		default:
			return questionsUnitReply("insufficient", .8, .1)
		}
	})
	out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", "units", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rec questionsBenchRecordResult
	if err := json.Unmarshal([]byte(strings.Split(out, "\n")[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.BestUnit == nil || *rec.BestUnit != 1 || rec.Option != "not_addressed" || rec.Status != "not_addressed" {
		t.Fatalf("record=%+v", rec)
	}
}

func TestQuestionsBenchUnitUnavailable(t *testing.T) {
	t.Parallel()
	root, corpus, sheet := questionsUnitFixture(t, "other", "other", "")
	server, _ := questionsUnitServer(t, func(p string) string {
		if strings.Contains(p, "Beta") {
			return `{"answers":{"wrong":{"type":"choice","choice":"answered_here","confidence":0.95}},"usage":{"input_tokens":13}}`
		}
		return questionsUnitReply("answered_here", .95, .95)
	})
	out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", "units", "--json")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("err=%v output=%s", err, out)
	}
	var rec questionsBenchRecordResult
	if err := json.Unmarshal([]byte(strings.Split(out, "\n")[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Status != "unavailable" || rec.InputTokens == nil || *rec.InputTokens != 41 || rec.UnavailableCalls == nil || *rec.UnavailableCalls != 1 {
		t.Fatalf("record=%+v", rec)
	}
}

func TestQuestionsBenchUnitMismatchUnavailable(t *testing.T) {
	t.Parallel()
	root, corpus, sheet := questionsUnitFixture(t, "other", "other", "")
	server, _ := questionsUnitServer(t, func(p string) string {
		if strings.Contains(p, "Beta") {
			return `{"answers":{"answer":{"type":"choice","choice":"answered_here","confidence":0.99,"probabilities":{"answered_here":0.99}},"extra":{"type":"choice","choice":"answered_here","confidence":0.5}},"usage":{"input_tokens":13}}`
		}
		return questionsUnitReply("not_addressed", .95, .2)
	})
	out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", "units", "--json")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("err=%v output=%s", err, out)
	}
	var rec questionsBenchRecordResult
	if err := json.Unmarshal([]byte(strings.Split(out, "\n")[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Status != "unavailable" || rec.InputTokens == nil || *rec.InputTokens != 41 || rec.UnavailableCalls == nil || *rec.UnavailableCalls != 1 {
		t.Fatalf("record=%+v", rec)
	}
	var final struct {
		Summary struct {
			UnavailableCalls *int `json:"unavailable_calls"`
		} `json:"summary"`
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil {
		t.Fatal(err)
	}
	if final.Summary.UnavailableCalls == nil || *final.Summary.UnavailableCalls != 1 {
		t.Fatalf("summary=%+v output=%s", final.Summary, out)
	}
	if rec.BestUnit == nil || *rec.BestUnit == 1 || len(rec.UnitResults) != 5 || rec.UnitResults[1].Status != "unavailable" || rec.UnitResults[1].Probability != 0 {
		t.Fatalf("record=%+v", rec)
	}
}

func TestQuestionsBenchAllUnitsUnavailable(t *testing.T) {
	t.Parallel()
	root, corpus, sheet := questionsUnitFixture(t, "other", "other", "")
	server, _ := questionsUnitServer(t, func(string) string {
		return `{"answers":{"wrong":{"type":"choice","choice":"answered_here","confidence":0.95}},"usage":{"input_tokens":7}}`
	})
	out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", "units", "--json")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("err=%v output=%s", err, out)
	}
	var rec questionsBenchRecordResult
	if err := json.Unmarshal([]byte(strings.Split(out, "\n")[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Status != "unavailable" || rec.BestUnit == nil || *rec.BestUnit != 0 || rec.UnavailableCalls == nil || *rec.UnavailableCalls != 5 {
		t.Fatalf("record=%+v", rec)
	}
	var final struct {
		Summary struct {
			UnavailableCalls  *int                      `json:"unavailable_calls"`
			QuestionsExcluded *int                      `json:"questions_excluded"`
			Criterion1        questionsBenchCriterion1 `json:"criterion1"`
			Criterion2        questionsBenchCriterion2 `json:"criterion2"`
		} `json:"summary"`
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil {
		t.Fatal(err)
	}
	if final.Summary.UnavailableCalls == nil || *final.Summary.UnavailableCalls != 5 {
		t.Fatalf("summary=%+v output=%s", final.Summary, out)
	}
	if final.Summary.QuestionsExcluded == nil || *final.Summary.QuestionsExcluded != 1 {
		t.Fatalf("summary=%+v output=%s", final.Summary, out)
	}
	if final.Summary.Criterion2.Yes.Total != 0 || final.Summary.Criterion2.NoUnclear.Total != 0 {
		t.Fatalf("criterion2=%+v want excluded", final.Summary.Criterion2)
	}
}

type scriptedQuestionsJudge struct {
	index   int
	replies []judge.Response
	errors  []error
}

func (j *scriptedQuestionsJudge) Ask(_ context.Context, _ judge.Request) (judge.Response, error) {
	i := j.index
	j.index++
	return j.replies[i], j.errors[i]
}

func TestQuestionsBenchValidUnitBeatsUnavailable(t *testing.T) {
	t.Parallel()
	question := questionsBenchRecord("o", "other", "test")
	question.Passage = "Title\n" + strings.Repeat("Scope: One\n", 11)
	judgeScript := &scriptedQuestionsJudge{replies: make([]judge.Response, 11), errors: make([]error, 11)}
	judgeScript.errors[0] = &judge.UnavailableError{Reason: judge.ReasonTimeout}
	for i := 1; i < 11; i++ {
		judgeScript.replies[i] = judge.Response{Answers: map[string]judge.Answer{"answer": {Type: judge.QuestionChoice, Choice: "not_addressed", Confidence: .95, Probabilities: map[string]float64{"answered_here": 0}}}}
	}
	record := questionsBenchUnitRecordFor(context.Background(), judgeScript, "", question, questionsLabel{Kind: "other", AnswerInPassage: "yes"}, .9, false)
	if record.BestUnit == nil || *record.BestUnit != 1 || record.Status != "not_addressed" || record.Option != "not_addressed" {
		t.Fatalf("record=%+v", record)
	}
}

func TestQuestionsBenchPartialUnavailable(t *testing.T) {
	t.Parallel()
	question := questionsBenchRecord("o", "other", "test")
	question.Passage = "Title\n" + strings.Repeat("Scope: One\n", 11)
	judgeScript := &scriptedQuestionsJudge{replies: make([]judge.Response, 11), errors: make([]error, 11)}
	judgeScript.replies[0] = judge.Response{Usage: judge.Usage{InputTokens: 13}}
	judgeScript.errors[0] = &judge.UnavailableError{Reason: judge.ReasonTimeout}
	for i := 1; i < 11; i++ {
		judgeScript.replies[i] = judge.Response{Answers: map[string]judge.Answer{"answer": {Type: judge.QuestionChoice, Choice: "answered_here", Confidence: .95, Probabilities: map[string]float64{"answered_here": .95}}}, Usage: judge.Usage{InputTokens: 7}}
	}
	record := questionsBenchUnitRecordFor(context.Background(), judgeScript, "", question, questionsLabel{Kind: "other", AnswerInPassage: "yes"}, .9, false)
	if record.Status != "firm" || record.Unavailable != "" || record.BestUnit == nil || *record.BestUnit != 1 || record.UnavailableCalls == nil || *record.UnavailableCalls != 1 || record.InputTokens == nil || *record.InputTokens != 83 || record.UnitResults[0].Probability != 0 {
		t.Fatalf("record=%+v", record)
	}
}

func TestQuestionsBenchUnitsRecord(t *testing.T) {
	t.Parallel()
	root, corpus, sheet := questionsUnitFixture(t, "other", "other", `quote “Beta”`)
	server, _ := questionsUnitServer(t, func(p string) string {
		if strings.Contains(p, "Beta") {
			return questionsUnitReply("answered_here", .95, .95)
		}
		return questionsUnitReply("not_addressed", .95, .05)
	})
	out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", "units", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rec questionsBenchRecordResult
	if err := json.Unmarshal([]byte(strings.Split(out, "\n")[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.PassageMode != "units" || rec.Units == nil || *rec.Units != 5 || rec.BestUnit == nil || *rec.BestUnit != 1 || rec.BestProbability == nil || *rec.BestProbability != .95 || rec.UnitsAtThreshold == nil || *rec.UnitsAtThreshold != 1 || rec.UnitBytesMax == nil || *rec.UnitBytesMax != 13 || rec.Calls == nil || *rec.Calls != 5 || rec.Retrieval != "kept" {
		t.Fatalf("record=%+v", rec)
	}
}

func TestQuestionsBenchUnitsJSONNoText(t *testing.T) {
	t.Parallel()
	root, corpus, sheet := questionsUnitFixture(t, "other", "other", "")
	server, _ := questionsUnitServer(t, func(string) string { return questionsUnitReply("answered_here", .95, .95) })
	out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", "units", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rec questionsBenchRecordResult
	if err := json.Unmarshal([]byte(strings.Split(out, "\n")[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.UnitResults) != 5 || strings.Contains(out, "Scope: Alpha") || strings.Contains(out, "Accept: Beta") || rec.UnitResults[0].Option != "answered_here" || rec.UnitResults[0].Confidence != .95 || rec.UnitResults[0].Probability != .95 {
		t.Fatalf("record=%+v output=%s", rec, out)
	}
}

func TestQuestionsBenchSummaryV2(t *testing.T) {
	t.Parallel()
	corpus := []questionCorpusRecord{questionsBenchRecord("o", "other", "test"), questionsBenchRecord("e", "environment", "test")}
	corpus[0].Passage = "Title\nScope: One\nAccept: Two; Three"
	corpus[1].Passage = "Title\nScope: Four"
	labels := map[string]questionsLabel{"o": {Kind: "other", AnswerInPassage: "yes", Note: `"Two"`}, "e": {Kind: "environment", AnswerInPassage: "no"}}
	response := judge.Response{Answers: map[string]judge.Answer{"answer": {Type: judge.QuestionChoice, Choice: "answered_here", Confidence: .95, Probabilities: map[string]float64{"answered_here": .95}}}, Usage: judge.Usage{InputTokens: 7}}
	records, summary := questionsBenchRecordsMode(context.Background(), staticBenchJudge{response: response}, "", corpus, labels, "", .9, "units", true)
	if len(records) != 2 || summary.Calls != 4 || summary.InputTokens != 28 || summary.UnavailableCalls == nil || *summary.UnavailableCalls != 0 || summary.QuestionsExcluded == nil || *summary.QuestionsExcluded != 0 || summary.UnitsDistribution.Min != 1 || summary.UnitsDistribution.Max != 3 || summary.UnitsDistribution.Median != 2 || summary.UnitsAtThresholdDistribution.Median != 2 || summary.WallTimeMs == nil || summary.FirmByCodeKind["environment"].No.Count != 1 || summary.FirmByCodeKind["environment"].No.Total != 1 || summary.Retrieval["dropped"] != 1 {
		t.Fatalf("records=%+v summary=%+v", records, summary)
	}
}

func TestQuestionsBenchCriterionOtherOnly(t *testing.T) {
	t.Parallel()
	corpus := []questionCorpusRecord{questionsBenchRecord("o", "other", "test"), questionsBenchRecord("e", "environment", "test")}
	for i := range corpus {
		corpus[i].Passage = "Title\nScope: One"
	}
	labels := map[string]questionsLabel{"o": {Kind: "other", AnswerInPassage: "yes"}, "e": {Kind: "environment", AnswerInPassage: "yes"}}
	response := judge.Response{Answers: map[string]judge.Answer{"answer": {Type: judge.QuestionChoice, Choice: "answered_here", Confidence: .95, Probabilities: map[string]float64{"answered_here": .95}}}}
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			t.Parallel()
			_, summary := questionsBenchRecordsMode(context.Background(), staticBenchJudge{response: response}, "", corpus, labels, "", .9, "units", all)
			if summary.Criterion2.Yes.Total != 1 || summary.Criterion2.Yes.Count != 1 {
				t.Fatalf("summary=%+v", summary)
			}
		})
	}
}

func TestQuestionsBenchLabelFreeUnits(t *testing.T) {
	t.Parallel()
	r := questionsBenchRecord("o", "other", "test")
	r.Passage = "Title\nScope: Alpha\nAccept: Beta; Gamma"
	root, corpus, sheet := questionsBenchFixture(t, []questionCorpusRecord{r}, []questionsLabel{{Kind: "other", AnswerInPassage: "yes", Note: "private label"}})
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(questionsBenchReply))
	}))
	t.Cleanup(server.Close)
	if _, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--passage", "units", "--json"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 3 {
		t.Fatalf("calls=%d", len(bodies))
	}
	for _, body := range bodies {
		if strings.Contains(body, r.Passage) || strings.Contains(body, r.Answer) || strings.Contains(body, "private label") || strings.Contains(body, "Beta; Gamma") {
			t.Fatalf("body=%s", body)
		}
	}
}

func TestQuestionsBenchCallsOnlyOther(t *testing.T) {
	t.Parallel()
	records := []questionCorpusRecord{questionsBenchRecord("e", "environment", "test"), questionsBenchRecord("s", "scope_change", "test"), questionsBenchRecord("c", "continue", "test"), questionsBenchRecord("o", "other", "test"), questionsBenchRecord("missing", "other", "test")}
	records[4].PlanFound = false
	labels := []questionsLabel{{Kind: "environment", AnswerInPassage: "no"}, {Kind: "scope_change", AnswerInPassage: "no"}, {Kind: "continue", AnswerInPassage: "no"}, {Kind: "other", AnswerInPassage: "yes"}, {Kind: "other", AnswerInPassage: "no"}}
	root, corpus, sheet := questionsBenchFixture(t, records, labels)
	server, bodies := questionsBenchServer(t, questionsBenchReply)
	out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--json")
	if err != nil || len(*bodies) != 1 || strings.Count(out, `"called":true`) != 1 || !strings.Contains(out, `"excluded":1`) {
		t.Fatalf("err=%v calls=%d output=%s", err, len(*bodies), out)
	}
}

func TestQuestionsBenchThreshold(t *testing.T) {
	t.Parallel()
	records := []questionCorpusRecord{questionsBenchRecord("o", "other", "test")}
	root, corpus, sheet := questionsBenchFixture(t, records, []questionsLabel{{Kind: "other", AnswerInPassage: "yes"}})
	server, _ := questionsBenchServer(t, questionsBenchReply)
	for _, tc := range []struct {
		name, flag, want string
		valid            bool
	}{{"default", "", `"threshold":0.9`, true}, {"zero", "0", `"threshold":0`, true}, {"one", "1", `"threshold":1`, true}, {"negative", "-0.1", "", false}, {"over", "1.1", "", false}, {"nan", "NaN", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var extra []string
			if tc.flag != "" {
				extra = []string{"--threshold", tc.flag}
			}
			out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, append(extra, "--json")...)
			if (err == nil) != tc.valid || (tc.valid && !strings.Contains(out, tc.want)) {
				t.Fatalf("err=%v output=%s", err, out)
			}
		})
	}
}

func TestQuestionsBenchRecord(t *testing.T) {
	t.Parallel()
	records := []questionCorpusRecord{questionsBenchRecord("o", "other", "test")}
	root, corpus, sheet := questionsBenchFixture(t, records, []questionsLabel{{Kind: "other", AnswerInPassage: "yes"}})
	server, _ := questionsBenchServer(t, questionsBenchReply)
	out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var record questionsBenchRecordResult
	if err := json.Unmarshal([]byte(strings.Split(out, "\n")[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record.ID != "o" || record.CodeKind != "other" || record.Kind != "other" || record.AnswerInPassage != "yes" || !record.Called || record.Option != "answered_here" || record.Confidence != .95 || record.Probabilities["answered_here"] != .95 || record.Status != "firm" || record.InputTokens == nil || *record.InputTokens != 17 {
		t.Fatalf("record=%+v", record)
	}
}

func TestQuestionsBenchUnavailable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, reply, reason string }{
		{"mismatch", `{"answers":{"wrong":{"type":"choice","choice":"answered_here","confidence":0.95}},"usage":{"input_tokens":13}}`, "answer_mismatch"},
		{"malformed", `{`, "malformed_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := []questionCorpusRecord{questionsBenchRecord("o", "other", "test")}
			root, corpus, sheet := questionsBenchFixture(t, records, []questionsLabel{{Kind: "other", AnswerInPassage: "yes"}})
			server, _ := questionsBenchServer(t, tc.reply)
			out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--json")
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 2 || !strings.Contains(out, `"unavailable":"`+tc.reason+`"`) || tc.name == "mismatch" && !strings.Contains(out, `"input_tokens":13`) {
				t.Fatalf("err=%v output=%s", err, out)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		answer judge.Answer
		err    error
		reason string
	}{
		{"mismatch no usable answer", judge.Answer{}, &judge.UnavailableError{Reason: judge.ReasonAnswerMismatch}, judge.ReasonAnswerMismatch},
		{"other error with usage", judge.Answer{Type: judge.QuestionChoice, Choice: "answered_here", Confidence: .95}, &judge.UnavailableError{Reason: judge.ReasonTimeout}, judge.ReasonTimeout},
		{"mismatch usable answer", judge.Answer{Type: judge.QuestionChoice, Choice: "answered_here", Confidence: .95}, &judge.UnavailableError{Reason: judge.ReasonAnswerMismatch}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			answers := map[string]judge.Answer{}
			if tc.answer.Type != "" {
				answers["answer"] = tc.answer
			}
			j := staticBenchJudge{response: judge.Response{Answers: answers, Usage: judge.Usage{InputTokens: 29}}, err: tc.err}
			record := questionsBenchRecordFor(context.Background(), j, "", questionsBenchRecord("o", "other", "test"), questionsLabel{Kind: "other", AnswerInPassage: "yes"}, .9)
			if record.Unavailable != tc.reason || record.InputTokens == nil || *record.InputTokens != 29 || tc.reason == "" && (record.Status != "firm" || record.Reason != judge.ReasonAnswerMismatch) {
				t.Fatalf("record=%+v", record)
			}
		})
	}
}

func TestQuestionsBenchSummaryCounts(t *testing.T) {
	t.Parallel()
	corpus := []questionCorpusRecord{questionsBenchRecord("s", "scope_change", "calibrate"), questionsBenchRecord("e", "environment", "test"), questionsBenchRecord("c", "continue", "test"), questionsBenchRecord("y", "other", "calibrate"), questionsBenchRecord("n", "other", "test")}
	labels := map[string]questionsLabel{"s": {Kind: "scope_change"}, "e": {Kind: "environment"}, "c": {Kind: "continue"}, "y": {Kind: "other", AnswerInPassage: "yes"}, "n": {Kind: "other", AnswerInPassage: "no"}}
	response := judge.Response{Answers: map[string]judge.Answer{"answer": {Type: judge.QuestionChoice, Choice: "answered_here", Confidence: .95}}, Usage: judge.Usage{InputTokens: 17}}
	records, summary := questionsBenchRecords(context.Background(), staticBenchJudge{response: response}, "", corpus, labels, "", .9)
	if len(records) != 5 || summary.Criterion1.Status != "PASS" || summary.Criterion2.Status != "reported only" || summary.Criterion2.Yes.Count != 1 || summary.Criterion2.NoUnclear.Count != 1 || summary.Calls != 2 || summary.InputTokens != 34 || summary.BySplit["calibrate"].Calls != 1 || summary.BySplit["test"].Calls != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil || !bytes.Contains(encoded, []byte(`"criterion1"`)) || !bytes.Contains(encoded, []byte(`"calls":2`)) {
		t.Fatalf("json=%s err=%v", encoded, err)
	}
}

func TestQuestionsBenchOffline(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, threshold string
		valid           bool
	}{{"default", "", true}, {"zero", "0", true}, {"negative", "-0.1", false}, {"over one", "1.1", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, corpus, sheet := questionsBenchFixture(t, []questionCorpusRecord{questionsBenchRecord("o", "other", "test")}, []questionsLabel{{Kind: "other", AnswerInPassage: "yes"}})
			if err := os.WriteFile(filepath.Join(root, ".batuta", "judge.json"), []byte(`{"provider":"off"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			var args []string
			if tc.threshold != "" {
				args = []string{"--threshold", tc.threshold}
			}
			out, err := questionsBenchRun(t, root, corpus, sheet, "", append(args, "--json")...)
			if tc.valid {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 2 || !strings.Contains(out, `"unavailable":"judge_off"`) || !strings.Contains(out, `"calls":0`) {
					t.Fatalf("err=%v output=%s", err, out)
				}
			} else if err == nil || out != "" {
				t.Fatalf("err=%v output=%s", err, out)
			}
		})
	}
}

func TestQuestionsBenchLabelFree(t *testing.T) {
	t.Parallel()
	record := questionsBenchRecord("o", "other", "test")
	root, corpus, sheet := questionsBenchFixture(t, []questionCorpusRecord{record}, []questionsLabel{{Kind: "other", AnswerInPassage: "yes", Note: "secret label note"}})
	server, bodies := questionsBenchServer(t, questionsBenchReply)
	if _, err := questionsBenchRun(t, root, corpus, sheet, server.URL); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal((*bodies)[0])
	if strings.Contains(string(encoded), record.Answer) || strings.Contains(string(encoded), "secret label note") || strings.Contains(string(encoded), "answer_in_passage") {
		t.Fatalf("request=%s", encoded)
	}
}

func TestQuestionsBenchSummary(t *testing.T) {
	t.Parallel()
	records := []questionCorpusRecord{questionsBenchRecord("s", "scope_change", "calibrate"), questionsBenchRecord("e", "environment", "test"), questionsBenchRecord("c", "continue", "test"), questionsBenchRecord("yes", "other", "calibrate"), questionsBenchRecord("no", "other", "test")}
	labels := []questionsLabel{{Kind: "scope_change", AnswerInPassage: "no"}, {Kind: "environment", AnswerInPassage: "no"}, {Kind: "continue", AnswerInPassage: "no"}, {Kind: "other", AnswerInPassage: "yes"}, {Kind: "other", AnswerInPassage: "no"}}
	root, corpus, sheet := questionsBenchFixture(t, records, labels)
	server, bodies := questionsBenchServer(t, questionsBenchReply)
	out, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--json")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var envelope struct {
		Summary questionsBenchSummary `json:"summary"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &envelope); err != nil {
		t.Fatal(err)
	}
	s := envelope.Summary
	if len(*bodies) != 2 || s.Calls != 2 || s.InputTokens != 34 || s.Criterion1.Status != "PASS" || s.Criterion2.Status != "reported only" || s.Criterion2.Yes.Total != 1 || s.Criterion2.Yes.Count != 1 || s.Criterion2.NoUnclear.Total != 1 || s.Criterion2.NoUnclear.Count != 1 || s.BySplit["calibrate"].Calls != 1 || s.BySplit["test"].Calls != 1 {
		t.Fatalf("summary=%+v", s)
	}
	for _, kind := range []string{"scope_change", "environment", "continue"} {
		if s.Criterion1.Kinds[kind].Precision.Count != 1 || s.Criterion1.Kinds[kind].Recall.Share != 1 {
			t.Fatalf("%s=%+v", kind, s.Criterion1.Kinds[kind])
		}
	}
}

func TestQuestionsBenchJSON(t *testing.T) {
	t.Parallel()
	records := []questionCorpusRecord{questionsBenchRecord("o", "other", "test")}
	root, corpus, sheet := questionsBenchFixture(t, records, []questionsLabel{{Kind: "other", AnswerInPassage: "yes"}})
	server, _ := questionsBenchServer(t, questionsBenchReply)
	jsonOut, err := questionsBenchRun(t, root, corpus, sheet, server.URL, "--json")
	if err != nil {
		t.Fatal(err)
	}
	textOut, err := questionsBenchRun(t, root, corpus, sheet, server.URL)
	if err != nil || !strings.Contains(textOut, "criterion1") || !strings.Contains(textOut, "criterion2") || !strings.Contains(textOut, "answered_here") || strings.Count(strings.TrimSpace(jsonOut), "\n") != 1 {
		t.Fatalf("err=%v text=%s json=%s", err, textOut, jsonOut)
	}
}
