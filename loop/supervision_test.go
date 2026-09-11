package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
)

func supervisionFixture(t *testing.T) (*journal.Store, SupervisionOptions) {
	t.Helper()
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return store, SupervisionOptions{
		Workspace: root, Delivery: "delivery-one", CursorPath: filepath.Join(root, "observer.json"),
		Now: func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) },
	}
}

func supervisionAppend(t *testing.T, store *journal.Store, opts SupervisionOptions, kind journal.Kind, detail string) journal.Record {
	t.Helper()
	record, err := store.Append(opts.Delivery, journal.Record{Kind: kind, TaskID: "task_1", Detail: json.RawMessage(detail), At: opts.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func supervisionObserve(t *testing.T, opts SupervisionOptions) SupervisionObservation {
	t.Helper()
	got, err := ObserveSupervision(opts)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSupervisionDurableDeduplication(t *testing.T) {
	store, opts := supervisionFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	question := supervisionAppend(t, store, opts, KindQuestion, `{"execution":2,"request_id":"question-one","question":"Which format?"}`)
	first := supervisionObserve(t, opts)
	if first.Cursor != 2 || len(first.Events) != 1 || len(first.Pending) != 1 {
		t.Fatalf("first observation = %+v", first)
	}
	event := first.Events[0]
	if event.ID != "delivery-one:2" || event.Sequence != question.Seq || event.QuestionID != "question-one" || event.Execution != 2 || event.Evidence.Digest != question.Digest {
		t.Fatalf("event identity = %+v", event)
	}
	// Each call reloads the durable cursor, as a fresh process would.
	again := supervisionObserve(t, opts)
	if len(again.Events) != 0 || len(again.Pending) != 1 || again.Pending[0].ID != event.ID {
		t.Fatalf("repeated observation = %+v", again)
	}
	if err := AcknowledgeSupervision(opts, event.ID); err != nil {
		t.Fatal(err)
	}
	if err := AcknowledgeSupervision(opts, event.ID); err != nil {
		t.Fatalf("repeated acknowledgment: %v", err)
	}
	if got := supervisionObserve(t, opts); len(got.Events) != 0 || len(got.Pending) != 0 {
		t.Fatalf("acknowledged event replayed: %+v", got)
	}
	supervisionAppend(t, store, opts, KindQuestion, `{"execution":3,"request_id":"question-two"}`)
	if got := supervisionObserve(t, opts); len(got.Events) != 1 || got.Events[0].ID != "delivery-one:3" {
		t.Fatalf("next event = %+v", got)
	}
	if err := AcknowledgeSupervision(opts, "delivery-two:3"); err == nil {
		t.Fatal("accepted an unknown event acknowledgment")
	}
}

func TestSupervisionPartialTrailingRecord(t *testing.T) {
	store, opts := supervisionFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	supervisionAppend(t, store, opts, KindQuestion, `{"execution":1}`)
	path := store.Path(opts.Delivery)
	complete, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.IndexByte(string(complete), '\n') + 1
	for _, end := range []int{start + 10, len(complete) - 1, len(complete)} {
		if err := os.WriteFile(path, complete[:end], 0o600); err != nil {
			t.Fatal(err)
		}
		got := supervisionObserve(t, opts)
		want := 0
		if end == len(complete) {
			want = 1
		}
		if len(got.Events) != want || got.Cursor != 1+want {
			t.Fatalf("end %d: %+v", end, got)
		}
	}
}

func TestSupervisionRejectsCorruptionAndCursorReuse(t *testing.T) {
	for _, scenario := range []string{"complete malformed line", "chain corruption", "rewritten prefix", "truncation", "other delivery", "corrupt cursor", "empty cursor object"} {
		t.Run(scenario, func(t *testing.T) {
			store, opts := supervisionFixture(t)
			supervisionAppend(t, store, opts, KindOpened, `{}`)
			supervisionAppend(t, store, opts, KindQuestion, `{}`)
			supervisionObserve(t, opts)
			path := store.Path(opts.Delivery)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "complete malformed line":
				data = append(data, []byte("{broken}\n")...)
			case "chain corruption":
				data = []byte(strings.Replace(string(data), "question_recorded", "failure_recorded", 1))
			case "rewritten prefix":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				supervisionAppend(t, store, opts, KindOpened, `{"different":true}`)
				supervisionAppend(t, store, opts, KindQuestion, `{}`)
				data, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			case "truncation":
				data = data[:strings.IndexByte(string(data), '\n')+1]
			case "other delivery":
				opts.Delivery = "delivery-two"
				supervisionAppend(t, store, opts, KindOpened, `{}`)
			case "corrupt cursor":
				if err := os.WriteFile(opts.CursorPath, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty cursor object":
				if err := os.WriteFile(opts.CursorPath, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ObserveSupervision(opts); err == nil {
				t.Fatal("accepted corrupt or mismatched evidence")
			}
		})
	}
}

func TestSupervisionCompactRedactedEvents(t *testing.T) {
	store, opts := supervisionFixture(t)
	secret := "sk-secret-token"
	detail, err := json.Marshal(map[string]any{"execution": 1, "request_id": "question-one", "question": strings.Repeat(secret, 2000), "stdout": secret, "ask_path": "/private/" + secret})
	if err != nil {
		t.Fatal(err)
	}
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	supervisionAppend(t, store, opts, KindQuestion, string(detail))
	event := supervisionObserve(t, opts).Events[0]
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 4<<10 || strings.Contains(string(encoded), secret) || event.Action == "" || event.Evidence.Path != ".batuta/journal/delivery-one.jsonl" {
		t.Fatalf("unsafe or non-actionable event: %s", encoded)
	}
	data, err := os.ReadFile(opts.CursorPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("raw evidence copied into durable cursor")
	}
}

func TestSupervisionPersistenceFailureDoesNotEmit(t *testing.T) {
	store, opts := supervisionFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	supervisionAppend(t, store, opts, KindQuestion, `{}`)
	if err := os.Mkdir(opts.CursorPath, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := ObserveSupervision(opts)
	if err == nil || len(got.Events) != 0 {
		t.Fatalf("emitted without persistence: %+v, %v", got, err)
	}
	if err := os.Remove(opts.CursorPath); err != nil {
		t.Fatal(err)
	}
	if got := supervisionObserve(t, opts); len(got.Events) != 1 {
		t.Fatalf("lost event after persistence failure: %+v", got)
	}
}

func TestSupervisionRequiresExplicitIdentity(t *testing.T) {
	_, opts := supervisionFixture(t)
	if _, err := ObserveSupervision(opts); !errors.Is(err, journal.ErrUnknownDelivery) {
		t.Fatalf("missing journal: %v", err)
	}
	for _, field := range []string{"workspace", "delivery", "cursor"} {
		bad := opts
		switch field {
		case "workspace":
			bad.Workspace = "relative"
		case "delivery":
			bad.Delivery = "../escape"
		case "cursor":
			bad.CursorPath = ""
		}
		if _, err := ObserveSupervision(bad); err == nil {
			t.Fatalf("accepted invalid %s", field)
		}
	}
}

func TestSupervisionTerminalRequiresJournalEvidence(t *testing.T) {
	for _, test := range []struct {
		kind     journal.Kind
		detail   string
		state    string
		complete bool
	}{
		{KindFinished, `{"exit_code":0,"finished":true}`, "", false},
		{KindFinished, `{"exit_code":1,"finished":true}`, "", false},
		{KindInterrupted, `{"error":"process disappeared"}`, "", false},
		{kindFinalizing, `{"state":"done"}`, "", false},
		{KindTerminal, `{"state":"done","cleanup_pending":true}`, StateDone, false},
		{KindTerminal, `{"state":"done","bookkeeping_pending":true}`, StateDone, false},
		{KindTerminal, `{"state":"done","pending_ref_deletions":[{"ref":"parked"}]}`, StateDone, false},
		{KindTerminal, `{"state":"waiting_input"}`, StateWaitingInput, false},
		{KindTerminal, `{"state":"blocked"}`, StateBlocked, false},
		{KindTerminal, `{"state":"canceled"}`, StateCanceled, false},
		{KindTerminal, `{"state":"done"}`, StateDone, true},
	} {
		t.Run(string(test.kind)+test.detail, func(t *testing.T) {
			store, opts := supervisionFixture(t)
			supervisionAppend(t, store, opts, KindOpened, `{}`)
			supervisionAppend(t, store, opts, test.kind, test.detail)
			got := supervisionObserve(t, opts)
			if got.TerminalState != test.state || got.Completed != test.complete || got.Presence != "none" {
				t.Fatalf("observation = %+v", got)
			}
			if test.kind == KindFinished && (len(got.Events) != 1 || got.Events[0].Kind != KindFinished || got.Events[0].Completed) {
				t.Fatalf("process exit misclassified: %+v", got)
			}
		})
	}
}

func TestSupervisionWaitingIsPassive(t *testing.T) {
	store, opts := supervisionFixture(t)
	now := opts.Now()
	opts.Now = func() time.Time { return now }
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	supervisionAppend(t, store, opts, KindStarted, `{"execution":1}`)
	lockPath := filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".lock")
	if err := os.WriteFile(lockPath, []byte(`{"pid":123}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockPath, now, now); err != nil {
		t.Fatal(err)
	}
	if got := supervisionObserve(t, opts); got.Presence != "running" {
		t.Fatalf("fresh activity = %+v", got)
	}
	before, err := os.ReadFile(store.Path(opts.Delivery))
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		now = now.Add(time.Hour)
		got := supervisionObserve(t, opts)
		if got.Presence != "stale" || got.TerminalState != "" || got.Completed || len(got.Events) != 0 || got.Cursor != 2 {
			t.Fatalf("staleness treated as failure: %+v", got)
		}
	}
	after, err := os.ReadFile(store.Path(opts.Delivery))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("observation mutated delivery journal")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if got := supervisionObserve(t, opts); got.Presence != "none" || got.Completed || got.TerminalState != "" || len(got.Events) != 0 {
		t.Fatalf("absent process treated as terminal: %+v", got)
	}
}

func TestSupervisionResumedDeliveryIsNotTerminal(t *testing.T) {
	store, opts := supervisionFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	supervisionAppend(t, store, opts, KindTerminal, `{"state":"waiting_input"}`)
	supervisionObserve(t, opts)
	supervisionAppend(t, store, opts, KindAnswer, `{"execution":1}`)
	if got := supervisionObserve(t, opts); got.TerminalState != "" || got.Completed {
		t.Fatalf("resumed delivery reported old terminal: %+v", got)
	}
}

func TestSupervisionForegroundWaitAndCancellation(t *testing.T) {
	store, observer := supervisionFixture(t)
	supervisionAppend(t, store, observer, KindOpened, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	waits := 0
	opts := SuperviseOptions{Observer: observer, Interval: time.Second, Output: &output,
		Sleep: func(ctx context.Context, delay time.Duration) error {
			waits++
			if delay != time.Second {
				t.Fatalf("delay = %s", delay)
			}
			if waits == 3 {
				cancel()
				return ctx.Err()
			}
			return nil
		},
	}
	if err := Supervise(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if waits != 3 || strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("waits=%d output=%s", waits, &output)
	}
	if records, err := store.Read(observer.Delivery); err != nil || len(records) != 1 {
		t.Fatalf("passive wait mutated journal: %v, %v", records, err)
	}
}

func TestSupervisionForegroundBoundsAndOnce(t *testing.T) {
	store, observer := supervisionFixture(t)
	supervisionAppend(t, store, observer, KindOpened, `{}`)
	for _, interval := range []time.Duration{-time.Second, 0, time.Millisecond, time.Minute + time.Nanosecond} {
		if err := Supervise(context.Background(), SuperviseOptions{Observer: observer, Interval: interval, Once: true}); err == nil {
			t.Fatalf("accepted interval %s", interval)
		}
	}
	var output bytes.Buffer
	if err := Supervise(context.Background(), SuperviseOptions{Observer: observer, Interval: time.Second, Once: true, Output: &output,
		Sleep: func(context.Context, time.Duration) error { t.Fatal("once slept"); return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"delivery":"delivery-one"`) || !strings.Contains(output.String(), `"state":"open"`) {
		t.Fatalf("missing explicit delivery state: %s", &output)
	}
}
