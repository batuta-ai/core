package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/inventory"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/worktree"
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

func supervisionRepository(t *testing.T, opts SupervisionOptions) fixture {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{root: opts.Workspace, git: git}
	f.run(t, "init", "-q")
	f.run(t, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial")
	f.base = f.run(t, "rev-parse", "HEAD")
	return f
}

func TestSupervisionTerminalDeletionIntent(t *testing.T) {
	for _, scenario := range []string{"absent", "present", "replaced", "packed", "dangling", "corrupt ref", "lookup failure", "invalid ref", "short ref", "over budget", "cleanup", "bookkeeping", "blocked"} {
		t.Run(scenario, func(t *testing.T) {
			store, opts := supervisionFixture(t)
			f := supervisionRepository(t, opts)
			refs := []worktree.ParkedRef{
				{Ref: "refs/batuta/parked/old/task-1", SHA: f.base},
				{Ref: "refs/batuta/parked/old/task-2", SHA: f.base},
				{Ref: "refs/batuta/parked/old/task-3", SHA: f.base},
			}
			// Prefix matches and refs from other deliveries cannot stand in for an exact ref.
			f.run(t, "update-ref", refs[0].Ref+"-other", f.base)
			f.run(t, "update-ref", "refs/batuta/parked/other/task-3", f.base)
			detail := terminalDetail{State: StateDone, Deletions: refs}
			switch scenario {
			case "present", "packed":
				f.run(t, "update-ref", refs[1].Ref, f.base)
				if scenario == "packed" {
					f.run(t, "pack-refs", "--all")
				}
			case "replaced":
				f.run(t, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "replacement")
				f.run(t, "update-ref", refs[2].Ref, f.run(t, "rev-parse", "HEAD"))
			case "dangling":
				f.run(t, "symbolic-ref", refs[1].Ref, "refs/heads/missing")
			case "corrupt ref":
				if err := os.WriteFile(filepath.Join(opts.Workspace, ".git", refs[1].Ref), []byte("invalid\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "lookup failure":
				if err := os.Rename(filepath.Join(opts.Workspace, ".git"), filepath.Join(opts.Workspace, "saved-git")); err != nil {
					t.Fatal(err)
				}
			case "invalid ref":
				detail.Deletions[1].Ref = "refs/batuta/../invalid"
			case "short ref":
				detail.Deletions[1].Ref = "parked"
			case "over budget":
				for len(detail.Deletions) <= 128 {
					detail.Deletions = append(detail.Deletions, refs[0])
				}
			case "cleanup":
				detail.CleanupPending = true
			case "bookkeeping":
				detail.BookkeepingPending = true
			case "blocked":
				detail.State = StateBlocked
			}
			data, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			supervisionAppend(t, store, opts, KindTerminal, string(data))
			before, err := os.ReadFile(store.Path(opts.Delivery))
			if err != nil {
				t.Fatal(err)
			}
			var refsBefore string
			if scenario != "lookup failure" {
				refsBefore = f.run(t, "for-each-ref")
			}
			got := supervisionObserve(t, opts)
			complete := scenario == "absent"
			if got.Completed != complete || len(got.Events) != 1 || got.Events[0].Completed != complete || len(got.Pending) != 1 || got.Pending[0].Completed != complete {
				t.Fatalf("observation = %+v", got)
			}
			if got.Pending[0].RecoveryPending != (scenario != "absent" && scenario != "blocked") {
				t.Fatalf("recovery = %+v", got.Pending[0])
			}
			after, err := os.ReadFile(store.Path(opts.Delivery))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("observation changed journal: %v", err)
			}
			if scenario != "lookup failure" && f.run(t, "for-each-ref") != refsBefore {
				t.Fatal("observation changed refs")
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

func TestSupervisionLifecycleScenarios(t *testing.T) {
	for _, test := range []struct {
		name      string
		state     string
		completed bool
	}{
		{name: "waiting_input", state: StateWaitingInput},
		{name: "done", state: StateDone, completed: true},
		{name: "blocked", state: StateBlocked},
		{name: "canceled", state: StateCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, opts := supervisionFixture(t)
			supervisionAppend(t, store, opts, KindOpened, `{}`)
			supervisionAppend(t, store, opts, KindTerminal, `{"state":"`+test.state+`"}`)
			got := supervisionObserve(t, opts)
			if got.TerminalState != test.state || got.Completed != test.completed || len(got.Events) != 1 {
				t.Fatalf("observation = %+v", got)
			}
		})
	}

	t.Run("running", func(t *testing.T) {
		store, opts := supervisionFixture(t)
		supervisionAppend(t, store, opts, KindOpened, `{}`)
		supervisionAppend(t, store, opts, KindStarted, `{"execution":1}`)
		lockPath := filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".lock")
		if err := os.WriteFile(lockPath, []byte(`{"pid":123}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(lockPath, opts.Now(), opts.Now()); err != nil {
			t.Fatal(err)
		}
		got := supervisionObserve(t, opts)
		if got.Presence != "running" || got.TerminalState != "" || got.Completed || len(got.Events) != 0 {
			t.Fatalf("observation = %+v", got)
		}
	})

	t.Run("restart", func(t *testing.T) {
		store, opts := supervisionFixture(t)
		supervisionAppend(t, store, opts, KindOpened, `{}`)
		event := supervisionAppend(t, store, opts, KindQuestion, `{"execution":1,"request_id":"restart-question"}`)
		first := supervisionObserve(t, opts)
		restarted := SupervisionOptions{Workspace: opts.Workspace, Delivery: opts.Delivery, CursorPath: opts.CursorPath, Now: opts.Now}
		second := supervisionObserve(t, restarted)
		if len(first.Events) != 1 || first.Events[0].Sequence != event.Seq || len(second.Events) != 0 || len(second.Pending) != 1 || second.Pending[0].ID != first.Events[0].ID {
			t.Fatalf("first=%+v restarted=%+v", first, second)
		}
	})
}

func TestSupervisionDuplicateObservers(t *testing.T) {
	store, opts := supervisionFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	supervisionAppend(t, store, opts, KindQuestion, `{"execution":1,"request_id":"shared-question"}`)

	start := make(chan struct{})
	results := make(chan SupervisionObservation, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			got, err := ObserveSupervision(opts)
			results <- got
			errs <- err
		}()
	}
	close(start)
	newEvents := 0
	for range 2 {
		got := <-results
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		newEvents += len(got.Events)
		if len(got.Pending) != 1 || got.Pending[0].ID != "delivery-one:2" {
			t.Fatalf("observation = %+v", got)
		}
	}
	if newEvents != 1 {
		t.Fatalf("new event deliveries = %d, want 1", newEvents)
	}
	if got := supervisionObserve(t, opts); len(got.Events) != 0 || len(got.Pending) != 1 {
		t.Fatalf("durable outbox = %+v", got)
	}
}

func supervisionRunnerFixture(t *testing.T) (fixture, SuperviseOptions, SupervisionEvent) {
	t.Helper()
	f := setup(t)
	// Keep the first run at a question, before any integration preflight.
	planPath := filepath.Join(f.root, ".batuta", "plans", "greetings.md")
	plan := testPlan
	plan = plan[:strings.Index(plan, "- [ ] 2.")] + "\n## Decisions and context\nOne approved task.\n"
	if err := os.WriteFile(planPath, []byte(plan), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", planPath)
	f.run(t, "commit", "-qm", "test: one waiting task")
	var output bytes.Buffer
	execution := f.options("ask", &output)
	runner, err := New(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := runner.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("waiting run: %s %v\n%s", state, err, &output)
	}
	observer := SupervisionOptions{Workspace: f.root, Delivery: runner.Delivery(), CursorPath: filepath.Join(t.TempDir(), "cursor.json"), Now: execution.Now}
	observation := supervisionObserve(t, observer)
	var event SupervisionEvent
	for _, candidate := range observation.Events {
		if candidate.Kind == KindQuestion {
			event = candidate
		}
	}
	policy := &SupervisionPolicy{Delivery: event.Delivery, TaskID: event.TaskID, Execution: event.Execution, QuestionID: event.QuestionID,
		QuestionDigest: event.Evidence.Digest, Action: SupervisionContinueApprovedTask, Ownership: "approved_task", MaxAttempts: 2,
		PlanEvidence: SupervisionEvidence{Path: ".batuta/plans/greetings.md", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(plan)))}}
	execution.Environment[0] = "FAKE_SCENARIO=question-at-ceiling"
	execution.Plan = ""
	execution.Transport = &executor.TransportBackend{Mode: "cli"}
	return f, SuperviseOptions{Observer: observer, Policy: policy, Execution: &execution, Once: true, Interval: time.Second}, event
}

func TestSupervisionPolicyResumesRunner(t *testing.T) {
	f, opts, _ := supervisionRunnerFixture(t)
	before := kinds(readJournal(t, f, opts.Observer.Delivery))
	passive := opts
	passive.Policy = nil
	if err := Supervise(context.Background(), passive); err != nil {
		t.Fatal(err)
	}
	if got := kinds(readJournal(t, f, opts.Observer.Delivery)); got[KindAnswer] != 0 || got[KindStarted] != before[KindStarted] {
		t.Fatalf("passive observer dispatched: %v", got)
	}
	if err := Supervise(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	records := readJournal(t, f, opts.Observer.Delivery)
	counts := kinds(records)
	if counts[KindAnswer] != 1 || counts[KindStarted] != before[KindStarted]+1 {
		t.Fatalf("answer did not resume: %v", counts)
	}
	if counts[KindQuestion] != 2 || !strings.Contains(string(records[len(records)-2].Graph), "choose the final behavior") {
		t.Fatalf("resumed worker did not receive bound answer: %v", counts)
	}

	if err := Supervise(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(readJournal(t, f, opts.Observer.Delivery)) != len(records) {
		t.Fatal("repeated continuation")
	}
}

func TestSupervisionContinuationCrashBoundaries(t *testing.T) {
	for _, stage := range []string{"pending", "acquiring", "acquired", "running"} {
		t.Run(stage, func(t *testing.T) {
			f, opts, event := supervisionRunnerFixture(t)
			decision, err := InterveneSupervision(opts.Observer, event.ID, opts.Policy)
			if err != nil {
				t.Fatal(err)
			}
			// Leave the budget at its pre-answer crash state, with the answer durable.
			ledgerPath := filepath.Join(f.root, journal.Dir, event.Delivery+".supervision.json")
			ledger, err := readSupervisionDecisions(ledgerPath, event.Delivery)
			if err != nil {
				t.Fatal(err)
			}
			decision.Outcome, decision.Reason = "pending", "answer_attempt_recorded"
			for key := range ledger.Entries {
				ledger.Entries[key] = decision
			}
			if err := writeSupervisionJSON(ledgerPath, ledger); err != nil {
				t.Fatal(err)
			}
			settings, err := supervisionSettings(*opts.Execution)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(opts.Policy)
			intent := supervisionContinuation{Version: 1, Delivery: event.Delivery, EventID: event.ID, PolicyDigest: fmt.Sprintf("%x", sha256.Sum256(encoded)), Settings: settings, Stage: stage}
			path := filepath.Join(f.root, journal.Dir, fmt.Sprintf("%s.continuation-%d.json", event.Delivery, event.Sequence))
			if err := writeSupervisionJSON(path, intent); err != nil {
				t.Fatal(err)
			}
			before := kinds(readJournal(t, f, event.Delivery))
			if stage == "acquired" || stage == "running" {
				runnerOptions := *opts.Execution
				runnerOptions.Resume = event.Delivery
				runner, err := Resume(context.Background(), runnerOptions)
				if err != nil {
					t.Fatal(err)
				}
				defer runner.Release()
				// A crashed or still-live owner is not authorization for takeover.
				decision, err := continueSupervision(context.Background(), opts, event)
				if err != nil || decision.Continuation != "ownership_unresolved" {
					t.Fatalf("owned restart: %+v %v", decision, err)
				}
				if kinds(readJournal(t, f, event.Delivery))[KindStarted] != before[KindStarted] {
					t.Fatal("duplicate runner")
				}
				if err := runner.Release(); err != nil {
					t.Fatal(err)
				}
			}
			decision, err = continueSupervision(context.Background(), opts, event)
			if err != nil || decision.Outcome != "answered" || decision.Continuation != "resumed" || decision.RunState != StateWaitingInput {
				t.Fatalf("restart: %+v %v", decision, err)
			}
			records := readJournal(t, f, event.Delivery)
			if got := kinds(records); got[KindAnswer] != 1 || got[KindStarted] != before[KindStarted]+1 {
				t.Fatalf("replayed: %v", got)
			}
			// Crash after Run before the continuation acknowledgment.
			data, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(data, &intent) != nil {
				t.Fatal("read continuation", err)
			}
			intent.Stage, intent.Activity, intent.RunState = "running", SupervisionEvidence{}, ""
			if err := writeSupervisionJSON(path, intent); err != nil {
				t.Fatal(err)
			}
			decision, err = continueSupervision(context.Background(), opts, event)
			if err != nil || decision.Continuation != "resumed" || len(readJournal(t, f, event.Delivery)) != len(records) {
				t.Fatalf("replayed after activity: %+v %v", decision, err)
			}
		})
	}
}

func TestSupervisionContinuationConcurrentObservers(t *testing.T) {
	f, opts, event := supervisionRunnerFixture(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := continueSupervision(context.Background(), opts, event); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if counts := kinds(readJournal(t, f, event.Delivery)); counts[KindAnswer] != 1 || counts[KindStarted] != 2 {
		t.Fatalf("concurrent execution: %v", counts)
	}
}

func TestSupervisionContinuationRequiresExecutionSettings(t *testing.T) {
	store, observer, event, policy := supervisionPolicyFixture(t)
	opts := SuperviseOptions{Observer: observer, Policy: &policy, Execution: &Options{}}
	before := len(answerRecords(t, store, event.Delivery))
	if _, err := continueSupervision(context.Background(), opts, event); err == nil {
		t.Fatal("accepted implicit execution settings")
	}
	if len(answerRecords(t, store, event.Delivery)) != before {
		t.Fatal("answered before validating execution settings")
	}
}

func TestSupervisionContinuationBlocksUnresolvedWork(t *testing.T) {
	for _, scenario := range []string{"stale owner", "broken owner", "uncertain dispatch", "running sibling", "changed settings", "changed plan", "replaced journal", "corrupt intent", "lost activity", "policy removed"} {
		t.Run(scenario, func(t *testing.T) {
			store, observer, event, policy := supervisionPolicyFixture(t)
			execution := Options{Workspace: observer.Workspace, Skills: observer.Workspace, Transport: &executor.TransportBackend{Mode: "cli"},
				Inventory: func(context.Context) (inventory.InventorySnapshot, error) {
					t.Fatal("unsafe continuation reached inventory")
					return inventory.InventorySnapshot{}, nil
				}}
			opts := SuperviseOptions{Observer: observer, Policy: &policy, Execution: &execution}
			if _, err := InterveneSupervision(observer, event.ID, &policy); err != nil {
				t.Fatal(err)
			}
			records := answerRecords(t, store, event.Delivery)
			path := filepath.Join(observer.Workspace, journal.Dir, fmt.Sprintf("%s.continuation-%d.json", event.Delivery, event.Sequence))
			settings, err := supervisionSettings(execution)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(policy)
			intent := supervisionContinuation{Version: 1, Delivery: event.Delivery, EventID: event.ID, PolicyDigest: fmt.Sprintf("%x", sha256.Sum256(encoded)), Settings: settings, Stage: "acquiring"}
			if err := writeSupervisionJSON(path, intent); err != nil {
				t.Fatal(err)
			}
			wantError := false
			switch scenario {
			case "stale owner", "broken owner":
				owner := presenceLock{PID: 1, Host: "another-host", StartedAt: observer.Now().Add(-time.Hour), RefreshedAt: observer.Now().Add(-time.Hour)}
				data, _ := json.Marshal(owner)
				if scenario == "broken owner" {
					data = []byte("{")
				}
				if err := os.WriteFile(filepath.Join(observer.Workspace, journal.Dir, event.Delivery+".lock"), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "uncertain dispatch", "running sibling":
				record := records[len(records)-1]
				kind, detail := KindDispatchIntent, json.RawMessage(`{"execution":2,"backend":"acp","submission":"uncertain"}`)
				if scenario == "running sibling" {
					kind, detail = KindProgress, json.RawMessage(`{}`)
				}
				if _, err := store.Append(event.Delivery, journal.Record{Kind: kind, TaskID: "other", Detail: detail, Graph: record.Graph}); err != nil {
					t.Fatal(err)
				}
			case "changed settings":
				execution.Parallel++
				wantError = true
			case "changed plan":
				if err := os.WriteFile(filepath.Join(observer.Workspace, policy.PlanEvidence.Path), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced journal":
				intent.Answer = SupervisionEvidence{Path: event.Evidence.Path, Sequence: len(records), Digest: "different"}
				if err := writeSupervisionJSON(path, intent); err != nil {
					t.Fatal(err)
				}
				wantError = true
			case "lost activity":
				intent.Stage = "resumed"
				if err := writeSupervisionJSON(path, intent); err != nil {
					t.Fatal(err)
				}
				wantError = true
			case "corrupt intent":
				if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				wantError = true
			case "policy removed":
				// A durable intent alone never supplies policy authority.
				opts.Policy = nil
				opts.Interval, opts.Once = time.Second, true
				before := len(answerRecords(t, store, event.Delivery))
				if err := Supervise(context.Background(), opts); err != nil {
					t.Fatal(err)
				}
				if len(answerRecords(t, store, event.Delivery)) != before {
					t.Fatal("intent authorized execution")
				}
				return
			}
			before := len(answerRecords(t, store, event.Delivery))
			decision, err := continueSupervision(context.Background(), opts, event)
			if (err != nil) != wantError {
				t.Fatalf("decision=%+v err=%v", decision, err)
			}
			if scenario == "lost activity" && (err == nil || !strings.Contains(err.Error(), "activity")) {
				t.Fatalf("lost activity was not reconciled: %v", err)
			}
			if len(answerRecords(t, store, event.Delivery)) != before {
				t.Fatal("unresolved continuation dispatched")
			}
		})
	}
}

func TestSupervisionReadFileBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := readSupervisionFile(path, 4)
	if err != nil || string(data) != "1234" {
		t.Fatalf("regular file: %q, %v", data, err)
	}
	if _, err := readSupervisionFile(path, 3); err == nil {
		t.Fatal("accepted oversized file")
	}
	if _, err := readSupervisionFile(filepath.Dir(path), 4); err == nil {
		t.Fatal("accepted directory")
	}
	if _, err := readSupervisionFile(path+".missing", 4); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestSupervisionJSONReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for _, value := range []string{"first", "replacement"} {
		if err := writeSupervisionJSON(path, value); err != nil {
			t.Fatal(err)
		}
		data, err := readSupervisionFile(path, 32)
		if err != nil || string(data) != `"`+value+`"` {
			t.Fatalf("persisted value: %s, %v", data, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("temporary files left behind: %v, %v", entries, err)
	}
}
