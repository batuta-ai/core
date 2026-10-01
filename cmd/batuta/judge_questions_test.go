package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func questionsFixture(t *testing.T, slug string, events ...journal.Record) (string, string) {
	t.Helper()
	root := t.TempDir()
	planDir := filepath.Join(root, ".batuta", "plans")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plan, err := os.ReadFile("testdata/questions/plan.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "demo.md"), plan, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	delivery := "demo-a1b2"
	opened, _ := json.Marshal(map[string]any{"slug": slug, "plan_path": filepath.Join(planDir, slug+".md")})
	if _, err := store.Append(delivery, journal.Record{Kind: loop.KindOpened, Detail: opened}); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if _, err := store.Append(delivery, event); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, ".batuta", "journal"), root
}

func questionEvent(kind journal.Kind, task string, detail map[string]any) journal.Record {
	data, _ := json.Marshal(detail)
	return journal.Record{Kind: kind, TaskID: task, Detail: data}
}

func fixtureQuestion(text string, execution int, request string) journal.Record {
	return questionEvent(loop.KindQuestion, "task_1", map[string]any{"execution": execution, "question": text, "request_id": request, "run_id": "run-1", "ask_path": "ask.md"})
}

func buildFixture(t *testing.T, slug string, events ...journal.Record) ([]questionCorpusRecord, questionCorpusSummary) {
	t.Helper()
	journalDir, root := questionsFixture(t, slug, events...)
	out := filepath.Join(root, "corpus.jsonl")
	if err := runJudge([]string{"questions", "build", "--journal", journalDir, "--out", out}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	result, err := readQuestionsCorpus(out)
	if err != nil {
		t.Fatal(err)
	}
	return result.records, result.summary
}

func TestQuestionsBuildOffline(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "corpus.jsonl")
	err := runJudge([]string{"questions", "build", "--journal", "testdata/questions/workspace/.batuta/journal", "--out", out}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := readQuestionsCorpus(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus.records) != 1 || corpus.records[0].Passage == "" || corpus.records[0].Answer != "JSON" || corpus.summary.Journals != 1 {
		t.Fatalf("corpus = %+v", corpus)
	}
}

func TestQuestionsBuildFields(t *testing.T) {
	t.Parallel()
	answer := questionEvent(loop.KindAnswer, "task_1", map[string]any{"execution": 1, "question": "Which format?", "answer": "JSON"})
	records, _ := buildFixture(t, "demo", fixtureQuestion("Which format?", 1, "q1"), answer)
	r := records[0]
	if r.ID != "demo-a1b2/task_1/e1/qq1" || r.Repo == "" || r.Delivery != "demo-a1b2" || r.PlanSlug != "demo" || r.Task != "task_1" || r.Execution != 1 || r.Question != "Which format?" || r.Answer != "JSON" || len(r.Scope) != 1 || r.CodeKind != "other" || r.Passage == "" || !r.PassageFound || !r.PlanFound || r.Split == "" {
		t.Fatalf("record = %+v", r)
	}
}

func TestQuestionsBuildNotFound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, slug, task string }{{"plan", "missing", "task_1"}, {"task", "demo", "task_9"}} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := fixtureQuestion("src/outside.go?", 1, "q1")
			q.TaskID = tc.task
			records, summary := buildFixture(t, tc.slug, q)
			if records[0].PlanFound || records[0].Passage != "" || records[0].CodeKind != "scope_change" || summary.WithoutPlan != 1 {
				t.Fatalf("record=%+v summary=%+v", records[0], summary)
			}
		})
	}
}

func TestQuestionsBuildSplit(t *testing.T) {
	t.Parallel()
	records, _ := buildFixture(t, "demo", fixtureQuestion("Which format?", 1, "q1"))
	hash := sha256.Sum256([]byte("demo-a1b2"))
	want := "test"
	if hash[0]%2 == 0 {
		want = "calibrate"
	}
	if records[0].Split != want {
		t.Fatalf("split = %s, want %s", records[0].Split, want)
	}
}

func TestQuestionsBuildBounds(t *testing.T) {
	t.Parallel()
	answer := questionEvent(loop.KindAnswer, "task_1", map[string]any{"execution": 1, "question": "x", "answer": strings.Repeat("b", 5000)})
	records, _ := buildFixture(t, "demo", fixtureQuestion(strings.Repeat("a", 5000), 1, "q1"), answer)
	if len(records[0].Question) != 4000 || len(records[0].Answer) != 4000 || len(records[0].Passage) > 4000 {
		t.Fatalf("bounds = %d/%d/%d", len(records[0].Question), len(records[0].Answer), len(records[0].Passage))
	}
}

func TestQuestionsBuildSummary(t *testing.T) {
	t.Parallel()
	records, summary := buildFixture(t, "demo", fixtureQuestion("Docker unavailable", 1, "q1"), fixtureQuestion("Which format?", 2, "q2"))
	if len(records) != 2 || summary.Journals != 1 || summary.Questions != 2 || summary.WithAnswer != 0 || summary.WithoutPlan != 0 || summary.ByCodeKind["environment"] != 1 || summary.ByCodeKind["other"] != 1 || summary.BySplit[records[0].Split] != 2 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestQuestionsSheet(t *testing.T) {
	t.Parallel()
	dir, root := questionsFixture(t, "demo", fixtureQuestion("Which\tformat?", 1, "q1"))
	corpus, sheet := filepath.Join(root, "corpus.jsonl"), filepath.Join(root, "sheet.tsv")
	if err := runJudge([]string{"questions", "build", "--journal", dir, "--out", corpus}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runJudge([]string{"questions", "sheet", "--corpus", corpus, "--out", sheet}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sheet)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "id\tkind\tanswer_in_passage\tnote\tcode_kind\tquestion\tpassage\n") || !strings.Contains(string(data), "\t\t\tother\tWhich format?") {
		t.Fatalf("sheet = %q", data)
	}
}

func TestQuestionsLabelsRejects(t *testing.T) {
	t.Parallel()
	for _, row := range []string{"wrong\tother\tno\t\tother\tq\tp", "id\tbad\tno\t\tother\tq\tp", "id\tother\tbad\t\tother\tq\tp", "id\tother\tno"} {
		row := row
		t.Run(row, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			corpus := filepath.Join(root, "c.jsonl")
			sheet := filepath.Join(root, "s.tsv")
			writeCorpusFixture(t, corpus)
			os.WriteFile(sheet, []byte(questionsSheetHeader+"\n"+row+"\n"), 0o600)
			err := runJudge([]string{"questions", "labels", "--corpus", corpus, "--sheet", sheet}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "row 2") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("missing corpus id", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		corpus, sheet := filepath.Join(root, "c.jsonl"), filepath.Join(root, "s.tsv")
		writeCorpusFixture(t, corpus)
		if err := os.WriteFile(sheet, []byte(questionsSheetHeader+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runJudge([]string{"questions", "labels", "--corpus", corpus, "--sheet", sheet}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "row 2") || !strings.Contains(err.Error(), "id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("repeated sheet id", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		corpus, sheet := filepath.Join(root, "c.jsonl"), filepath.Join(root, "s.tsv")
		writeCorpusFixture(t, corpus)
		row := "id\tother\tno\t\tother\tq\tp\n"
		if err := os.WriteFile(sheet, []byte(questionsSheetHeader+"\n"+row+row), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runJudge([]string{"questions", "labels", "--corpus", corpus, "--sheet", sheet}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "row 3") {
			t.Fatalf("err = %v", err)
		}
	})
}

func writeCorpusFixture(t *testing.T, path string) {
	t.Helper()
	r := questionCorpusRecord{ID: "id", Repo: "repo", Delivery: "delivery", PlanSlug: "demo", Task: "task_1", Execution: 1, Question: "q", Answer: "", Scope: []string{}, CodeKind: "other", Passage: "p", PassageFound: true, PlanFound: true, Split: "test"}
	var b bytes.Buffer
	json.NewEncoder(&b).Encode(r)
	json.NewEncoder(&b).Encode(map[string]any{"summary": questionCorpusSummary{Questions: 1}})
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionsLabelsCounts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	corpus := filepath.Join(root, "c.jsonl")
	sheet := filepath.Join(root, "s.tsv")
	writeCorpusFixture(t, corpus)
	if err := os.WriteFile(sheet, []byte(questionsSheetHeader+"\nid\tother\tyes\tnote\tother\tq\tp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{false, true} {
		var out bytes.Buffer
		args := []string{"questions", "labels", "--corpus", corpus, "--sheet", sheet}
		if asJSON {
			args = append(args, "--json")
		}
		if err := runJudge(args, &out, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "other") || !strings.Contains(out.String(), "yes") {
			t.Fatalf("output = %q", out.String())
		}
		if asJSON {
			var report questionsLabelsReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.Confusion["other"]["other"] != 1 || report.Precision["other"].Share != 1 || report.Recall["other"].Share != 1 || report.AnswerInPassage["other"]["yes"] != 1 {
				t.Fatalf("report = %+v", report)
			}
		}
	}
}

func TestQuestionsReaderStrict(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"id", "question", "passage", "code_kind", "split", "repo", "delivery", "plan_slug", "task", "execution", "answer", "scope", "passage_found", "plan_found"} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "c.jsonl")
			var r map[string]any
			data, _ := json.Marshal(questionCorpusRecord{ID: "id", Repo: "r", Delivery: "d", PlanSlug: "p", Task: "task_1", Execution: 1, Question: "q", Answer: "", Scope: []string{}, CodeKind: "other", Passage: "p", PassageFound: true, PlanFound: true, Split: "test"})
			json.Unmarshal(data, &r)
			delete(r, field)
			data, _ = json.Marshal(r)
			os.WriteFile(path, append(data, []byte("\n{\"summary\":{\"questions\":1}}\n")...), 0o600)
			_, err := readQuestionsCorpus(path)
			if err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), field) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	for _, field := range []string{"id", "question", "passage", "code_kind", "split"} {
		for _, value := range []any{nil, 3} {
			field, value := field, value
			t.Run(field+"/null-or-number", func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				path := filepath.Join(root, "c.jsonl")
				var r map[string]any
				data, _ := json.Marshal(questionCorpusRecord{ID: "id", Repo: "r", Delivery: "d", PlanSlug: "p", Task: "task_1", Execution: 1, Question: "q", Answer: "", Scope: []string{}, CodeKind: "other", Passage: "p", PassageFound: true, PlanFound: true, Split: "test"})
				json.Unmarshal(data, &r)
				r[field] = value
				data, _ = json.Marshal(r)
				if err := os.WriteFile(path, append(data, []byte("\n{\"summary\":{\"questions\":1}}\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
				_, err := readQuestionsCorpus(path)
				if err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), field) {
					t.Fatalf("err = %v", err)
				}
			})
		}
	}
}

func TestQuestionsBuildOutIsInput(t *testing.T) {
	t.Parallel()
	journalDir, root := questionsFixture(t, "demo", fixtureQuestion("Which format?", 1, "q1"))
	input := filepath.Join(journalDir, "demo-a1b2.jsonl")
	before, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.jsonl")
	if err := os.Symlink(input, link); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{input, link} {
		err := runJudge([]string{"questions", "build", "--journal", journalDir, "--out", out}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "may not be an input") {
			t.Fatalf("out=%s err=%v", out, err)
		}
		after, err := os.ReadFile(input)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("out=%s journal changed: err=%v", out, err)
		}
	}
}

func TestQuestionsSheetOutIsInput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	corpus := filepath.Join(root, "c.jsonl")
	writeCorpusFixture(t, corpus)
	before, err := os.ReadFile(corpus)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.jsonl")
	if err := os.Symlink(corpus, link); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{corpus, link} {
		err := runJudge([]string{"questions", "sheet", "--corpus", corpus, "--out", out}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "may not be an input") {
			t.Fatalf("out=%s err=%v", out, err)
		}
		after, err := os.ReadFile(corpus)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("out=%s corpus changed: err=%v", out, err)
		}
	}
}

func TestQuestionsLabelsContextMismatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ cell, row string }{
		{"code_kind", "id\tother\tno\t\tenvironment\tq\tp"},
		{"question", "id\tother\tno\t\tother\tchanged\tp"},
		{"passage", "id\tother\tno\t\tother\tq\tchanged"},
	} {
		tc := tc
		t.Run(tc.cell, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			corpus, sheet := filepath.Join(root, "c.jsonl"), filepath.Join(root, "s.tsv")
			writeCorpusFixture(t, corpus)
			if err := os.WriteFile(sheet, []byte(questionsSheetHeader+"\n"+tc.row+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, form := range []string{"labels", "bench"} {
				err := runJudge([]string{"questions", form, "--corpus", corpus, "--sheet", sheet}, &bytes.Buffer{}, &bytes.Buffer{})
				if err == nil || !strings.Contains(err.Error(), "row 2") || !strings.Contains(err.Error(), tc.cell) {
					t.Fatalf("%s: err = %v", form, err)
				}
			}
		})
	}
}

func TestQuestionsLabelsContextRoundTrip(t *testing.T) {
	t.Parallel()
	dir, root := questionsFixture(t, "demo", fixtureQuestion("Which\tformat?\r\nplease", 1, "q1"))
	corpus, sheet := filepath.Join(root, "corpus.jsonl"), filepath.Join(root, "sheet.tsv")
	if err := runJudge([]string{"questions", "build", "--journal", dir, "--out", corpus}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runJudge([]string{"questions", "sheet", "--corpus", corpus, "--out", sheet}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sheet)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	cells := strings.Split(lines[1], "\t")
	cells[1], cells[2] = "other", "no"
	lines[1] = strings.Join(cells, "\t")
	if err := os.WriteFile(sheet, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runJudge([]string{"questions", "labels", "--corpus", corpus, "--sheet", sheet}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionsUsage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"questions"}, {"questions", "unknown"}, {"questions", "build"}, {"questions", "sheet"}, {"questions", "labels"}} {
		err := runJudge(args, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}
