package loop

import (
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func answerRecords(t *testing.T, store *journal.Store, delivery string) []journal.Record {
	t.Helper()
	records, err := store.Read(delivery)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func copyAnswerDelivery(t *testing.T, store *journal.Store, delivery string, records []journal.Record) {
	t.Helper()
	for _, record := range records {
		if _, err := store.Append(delivery, record); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnswerBindsToDelivery(t *testing.T) {
	m := resumableAnswerWatch(t)
	synctest.Test(t, func(t *testing.T) {
		// Advance the virtual clock so the public API closes the fixture's human pause.
		time.Sleep(m.currentTime.Sub(time.Now()))
		before := answerRecords(t, m.store, m.delivery)
		copyAnswerDelivery(t, m.store, "aaa-other", before)
		var graph routing.DeliveryGraph
		if err := json.Unmarshal(before[len(before)-1].Graph, &graph); err != nil {
			t.Fatal(err)
		}
		task := graphTask(&graph, m.panel.Detail.Task)
		questionID := task.Attempts[len(task.Attempts)-1].Question.RequestID
		for _, wrong := range []struct{ delivery, question string }{{"missing", questionID}, {m.delivery, "stale"}, {"", questionID}, {m.delivery, ""}} {
			if _, err := AnswerDelivery(m.workspace, wrong.delivery, task.TaskID, wrong.question, "use JSON"); err == nil {
				t.Fatalf("accepted invalid binding: %+v", wrong)
			}
			if len(answerRecords(t, m.store, m.delivery)) != len(before) {
				t.Fatal("invalid binding recorded an answer")
			}
		}
		delivery, err := AnswerDelivery(m.workspace, m.delivery, task.TaskID, questionID, "use JSON")
		if err != nil || delivery != m.delivery {
			t.Fatalf("bound answer: %q, %v", delivery, err)
		}
		after := answerRecords(t, m.store, m.delivery)
		if len(after) != len(before)+1 || after[len(after)-1].Kind != KindAnswer {
			t.Fatal("bound answer not recorded")
		}
		if len(answerRecords(t, m.store, "aaa-other")) != len(before) {
			t.Fatal("other delivery changed")
		}
		if _, err := AnswerDelivery(m.workspace, m.delivery, task.TaskID, questionID, "use JSON"); err == nil {
			t.Fatal("duplicate answer accepted")
		}
	})
}
