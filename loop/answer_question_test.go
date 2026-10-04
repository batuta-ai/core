package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func exactAnswerFixture(t *testing.T) (string, *journal.Store, QuestionTarget) {
	t.Helper()
	root := tempDir(t)
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	const delivery, task, run, prompt = "delivery", "task_1", "delivery-task-1-e1", "Choose the behavior"
	graph := routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: task, State: routing.GraphTaskRunning,
		Attempts: []routing.GraphTaskAttempt{{Execution: 1, State: routing.GraphTaskRunning,
			ChildRunID: run, WorktreeID: "worktree", WorktreeRoot: root,
			BaseHeadSHA: strings.Repeat("b", 40), Runtime: routing.RuntimeValue{Provider: "codex", Model: "fixture", Reasoning: "high"},
		}},
	}}}
	opened, err := store.Append(delivery, panelRecord(t, KindOpened, "", now,
		openedDetail{Slug: "fixture", Workspace: root, PlanPath: ".batuta/plans/fixture.md", PlanDigest: digestString("fixture")}, graph))
	if err != nil {
		t.Fatal(err)
	}
	question := routing.TaskQuestion{RequestID: digestString("question:" + run + ":" + prompt), Prompt: prompt, ContextDigest: digestString("context")}
	if _, err := graph.RecordQuestion(task, 1, run, question, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(delivery, panelRecord(t, KindQuestion, task, now, map[string]any{"execution": 1}, graph)); err != nil {
		t.Fatal(err)
	}
	return root, store, QuestionTarget{DeliveryID: delivery, TaskID: task, Execution: 1, QuestionID: question.RequestID, OpenedDigest: opened.Digest}
}

func TestAnswerQuestionRepeatedPromptRejectsOldExecution(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		root, store, target := exactAnswerFixture(t)
		if id, err := AnswerQuestion(context.Background(), root, target, "First answer"); err != nil || id != target.DeliveryID {
			t.Fatalf("first answer: id=%q err=%v", id, err)
		}
		records := answerRecords(t, store, target.DeliveryID)
		var graph routing.DeliveryGraph
		if err := json.Unmarshal(records[len(records)-1].Graph, &graph); err != nil {
			t.Fatal(err)
		}
		task := graphTask(&graph, target.TaskID)
		first := task.Attempts[0]
		next := task.Attempts[len(task.Attempts)-1]
		if next.Execution != 2 || next.ChildRunID != first.ChildRunID {
			t.Fatalf("native continuation: %+v", next)
		}
		question := *first.Question
		question.Answer = nil
		if _, err := graph.RecordQuestion(target.TaskID, next.Execution, next.ChildRunID, question, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(target.DeliveryID, panelRecord(t, KindQuestion, target.TaskID, time.Now().UTC(), map[string]any{"execution": 2}, graph)); err != nil {
			t.Fatal(err)
		}
		before := answerRecords(t, store, target.DeliveryID)
		if id, err := AnswerQuestion(context.Background(), root, target, "First answer"); id != "" || !errors.Is(err, ErrStaleQuestion) {
			t.Fatalf("old execution accepted: id=%q err=%v", id, err)
		}
		if after := answerRecords(t, store, target.DeliveryID); len(after) != len(before) {
			t.Fatal("stale answer changed journal")
		}
		target.Execution = 2
		if id, err := AnswerQuestion(context.Background(), root, target, "Current answer"); err != nil || id != target.DeliveryID {
			t.Fatalf("current execution rejected: id=%q err=%v", id, err)
		}
	})
}

func TestAnswerQuestionRejectsInvalidTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*QuestionTarget)
	}{
		{"delivery empty", func(q *QuestionTarget) { q.DeliveryID = "" }},
		{"delivery path", func(q *QuestionTarget) { q.DeliveryID = "../delivery" }},
		{"task empty", func(q *QuestionTarget) { q.TaskID = "" }},
		{"task padded", func(q *QuestionTarget) { q.TaskID = " task_1" }},
		{"task control", func(q *QuestionTarget) { q.TaskID = "task_1\x00" }},
		{"execution zero", func(q *QuestionTarget) { q.Execution = 0 }},
		{"execution ceiling", func(q *QuestionTarget) { q.Execution = routing.MaxTaskExecutions + 1 }},
		{"question empty", func(q *QuestionTarget) { q.QuestionID = "" }},
		{"question malformed", func(q *QuestionTarget) { q.QuestionID = "not-a-digest" }},
		{"opening empty", func(q *QuestionTarget) { q.OpenedDigest = "" }},
		{"opening malformed", func(q *QuestionTarget) { q.OpenedDigest = strings.Repeat("z", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, store, target := exactAnswerFixture(t)
			before := answerRecords(t, store, target.DeliveryID)
			delivery := target.DeliveryID
			tc.change(&target)
			if id, err := AnswerQuestion(context.Background(), root, target, "Answer"); id != "" || !errors.Is(err, ErrInvalidQuestionTarget) {
				t.Fatalf("invalid target: id=%q err=%v", id, err)
			}
			if len(answerRecords(t, store, delivery)) != len(before) {
				t.Fatal("invalid target changed journal")
			}
		})
	}
}

func TestAnswerQuestionRejectsChangedIdentity(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"task", "task alias", "question", "opening", "replaced opening", "second opening", "foreign delivery", "terminal"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			root, store, target := exactAnswerFixture(t)
			before := answerRecords(t, store, target.DeliveryID)
			switch change {
			case "task":
				target.TaskID = "task_2"
			case "task alias":
				target.TaskID = "1"
			case "question":
				target.QuestionID = digestString("other question")
			case "opening":
				target.OpenedDigest = digestString("other opening")
			case "replaced opening":
				if err := os.Remove(store.Path(target.DeliveryID)); err != nil {
					t.Fatal(err)
				}
				before[0].Detail = json.RawMessage(`{"slug":"different"}`)
				copyAnswerDelivery(t, store, target.DeliveryID, before)
			case "second opening":
				last := before[len(before)-1]
				last.Kind = KindOpened
				if _, err := store.Append(target.DeliveryID, last); err != nil {
					t.Fatal(err)
				}
			case "foreign delivery":
				target.DeliveryID = "other"
				before[0].Detail = json.RawMessage(`{"slug":"different"}`)
				copyAnswerDelivery(t, store, target.DeliveryID, before)
			case "terminal":
				last := before[len(before)-1]
				last.Kind, last.Detail = KindTerminal, json.RawMessage(`{"state":"done"}`)
				if _, err := store.Append(target.DeliveryID, last); err != nil {
					t.Fatal(err)
				}
			}
			before = answerRecords(t, store, target.DeliveryID)
			if id, err := AnswerQuestion(context.Background(), root, target, "Answer"); id != "" || !errors.Is(err, ErrStaleQuestion) {
				t.Fatalf("changed %s: id=%q err=%v", change, id, err)
			}
			if len(answerRecords(t, store, target.DeliveryID)) != len(before) {
				t.Fatal("rejection changed journal")
			}
		})
	}
}

func TestAnswerQuestionCorruptJournal(t *testing.T) {
	t.Parallel()
	root, store, target := exactAnswerFixture(t)
	file, err := os.OpenFile(store.Path(target.DeliveryID), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("corrupt\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path(target.DeliveryID))
	if err != nil {
		t.Fatal(err)
	}
	if id, err := AnswerQuestion(context.Background(), root, target, "Answer"); id != "" || err == nil || errors.Is(err, ErrStaleQuestion) {
		t.Fatalf("corrupt journal misrepresented: %q %v", id, err)
	}
	after, err := os.ReadFile(store.Path(target.DeliveryID))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("corrupt journal changed")
	}
}

func TestAnswerQuestionRequiresExplicitWorkspace(t *testing.T) {
	t.Chdir(tempDir(t))
	_, _, target := exactAnswerFixture(t)
	for _, workspace := range []string{"", "."} {
		if id, err := AnswerQuestion(context.Background(), workspace, target, "Answer"); id != "" || !errors.Is(err, ErrInvalidQuestionTarget) {
			t.Fatalf("implicit workspace %q: %q %v", workspace, id, err)
		}
	}
}

func TestAnswerQuestionConcurrentAndDuplicate(t *testing.T) {
	t.Parallel()
	root, store, target := exactAnswerFixture(t)
	before := answerRecords(t, store, target.DeliveryID)
	start := make(chan struct{})
	results := make(chan string, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			id, err := AnswerQuestion(context.Background(), root, target, "Answer")
			if err == nil && id != target.DeliveryID {
				t.Errorf("unexpected success: %q", id)
			}
			if err != nil && !errors.Is(err, ErrDeliveryOwned) && !errors.Is(err, ErrStaleQuestion) {
				t.Errorf("unclassified concurrent refusal: %v", err)
			}
			results <- id
		})
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for id := range results {
		if id != "" {
			accepted++
		}
	}
	after := answerRecords(t, store, target.DeliveryID)
	if accepted != 1 || len(after) != len(before)+1 || after[len(after)-1].Kind != KindAnswer {
		t.Fatalf("accepted=%d records=%d->%d", accepted, len(before), len(after))
	}
	if id, err := AnswerQuestion(context.Background(), root, target, "Answer"); id != "" || !errors.Is(err, ErrStaleQuestion) {
		t.Fatalf("duplicate: id=%q err=%v", id, err)
	}
}

func TestAnswerQuestionCancellationOwnerAndMissingDelivery(t *testing.T) {
	t.Parallel()
	root, store, target := exactAnswerFixture(t)
	before := answerRecords(t, store, target.DeliveryID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if id, err := AnswerQuestion(ctx, root, target, "Answer"); id != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %q %v", id, err)
	}
	owner, err := acquireDeliveryOwnership(context.Background(), root, target.DeliveryID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	id, answerErr := AnswerQuestion(context.Background(), root, target, "Answer")
	if err := owner.stop(); err != nil {
		t.Fatal(err)
	}
	if id != "" || !errors.Is(answerErr, ErrDeliveryOwned) {
		t.Fatalf("live owner: %q %v", id, answerErr)
	}
	target.DeliveryID = "missing"
	if id, err := AnswerQuestion(context.Background(), root, target, "Answer"); id != "" || !errors.Is(err, journal.ErrUnknownDelivery) {
		t.Fatalf("missing delivery: %q %v", id, err)
	}
	if len(answerRecords(t, store, "delivery")) != len(before) {
		t.Fatal("rejection changed journal")
	}
}

func TestAnswerQuestionEmptyJournal(t *testing.T) {
	t.Parallel()
	root, store, target := exactAnswerFixture(t)
	if err := os.WriteFile(store.Path(target.DeliveryID), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if id, err := AnswerQuestion(context.Background(), root, target, "Answer"); id != "" || !errors.Is(err, journal.ErrUnknownDelivery) {
		t.Fatalf("empty journal: %q %v", id, err)
	}
	if records := answerRecords(t, store, target.DeliveryID); len(records) != 0 {
		t.Fatal("empty journal changed")
	}
}

func TestAnswerQuestionRejectsJournalClonedToForeignRoot(t *testing.T) {
	t.Parallel()
	_, store, target := exactAnswerFixture(t)
	foreignRoot := tempDir(t)
	foreignStore, err := journal.Open(foreignRoot)
	if err != nil {
		t.Fatal(err)
	}
	copyAnswerDelivery(t, foreignStore, target.DeliveryID, answerRecords(t, store, target.DeliveryID))
	before := answerRecords(t, foreignStore, target.DeliveryID)
	if before[0].Digest != target.OpenedDigest {
		t.Fatal("clone did not retain opening identity")
	}
	if id, err := AnswerQuestion(context.Background(), foreignRoot, target, "Answer"); id != "" || !errors.Is(err, ErrStaleQuestion) {
		t.Fatalf("foreign opening root: %q %v", id, err)
	}
	if len(answerRecords(t, foreignStore, target.DeliveryID)) != len(before) {
		t.Fatal("foreign journal changed")
	}
}

func TestAnswerQuestionNativeRunnerQuestion(t *testing.T) {
	t.Parallel()
	m := resumableAnswerWatch(t)
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(m.currentTime.Sub(time.Now()))
		records := answerRecords(t, m.store, m.delivery)
		var graph routing.DeliveryGraph
		if err := json.Unmarshal(records[len(records)-1].Graph, &graph); err != nil {
			t.Fatal(err)
		}
		task := graphTask(&graph, m.panel.Detail.Task)
		attempt := task.Attempts[len(task.Attempts)-1]
		target := QuestionTarget{DeliveryID: m.delivery, TaskID: task.TaskID, Execution: attempt.Execution, QuestionID: attempt.Question.RequestID, OpenedDigest: records[0].Digest}
		if id, err := AnswerQuestion(context.Background(), m.workspace, target, "use JSON"); err != nil || id != m.delivery {
			t.Fatalf("native runner answer: %q %v", id, err)
		}
	})
}

func TestAnswerQuestionNativeRepeatedResume(t *testing.T) {
	t.Parallel()
	f := setup(t)
	fixture := strings.ReplaceAll(fakeExecutor, "choose the second behavior", "choose the first behavior")
	if err := os.WriteFile(f.fake, []byte(fixture), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runner, err := New(context.Background(), f.options("question-at-ceiling", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := runner.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("initial run: %s %v\n%s", state, err, &out)
	}
	shown := func() QuestionTarget {
		t.Helper()
		records := answerRecords(t, runner.store, runner.Delivery())
		var graph routing.DeliveryGraph
		if err := json.Unmarshal(records[len(records)-1].Graph, &graph); err != nil {
			t.Fatal(err)
		}
		task := graphTask(&graph, "task_1")
		attempt := task.Attempts[len(task.Attempts)-1]
		return QuestionTarget{DeliveryID: runner.Delivery(), TaskID: task.TaskID, Execution: attempt.Execution, QuestionID: attempt.Question.RequestID, OpenedDigest: records[0].Digest}
	}
	first := shown()
	if id, err := AnswerQuestion(context.Background(), f.root, first, "first"); err != nil || id != runner.Delivery() {
		t.Fatalf("first native answer: %q %v", id, err)
	}
	opts := f.options("question-at-ceiling", &out)
	opts.Resume = runner.Delivery()
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("resumed run: %s %v\n%s", state, err, &out)
	}
	second := shown()
	if second.Execution != 2 || second.QuestionID != first.QuestionID || second.OpenedDigest != first.OpenedDigest {
		t.Fatalf("unexpected repeated native identity: first=%+v second=%+v", first, second)
	}
	before := answerRecords(t, runner.store, runner.Delivery())
	opened := 0
	for _, record := range before {
		if record.Kind == KindOpened {
			opened++
		}
	}
	if opened != 1 {
		t.Fatalf("resume produced %d openings", opened)
	}
	if id, err := AnswerQuestion(context.Background(), f.root, first, "first"); id != "" || !errors.Is(err, ErrStaleQuestion) {
		t.Fatalf("old native question accepted: %q %v", id, err)
	}
	if len(answerRecords(t, runner.store, runner.Delivery())) != len(before) {
		t.Fatal("stale native question changed journal")
	}
	if id, err := AnswerQuestion(context.Background(), f.root, second, "second"); err != nil || id != runner.Delivery() {
		t.Fatalf("current native question rejected: %q %v", id, err)
	}
}
