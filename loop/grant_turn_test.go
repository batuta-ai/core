package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func runGrantedQuestion(t *testing.T, scenario string) (fixture, *Runner, string) {
	t.Helper()
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
