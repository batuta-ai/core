package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
