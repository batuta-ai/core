package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func isolateGrantCache(t *testing.T) {
	t.Helper()
	cache := tempDir(t)
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LocalAppData", cache)
}

func grantCrashJournal(t *testing.T, backend string, turnStarted bool) (fixture, Options, string, string, routing.TaskGrant) {
	t.Helper()
	isolateGrantCache(t)
	f := setup(t)
	plan := "# Plan — Greetings\n\n**Goal:** Greeting.\n**Created:** 2026-09-06 · **Status:** approved\n\n## Tasks\n- [ ] 1. Add greeting one — backend/low\n      Scope: out/1.txt\n      Accept: greeting exists → test -f out/1.txt\n"
	if err := os.WriteFile(filepath.Join(f.root, ".batuta", "plans", "greetings.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", "-A")
	f.run(t, "commit", "-q", "-m", "test: one grant task")
	var out bytes.Buffer
	opts := f.options("default", &out)
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	delivery := r.Delivery()
	appendRecord := func(kind journal.Kind, taskID string, detail any) {
		t.Helper()
		encodedDetail, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		encodedGraph, err := json.Marshal(r.graph)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.store.Append(delivery, journal.Record{Kind: kind, TaskID: taskID, Detail: encodedDetail, Graph: encodedGraph, At: opts.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	appendRecord(KindOpened, "", openedDetail{Slug: r.plan.Slug, PlanPath: r.plan.Path, PlanDigest: r.plan.Set.Digest,
		Branch: r.branch, Head: r.openedHead, Parallel: r.parallel, Workspace: r.root, Generation: r.generation})
	wave, err := r.graph.AdmitReadyWave(routing.ReadyWaveInput{IntegrationHeadSHA: r.openedHead, RemainingSlots: 1, ReachableCommits: map[string]bool{r.openedHead: true}})
	if err != nil || len(wave.TaskIDs) != 1 || wave.TaskIDs[0] != "task_1" {
		t.Fatalf("AdmitReadyWave() = %+v, %v", wave, err)
	}
	if err := r.graph.BeginWaveAttempts(wave.Number, r.generation); err != nil {
		t.Fatal(err)
	}
	name, branch := r.worktreeName("task_1", 1), r.branchName("task_1", 1)
	root := r.git.Path(name)
	f.run(t, "worktree", "add", "-q", "-b", branch, root, r.openedHead)
	if _, err := r.graph.AttachWorktree("task_1", 1, routing.GraphWorktree{ID: name, Root: root, Ready: true}); err != nil {
		t.Fatal(err)
	}
	wt := attemptWorktree{Name: name, Branch: branch, Root: root}
	appendRecord(KindWorktree, "task_1", map[string]any{"execution": 1, "worktree": wt})
	const firstQuestion = "May I edit the greeting?"
	runID := delivery + "-task-1-e1"
	question := routing.TaskQuestion{RequestID: digestString("question:" + runID + ":" + firstQuestion), Prompt: firstQuestion, ContextDigest: digestString("context")}
	if _, err := r.graph.RecordQuestion("task_1", 1, runID, question, opts.Now()); err != nil {
		t.Fatal(err)
	}
	appendRecord(KindQuestion, "task_1", map[string]any{"execution": 1, "question": firstQuestion, "request_id": question.RequestID})
	grant := routing.TaskGrant{Paths: []string{"out/1.txt"}, Operations: []string{"write"}}
	if _, _, err := r.graph.RecordAnswer("task_1", 1, routing.TaskAnswer{QuestionOperationID: question.RequestID, LoopRunID: runID,
		Generation: 1, NodeID: "loop", Value: "go ahead", Grant: &grant}, opts.Now()); err != nil {
		t.Fatal(err)
	}
	appendRecord(KindAnswer, "task_1", map[string]any{"execution": 1, "answer": "go ahead", "grant": grant})
	appendRecord(KindStarted, "task_1", map[string]any{"execution": 2, "dispatch": dispatchDetail{Execution: 2, RunID: runID}})
	appendRecord(KindDispatchResult, "task_1", dispatchDetail{Execution: 2, RunID: runID, Backend: backend, Submission: executor.SubmissionUnknown})
	const pendingQuestion = "May I write out/1.txt?"
	appendRecord(KindGrantReplied, "task_1", grantRepliedDetail(2, runID, pendingQuestion, digestString("question:"+runID+":"+pendingQuestion), grant, 1))
	if turnStarted {
		appendRecord(KindDispatchIntent, "task_1", dispatchDetail{Execution: 2, Turn: 1, RunID: runID, Backend: backend, Submission: executor.SubmissionUncertain})
	}
	opts.Resume = delivery
	return f, opts, delivery, pendingQuestion, grant
}

func TestLoopGrantTurnInterrupted(t *testing.T) {
	f, opts, delivery, _, _ := grantCrashJournal(t, "cli", true)
	r, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Release()
	task, _ := r.graph.Task("task_1")
	if task.State != routing.GraphTaskPreparing || len(task.Attempts) != 3 || task.Attempts[1].BlockerCode != blockerInterrupted {
		t.Fatalf("replayed task = %+v", task)
	}
	if counts := kinds(readJournal(t, f, delivery)); counts[KindFailure] != 1 || counts[KindQuestion] != 1 {
		t.Fatalf("replay records = %v", counts)
	}
}

func TestLoopGrantReplyThenCrash(t *testing.T) {
	f, opts, delivery, question, grant := grantCrashJournal(t, "cli", false)
	r, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Release()
	task, _ := r.graph.Task("task_1")
	if task.State != routing.GraphTaskPreparing || len(task.Attempts) != 3 || task.Attempts[1].BlockerCode != blockerInterrupted {
		t.Fatalf("replayed task = %+v", task)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v", state, err)
	}
	brief, err := os.ReadFile(r.grantTurnBriefPath("task_1", 3, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{question, "Already granted: paths " + grant.Paths[0] + "; operations " + grant.Operations[0], "## Standing grant"} {
		if !strings.Contains(string(brief), want) {
			t.Fatalf("next brief lacks %q:\n%s", want, brief)
		}
	}
	if counts := kinds(readJournal(t, f, delivery)); counts[KindFailure] != 1 || counts[KindQuestion] != 1 {
		t.Fatalf("replayed records = %v", counts)
	}
}

func TestLoopGrantTurnACPUncertain(t *testing.T) {
	for _, turnStarted := range []bool{true, false} {
		name := "reply_then_crash"
		if turnStarted {
			name = "turn_started"
		}
		t.Run(name, func(t *testing.T) {
			f, opts, delivery, _, _ := grantCrashJournal(t, "acp", turnStarted)
			r, err := Resume(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Release()
			task, _ := r.graph.Task("task_1")
			if task.State != routing.GraphTaskBlocked || len(task.Attempts) != 2 || task.BlockerCode != blockerSubmissionUncertain {
				t.Fatalf("replayed task = %+v", task)
			}
			if state, err := r.Run(context.Background()); err != nil || state != StateBlocked {
				t.Fatalf("Run() = %s, %v", state, err)
			}
			if counts := kinds(readJournal(t, f, delivery)); counts[KindFailure] != 1 || counts[KindQuestion] != 1 {
				t.Fatalf("replay records = %v", counts)
			}
		})
	}
}

func runGrantedQuestion(t *testing.T, scenario string) (fixture, *Runner, string) {
	t.Helper()
	isolateGrantCache(t)
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options(scenario, &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("initial Run() = %s, %v\n%s", state, err, out.String())
	}
	delivery, err := AnswerGrant(f.root, "1", "go ahead", routing.TaskGrant{Paths: []string{"out/1.txt"}, Operations: []string{"write"}})
	if err != nil || delivery != r.Delivery() {
		t.Fatalf("AnswerGrant() = %s, %v", delivery, err)
	}
	opts := f.options(scenario, &out)
	opts.Resume = delivery
	resumeClock := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	opts.Now = func() time.Time { return resumeClock }
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	state, err := resumed.Run(context.Background())
	if err != nil {
		t.Fatalf("resumed Run() = %s, %v\n%s", state, err, out.String())
	}
	return f, resumed, state
}

type grantTestBackend struct {
	backend executor.Backend
	before  func(executor.Execution)
	after   func()
}

func (b grantTestBackend) Execute(ctx context.Context, e executor.Execution) (executor.Result, error) {
	if b.before != nil {
		b.before(e)
	}
	result, err := b.backend.Execute(ctx, e)
	if b.after != nil {
		b.after()
	}
	return result, err
}

func runGrantTurnScenario(t *testing.T, scenario string, configure func(*Options), beforeRun func(*Runner)) (fixture, *Runner, string) {
	t.Helper()
	isolateGrantCache(t)
	f := setup(t)
	plan := "# Plan — Greetings\n\n**Goal:** Greeting.\n**Created:** 2026-09-06 · **Status:** approved\n\n## Tasks\n- [ ] 1. Add greeting one — backend/low\n      Scope: out/1.txt\n      Accept: greeting exists → test -f out/1.txt\n"
	if err := os.WriteFile(filepath.Join(f.root, ".batuta", "plans", "greetings.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", "-A")
	f.run(t, "commit", "-q", "-m", "test: one grant task")
	var out bytes.Buffer
	r, err := New(context.Background(), f.options(scenario, &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("initial Run() = %s, %v\n%s", state, err, out.String())
	}
	delivery, err := AnswerGrant(f.root, "1", "go ahead", routing.TaskGrant{Paths: []string{"out/1.txt"}, Operations: []string{"write"}})
	if err != nil || delivery != r.Delivery() {
		t.Fatalf("AnswerGrant() = %s, %v", delivery, err)
	}
	opts := f.options(scenario, &out)
	opts.Resume = delivery
	resumeClock := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	opts.Now = func() time.Time { return resumeClock }
	if configure != nil {
		configure(&opts)
	}
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if beforeRun != nil {
		beforeRun(resumed)
	}
	state, err := resumed.Run(context.Background())
	if err != nil {
		t.Fatalf("resumed Run() = %s, %v\n%s", state, err, out.String())
	}
	return f, resumed, state
}

func grantReplyQuestions(t *testing.T, f fixture, r *Runner) []string {
	t.Helper()
	var questions []string
	for _, record := range readJournal(t, f, r.Delivery()) {
		if record.TaskID != "task_1" || record.Kind != KindGrantReplied {
			continue
		}
		var detail struct {
			Question string `json:"question"`
		}
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		questions = append(questions, detail.Question)
	}
	return questions
}

func TestLoopGrantRepeatParks(t *testing.T) {
	f, r, state := runGrantTurnScenario(t, "grant-repeat", nil, nil)
	if state != StateWaitingInput {
		t.Fatalf("state = %s, want waiting_input", state)
	}
	questions := grantReplyQuestions(t, f, r)
	if len(questions) != 2 || questions[0] != "may I edit the greeting?" || questions[1] != "may I edit the second greeting?" {
		t.Fatalf("grant replies = %q", questions)
	}
	task, _ := r.graph.Task("task_1")
	if len(task.Attempts) != 2 || task.Attempts[1].Question == nil || task.Attempts[1].Question.Prompt != "may I   edit the greeting?" {
		t.Fatalf("parked task = %+v", task)
	}
}

func TestLoopGrantTurnCap(t *testing.T) {
	f, r, state := runGrantTurnScenario(t, "grant-cap", nil, nil)
	if state != StateWaitingInput {
		t.Fatalf("state = %s, want waiting_input", state)
	}
	if questions := grantReplyQuestions(t, f, r); len(questions) != 3 {
		t.Fatalf("grant replies = %q, want three", questions)
	}
	task, _ := r.graph.Task("task_1")
	if len(task.Attempts) != 2 || task.Attempts[1].Question == nil || task.Attempts[1].Question.Prompt != "may I edit greeting number 4?" {
		t.Fatalf("parked task = %+v", task)
	}
}

func TestLoopGrantTurnsShareDeadline(t *testing.T) {
	clock := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	var timeouts []time.Duration
	calls := 0
	f, r, state := runGrantTurnScenario(t, "grant-deadline", func(opts *Options) {
		opts.Now = func() time.Time { return clock }
	}, func(r *Runner) {
		r.backend = grantTestBackend{backend: r.backend, before: func(e executor.Execution) {
			timeouts = append(timeouts, e.Timeout)
		}, after: func() {
			calls++
			if calls == 1 {
				clock = clock.Add(30 * time.Second)
			} else {
				clock = clock.Add(2 * time.Minute)
			}
		}}
	})
	if state != StateWaitingInput {
		t.Fatalf("state = %s, want waiting_input", state)
	}
	if len(timeouts) != 2 || timeouts[0] != 2*time.Minute || timeouts[1] != 90*time.Second {
		t.Fatalf("dispatch timeouts = %v, want [2m 1m30s]", timeouts)
	}
	if questions := grantReplyQuestions(t, f, r); len(questions) != 1 {
		t.Fatalf("grant replies = %q, want one before deadline exhaustion", questions)
	}
	task, _ := r.graph.Task("task_1")
	if task.Attempts[1].Question == nil || task.Attempts[1].Question.Prompt != "may I edit greeting number 2?" {
		t.Fatalf("parked task = %+v", task)
	}
}

func TestLoopGrantLimitWaitKeepsDeadline(t *testing.T) {
	clock := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	reset := clock.Add(10 * time.Second)
	var timeouts []time.Duration
	var waited []time.Duration
	f, r, state := runGrantTurnScenario(t, "grant-limit-wait", func(opts *Options) {
		opts.Now = func() time.Time { return clock }
		opts.MaxLimitWaits = 2
		opts.Environment = append(opts.Environment, "FAKE_RESET_AT="+fmt.Sprint(reset.Unix()))
		opts.Sleep = fixtureSleep(func(delay time.Duration) {
			waited = append(waited, delay)
			clock = clock.Add(delay)
		})
	}, func(r *Runner) {
		r.backend = grantTestBackend{backend: r.backend, before: func(e executor.Execution) {
			timeouts = append(timeouts, e.Timeout)
		}}
	})
	if state != StateDone {
		t.Fatalf("state = %s, want done", state)
	}
	if len(waited) != 1 || waited[0] != 10*time.Second+time.Millisecond {
		t.Fatalf("waited = %v", waited)
	}
	if len(timeouts) != 4 || timeouts[0] != 2*time.Minute || timeouts[1] != 2*time.Minute || timeouts[2] != timeouts[1] {
		t.Fatalf("dispatch timeouts = %v", timeouts)
	}
	if questions := grantReplyQuestions(t, f, r); len(questions) != 2 {
		t.Fatalf("grant replies = %q", questions)
	}
}

func TestLoopGrantTurnsKeepTokenReservation(t *testing.T) {
	const reservation int64 = 317
	first := true
	f, r, state := runGrantTurnScenario(t, "grant-reservation", nil, func(r *Runner) {
		r.backend = grantTestBackend{backend: r.backend, before: func(executor.Execution) {
			if !first {
				return
			}
			first = false
			r.mu.Lock()
			defer r.mu.Unlock()
			for i := range r.graph.Tasks {
				if r.graph.Tasks[i].TaskID == "task_1" {
					r.graph.Tasks[i].Attempts[1].TokenAllowance = reservation
					return
				}
			}
			t.Fatal("task_1 missing from graph")
		}}
	})
	if state != StateDone {
		t.Fatalf("state = %s, want done", state)
	}
	if questions := grantReplyQuestions(t, f, r); len(questions) != 2 {
		t.Fatalf("grant replies = %q", questions)
	}
	for _, record := range readJournal(t, f, r.Delivery()) {
		if record.TaskID != "task_1" || (record.Kind != KindGrantReplied && record.Kind != KindDispatchResult) {
			continue
		}
		var graph routing.DeliveryGraph
		if err := json.Unmarshal(record.Graph, &graph); err != nil {
			t.Fatal(err)
		}
		task, _ := graph.Task("task_1")
		if len(task.Attempts) == 2 && task.Attempts[1].TokenAllowance != reservation {
			t.Fatalf("%s allowance = %d, want %d", record.Kind, task.Attempts[1].TokenAllowance, reservation)
		}
	}
	task, _ := r.graph.Task("task_1")
	if task.Attempts[1].TokenAllowance != reservation {
		t.Fatalf("final allowance = %d, want %d", task.Attempts[1].TokenAllowance, reservation)
	}
}

func TestLoopGrantRepliesWithoutParking(t *testing.T) {
	f, r, state := runGrantedQuestion(t, "grant-reply")
	if state != StateDone {
		t.Fatalf("state = %s, want done", state)
	}
	brief, err := os.ReadFile(filepath.Join(f.state, "grant-reply-brief"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"may I edit the greeting?", "Already granted:"} {
		if !bytes.Contains(brief, []byte(want)) {
			t.Fatalf("second brief lacks %q", want)
		}
	}
	task, found := r.graph.Task("task_1")
	if !found || len(task.Attempts) != 2 || task.State != routing.GraphTaskIntegrated {
		t.Fatalf("task after grant reply = %+v", task)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".batuta", "asks", "greetings-task-1.md")); !os.IsNotExist(err) {
		t.Fatalf("ask file remains: %v", err)
	}
	counts := map[journal.Kind]int{}
	for _, record := range readJournal(t, f, r.Delivery()) {
		if record.TaskID == "task_1" {
			counts[record.Kind]++
		}
	}
	if counts[KindGrantReplied] != 1 || counts[KindQuestion] != 1 || counts[KindDispatchResult] != 3 {
		t.Fatalf("task journal counts = %v", counts)
	}
}

func TestLoopGrantUnsafeQuestion(t *testing.T) {
	f, r, state := runGrantedQuestion(t, "grant-unsafe")
	if state != StateWaitingInput {
		t.Fatalf("state = %s, want waiting_input after the unsafe failure retries", state)
	}
	// A later retry may ask a safe question and get a grant reply; only the
	// execution that asked the unsafe question must never be replied to.
	records := readJournal(t, f, r.Delivery())
	unsafe := map[int]bool{}
	for _, record := range records {
		if record.TaskID == "task_1" && record.Kind == KindFailure && bytes.Contains(record.Detail, []byte(blockerUnsafeQuestion)) {
			var detail struct {
				Execution int `json:"execution"`
			}
			if err := json.Unmarshal(record.Detail, &detail); err != nil {
				t.Fatal(err)
			}
			unsafe[detail.Execution] = true
		}
	}
	if len(unsafe) == 0 {
		t.Fatal("no question_unsafe failure recorded")
	}
	for _, record := range records {
		if record.TaskID != "task_1" || record.Kind != KindGrantReplied {
			continue
		}
		var detail struct {
			Execution int    `json:"execution"`
			Question  string `json:"question"`
		}
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		if unsafe[detail.Execution] || strings.Contains(detail.Question, "/private/secret") {
			t.Fatalf("unsafe question got a grant reply: %s", record.Detail)
		}
	}
}

func TestLoopGrantTurnRunIdentity(t *testing.T) {
	f, r, state := runGrantedQuestion(t, "grant-reply")
	if state != StateDone {
		t.Fatalf("state = %s, want done", state)
	}
	task, _ := r.graph.Task("task_1")
	wantRunID := task.Attempts[1].ChildRunID
	var dispatches, replies int
	for _, record := range readJournal(t, f, r.Delivery()) {
		if record.TaskID != "task_1" || (record.Kind != KindDispatchIntent && record.Kind != KindDispatchResult && record.Kind != KindGrantReplied) {
			continue
		}
		var detail struct {
			Execution int    `json:"execution"`
			RunID     string `json:"run_id"`
		}
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.RunID == "" {
			t.Fatal("empty run_id")
		}
		if record.Kind == KindGrantReplied {
			replies++
		}
		if detail.Execution == 2 && detail.RunID != wantRunID {
			t.Fatalf("%s run_id = %s, want %s", record.Kind, detail.RunID, wantRunID)
		}
		if record.Kind != KindGrantReplied {
			dispatches++
		}
	}
	if dispatches != 6 || replies != 1 || len(r.summaryLocked().Integrated) != 3 {
		t.Fatalf("dispatches=%d replies=%d summary=%+v", dispatches, replies, r.summaryLocked())
	}
}

func TestGrantRepliedDetail(t *testing.T) {
	t.Parallel()

	grant := routing.TaskGrant{Paths: []string{"docs/b.md", "docs/a.md", "docs/a.md"}, Operations: []string{"write"}}
	detail := grantRepliedDetail(2, "run-1", "May I edit docs?", "req-1", grant, 3)

	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("detail keys = %v, want execution, run_id, question, request_id, grant_digest, turn", got)
	}
	want := map[string]any{"execution": float64(2), "run_id": "run-1", "question": "May I edit docs?", "request_id": "req-1", "turn": float64(3)}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("detail[%q] = %v, want %v", key, got[key], value)
		}
	}

	canonical, err := json.Marshal(grant.Normalize())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if digest := hex.EncodeToString(sum[:])[:12]; got["grant_digest"] != digest {
		t.Fatalf("grant_digest = %v, want %s", got["grant_digest"], digest)
	}
	if again := grantDigest(routing.TaskGrant{Paths: []string{"docs/a.md", "docs/b.md"}, Operations: []string{"write"}}); again != got["grant_digest"] {
		t.Fatalf("digest of an equal normalized grant = %s, want %v", again, got["grant_digest"])
	}
	if grantDigest(routing.TaskGrant{Paths: []string{"docs/a.md"}}) == got["grant_digest"] {
		t.Fatal("a different grant must not share a digest")
	}
}

func TestGrantTurnPaths(t *testing.T) {
	t.Parallel()

	r := &Runner{
		root:     tempDir(t),
		plan:     routing.Plan{Slug: "demo"},
		delivery: "delivery-20261024-abcd",
	}
	trail := strings.TrimSuffix(r.trailPath("task_1"), ".md")
	tests := []struct {
		name            string
		execution, turn int
		brief, log      string
	}{
		{"turn zero keeps today's paths", 1, 0, trail + "-e1.brief.md", r.runLogPath("task_1", 1)},
		{"turn one of execution two", 2, 1, trail + "-e2-g1.brief.md", trail + "-e2-g1.out.log"},
		{"turn three of execution one", 1, 3, trail + "-e1-g3.brief.md", trail + "-e1-g3.out.log"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := r.grantTurnBriefPath("task_1", test.execution, test.turn); got != test.brief {
				t.Fatalf("brief path = %s, want %s", got, test.brief)
			}
			if got := r.grantTurnLogPath("task_1", test.execution, test.turn); got != test.log {
				t.Fatalf("log path = %s, want %s", got, test.log)
			}
		})
	}
	if base := filepath.Base(r.grantTurnLogPath("task_1", 2, 1)); !strings.HasSuffix(base, "-e2-g1.out.log") {
		t.Fatalf("log name = %s", base)
	}
}

func TestTrailSummarizesGrantReply(t *testing.T) {
	t.Parallel()

	root := tempDir(t)
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	detail := grantRepliedDetail(1, "run-1", "May I edit docs?", "req-1", routing.TaskGrant{Paths: []string{"docs/**"}}, 2)
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("delivery-1", journal.Record{Kind: KindGrantReplied, TaskID: "task_1", At: time.Now().UTC(), Detail: raw, Graph: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Trail(root, "delivery-1", &out); err != nil {
		t.Fatal(err)
	}
	digest := grantDigest(routing.TaskGrant{Paths: []string{"docs/**"}})
	for _, want := range []string{"grant_replied", "execution=1", "turn=2", "request_id=req-1", "grant_digest=" + digest} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("trail lacks %q:\n%s", want, out.String())
		}
	}
}

func TestPanelSummarizesGrantReply(t *testing.T) {
	t.Parallel()

	records, graph, now := modelFixture(t)
	detail := grantRepliedDetail(1, "run-1", "May I edit docs?", "req-1", routing.TaskGrant{Paths: []string{"docs/**"}}, 1)
	records = append(records, panelRecord(t, KindGrantReplied, "task_2", now, detail, graph))

	model := PanelModel(records, now, "")
	digest := grantDigest(routing.TaskGrant{Paths: []string{"docs/**"}})
	for _, want := range []string{"grant_replied", "task_2", "turn=1", "grant_digest=" + digest} {
		if !strings.Contains(model.Detail.LastRecord, want) {
			t.Fatalf("task last record lacks %q: %q", want, model.Detail.LastRecord)
		}
	}
	if !strings.Contains(PanelModel(records, now, "task_2").Detail.LastRecord, "grant_replied") {
		t.Fatal("selected task detail must show the grant reply")
	}
	if model.Attention.Kind == "question" {
		t.Fatalf("a grant reply must not raise a question: %+v", model.Attention)
	}
}

func parkedQuestion(t *testing.T, f fixture, r *Runner) (string, map[string]any) {
	t.Helper()
	ask, err := os.ReadFile(filepath.Join(f.root, ".batuta", "asks", "greetings-task-1.md"))
	if err != nil {
		t.Fatal(err)
	}
	var detail map[string]any
	for _, record := range readJournal(t, f, r.Delivery()) {
		if record.TaskID == "task_1" && record.Kind == KindQuestion {
			detail = nil
			if err := json.Unmarshal(record.Detail, &detail); err != nil {
				t.Fatal(err)
			}
		}
	}
	if detail == nil {
		t.Fatal("no question_recorded detail for task_1")
	}
	return string(ask), detail
}

func TestLoopGrantParksOnExpansion(t *testing.T) {
	f, r, state := runGrantTurnScenario(t, "grant-expansion", nil, nil)
	if state != StateWaitingInput {
		t.Fatalf("state = %s, want waiting_input", state)
	}
	ask, detail := parkedQuestion(t, f, r)
	if detail["grant_park"] != "not_covered" {
		t.Fatalf("grant_park = %v, want not_covered", detail["grant_park"])
	}
	if beyond, _ := detail["beyond_grant"].([]any); len(beyond) != 2 || beyond[0] != "docs/extra.md" || beyond[1] != "install" {
		t.Fatalf("beyond_grant = %v", detail["beyond_grant"])
	}
	for _, want := range []string{"grant_park: not_covered", "beyond_grant: docs/extra.md, install"} {
		if !strings.Contains(ask, want) {
			t.Fatalf("ask file lacks %q:\n%s", want, ask)
		}
	}
}

func TestLoopGrantParksOnMalformedClause(t *testing.T) {
	f, r, state := runGrantTurnScenario(t, "grant-malformed", nil, nil)
	if state != StateWaitingInput {
		t.Fatalf("state = %s, want waiting_input", state)
	}
	ask, detail := parkedQuestion(t, f, r)
	if detail["grant_park"] != "malformed_clause" {
		t.Fatalf("grant_park = %v, want malformed_clause", detail["grant_park"])
	}
	if _, found := detail["beyond_grant"]; found {
		t.Fatalf("detail carries an item list: %v", detail)
	}
	if !strings.Contains(ask, "grant_park: malformed_clause") || strings.Contains(ask, "beyond_grant") {
		t.Fatalf("ask file:\n%s", ask)
	}
}

func TestLoopGrantParkReason(t *testing.T) {
	tests := []struct{ name, scenario, reason string }{
		{"turn cap", "grant-cap", "turn_cap"},
		{"repeated question", "grant-repeat", "repeat"},
		{"deadline", "grant-deadline", "deadline"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var configure func(*Options)
			var beforeRun func(*Runner)
			if test.scenario == "grant-deadline" {
				clock := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
				calls := 0
				configure = func(opts *Options) { opts.Now = func() time.Time { return clock } }
				beforeRun = func(r *Runner) {
					r.backend = grantTestBackend{backend: r.backend, after: func() {
						calls++
						if calls == 1 {
							clock = clock.Add(30 * time.Second)
						} else {
							clock = clock.Add(2 * time.Minute)
						}
					}}
				}
			}
			f, r, state := runGrantTurnScenario(t, test.scenario, configure, beforeRun)
			if state != StateWaitingInput {
				t.Fatalf("state = %s, want waiting_input", state)
			}
			ask, detail := parkedQuestion(t, f, r)
			if detail["grant_park"] != test.reason {
				t.Fatalf("grant_park = %v, want %s", detail["grant_park"], test.reason)
			}
			if _, found := detail["beyond_grant"]; found {
				t.Fatalf("detail carries an item list: %v", detail)
			}
			if !strings.Contains(ask, "grant_park: "+test.reason) {
				t.Fatalf("ask file lacks the reason:\n%s", ask)
			}
		})
	}
}

func TestLoopQuestionWithoutGrantUnchanged(t *testing.T) {
	isolateGrantCache(t)
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("ask", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	ask, detail := parkedQuestion(t, f, r)
	if len(detail) != 5 {
		t.Fatalf("detail keys = %v", detail)
	}
	for _, key := range []string{"execution", "run_id", "question", "request_id", "ask_path"} {
		if _, found := detail[key]; !found {
			t.Fatalf("detail lacks %q: %v", key, detail)
		}
	}
	// The worktree path in the ask file embeds the test name.
	if strings.Contains(strings.ToLower(strings.ReplaceAll(ask, f.root, "")), "grant") {
		t.Fatalf("ask file mentions a grant:\n%s", ask)
	}
}
