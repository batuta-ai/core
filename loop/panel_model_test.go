package loop

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func modelFixture(t *testing.T) ([]journal.Record, routing.DeliveryGraph, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	graph := routing.DeliveryGraph{
		Tasks: []routing.GraphTask{
			{TaskID: "task_1", State: routing.GraphTaskIntegrated, IntegratedCommitSHA: "commit-one", Attempts: []routing.GraphTaskAttempt{{Execution: 1}}},
			{TaskID: "task_2", State: routing.GraphTaskRunning, Attempts: []routing.GraphTaskAttempt{{Execution: 1, Runtime: routing.RuntimeValue{Provider: "codex", Model: "small"}}}},
			{TaskID: "task_3", State: routing.GraphTaskPending},
		},
		Waves: []routing.DeliveryWave{{Number: 1, BaseHeadSHA: "initial", TaskIDs: []string{"task_1"}}, {Number: 2, BaseHeadSHA: "commit-one", TaskIDs: []string{"task_2"}}},
	}
	records := []journal.Record{
		panelRecord(t, KindOpened, "", now.Add(-5*time.Minute), openedDetail{Slug: "dashboard", Workspace: "/projects/core", Branch: "feature", Head: "initial", Roadmap: "Dashboard v2", Phase: 2, Tasks: []taskSummary{{ID: "task_1", Title: "First"}, {ID: "task_2", Title: "View model"}, {ID: "task_3", Title: "Renderer"}}}, graph),
		panelRecord(t, KindStarted, "task_1", now.Add(-4*time.Minute), map[string]any{"execution": 1, "executor": "codex", "model": "small"}, graph),
		panelRecord(t, KindSettled, "", now.Add(-3*time.Minute), map[string]any{"wave": 1, "final_head": "commit-one"}, graph),
		panelRecord(t, KindWorktree, "task_2", now.Add(-2*time.Minute), map[string]any{"execution": 1, "worktree": attemptWorktree{Root: "/work/task_2"}}, graph),
		panelRecord(t, KindStarted, "task_2", now.Add(-time.Minute), map[string]any{"execution": 1, "run_id": "demo-task-2-e1", "log_path": ".batuta/runs/2026-09-06-demo-task-2-e1.out.log", "executor": "codex", "model": "small", "reasoning": "medium", "worktree": "/work/task_2"}, graph),
		panelRecord(t, KindProgress, "task_2", now.Add(-10*time.Second), map[string]any{"execution": 1, "criterion": 2, "state": "START"}, graph),
	}
	return records, graph, now
}

func TestPanelModelSummarisesTheJournal(t *testing.T) {
	records, graph, now := modelFixture(t)
	before, _ := json.Marshal(records)
	model := PanelModel(records, now, "")
	if model.Header.Delivery != "dashboard" || model.Header.Project != "core" || model.Header.Branch != "feature" || model.Header.Head != "commit-one" || model.Header.Elapsed != 5*time.Minute || model.Header.State != "running" || model.Header.Roadmap != "Dashboard v2" || model.Header.Phase != 2 {
		t.Fatalf("header: %+v", model.Header)
	}
	if model.Attention.Kind != "none" {
		t.Fatalf("attention: %+v", model.Attention)
	}
	if model.Context.Executor != "codex" || model.Context.Model != "small" || model.Context.Reasoning != "medium" || model.Context.Sessions != 2 || model.Context.Retries != 0 || model.Context.Escalations != 0 {
		t.Fatalf("context: %+v", model.Context)
	}
	if model.Context.TestCommand != "" || model.Context.Sandbox != "" || model.Context.PID != 0 || model.Health.TreeDirty != nil || model.Health.JournalAge != 10*time.Second {
		t.Fatalf("unknown fields / health: %+v %+v", model.Context, model.Health)
	}
	if model.Progress.WavesDone != 1 || model.Progress.WavesTotal != 2 || model.Progress.TasksDone != 1 || model.Progress.TasksTotal != 3 {
		t.Fatalf("progress: %+v", model.Progress)
	}
	if model.Waves[0].Number != 1 || model.Waves[0].Base != "initial" || model.Waves[0].Integrated != "commit-one" || model.Waves[0].Done != 1 || model.Waves[0].Total != 1 || model.Waves[0].State != "integrated" || model.Waves[0].Rows[0].Commit != "commit-one" {
		t.Fatalf("wave: %+v", model.Waves[0])
	}
	if model.Waves[1].Rows[0].Title != "View model" || model.Waves[1].Rows[0].Attempt != 1 || model.Waves[1].State != "running" {
		t.Fatalf("active wave: %+v", model.Waves[1])
	}
	after, _ := json.Marshal(records)
	if string(before) != string(after) || !reflect.DeepEqual(model, PanelModel(records, now, "")) {
		t.Fatal("model must be pure")
	}
	for _, state := range []string{"question", "limit", "blocked", "conflict", "escalated"} {
		t.Run(state, func(t *testing.T) {
			records, graph, now := modelFixture(t)
			task := &graph.Tasks[1]
			kind, detail, wantState, text := KindQuestion, map[string]any{"execution": 1, "question": "Which format?"}, "waiting_input", "Which format?"
			switch state {
			case "question":
				task.State = routing.GraphTaskWaitingInput
				task.Attempts[0].Question = &routing.TaskQuestion{Prompt: "Which format?"}
			case "limit":
				kind = KindLimitWait
				detail = map[string]any{"execution": 1, "wait": 1, "seconds": 60, "reset_at": now.Add(time.Minute)}
				wantState = "limit_wait"
				text = "wait 1"
			case "blocked":
				task.State = routing.GraphTaskBlocked
				task.BlockerCode = "gates_failed"
				task.Attempts = append(task.Attempts, routing.GraphTaskAttempt{Execution: 2, Runtime: routing.RuntimeValue{Provider: "codex", Model: "large"}})
				records = append(records, panelRecord(t, KindFailure, "task_2", now.Add(-8*time.Second), map[string]any{"execution": 1, "blocked": false, "same_runtime": false, "next_runtime": task.Attempts[1].Runtime}, graph))
				kind = KindFailure
				detail = map[string]any{"execution": 2, "blocked": true, "feedback": []string{"G2 tests failed"}}
				wantState = "blocked"
				text = "G2 tests failed"
			case "conflict":
				task.Attempts[0].Conflict = &routing.ConflictProof{IntegrationHeadSHA: "new-base"}
				task.Attempts = append(task.Attempts, routing.GraphTaskAttempt{Execution: 2, BaseHeadSHA: "new-base"})
				kind = KindSettled
				detail = map[string]any{"wave": 2, "final_head": "new-base", "conflict_task": "task_2"}
				wantState = "running"
				text = "new-base"
			case "escalated":
				task.State = routing.GraphTaskPreparing
				task.Attempts = append(task.Attempts, routing.GraphTaskAttempt{Execution: 2, Runtime: routing.RuntimeValue{Provider: "codex", Model: "large"}})
				kind = KindFailure
				detail = map[string]any{"execution": 1, "blocked": false, "same_runtime": false, "next_runtime": task.Attempts[1].Runtime}
				wantState = "running"
				text = "large"
			}
			records = append(records, panelRecord(t, kind, "task_2", now, detail, graph))
			got := PanelModel(records, now, "")
			if got.Attention.Kind != state || got.Attention.Task != "task_2" || !strings.Contains(got.Attention.Text, text) || got.Attention.Hint == "" || got.Header.State != wantState {
				t.Fatalf("state: %+v %+v", got.Header, got.Attention)
			}
			if state == "blocked" && (got.Context.Escalations != 1 || got.Health.LastError != "G2 tests failed") {
				t.Fatalf("escalation/health: %+v %+v", got.Context, got.Health)
			}
			if state == "conflict" && (got.Detail.Attempt != 2 || got.Progress.WavesTotal != 2 || got.Context.Retries != 0) {
				t.Fatalf("conflict: %+v", got)
			}
		})
	}
	records = append(records, panelRecord(t, KindTerminal, "", now, map[string]any{"state": StateDone}, graph))
	if got := PanelModel(records, now.Add(time.Hour), ""); got.Header.State != StateDone || got.Header.Elapsed != 5*time.Minute || got.Attention.Kind != "none" {
		t.Fatalf("terminal: %+v", got)
	}
	records[0] = panelRecord(t, KindOpened, "", now, map[string]any{"slug": "old"}, graph)
	if got := PanelModel(records[:1], now.Add(-time.Second), ""); got.Header.Roadmap != "" || got.Header.Phase != 0 || got.Header.Project != "" || got.Header.Elapsed != 0 {
		t.Fatalf("optional header: %+v", got.Header)
	}
}

func TestPanelModelDetailFollowsTheSelection(t *testing.T) {
	records, graph, now := modelFixture(t)
	got := PanelModel(records, now, "")
	if got.Detail.Task != "task_2" || got.Detail.Title != "View model" || got.Detail.Attempt != 1 || got.Detail.Criterion != 2 || got.Detail.CriterionState != "START" || got.Detail.Worktree != "/work/task_2" || got.Detail.LogPath != ".batuta/runs/2026-09-06-demo-task-2-e1.out.log" || got.Detail.LastAge != 10*time.Second || !strings.Contains(got.Detail.LastRecord, "task_progress") {
		t.Fatalf("active detail: %+v", got.Detail)
	}
	selected := PanelModel(records, now, "task_1")
	if selected.Detail.Task != "task_1" || selected.Detail.Title != "First" || selected.Detail.LogPath != "" || selected.Detail.Criterion != 0 || selected.Detail.LastAge != 4*time.Minute || selected.Context != got.Context {
		t.Fatalf("selected detail/context: %+v", selected)
	}
	if got := PanelModel(records, now, "missing"); got.Detail.Task != "" {
		t.Fatalf("unknown selection: %+v", got.Detail)
	}
	// A new attempt must not inherit the old progress, log, or question.
	graph.Tasks[1].Attempts = append(graph.Tasks[1].Attempts, routing.GraphTaskAttempt{Execution: 2})
	records = append(records, panelRecord(t, KindWorktree, "task_2", now, map[string]any{"execution": 2, "worktree": attemptWorktree{Root: "/work/new"}}, graph))
	got = PanelModel(records, now, "")
	if got.Detail.Attempt != 2 || got.Detail.Criterion != 0 || got.Detail.LogPath != "" || got.Detail.Worktree != "/work/new" {
		t.Fatalf("fresh detail: %+v", got.Detail)
	}
	records = append(records, panelRecord(t, KindProgress, "task_2", now, map[string]any{"execution": 1, "criterion": 9, "state": "DONE"}, graph))
	if got := PanelModel(records, now, ""); got.Detail.Criterion != 0 {
		t.Fatalf("stale progress: %+v", got.Detail)
	}
}

func TestPanelModelOlderJournalLeavesLogPathUnknown(t *testing.T) {
	records, graph, now := modelFixture(t)
	records[4] = panelRecord(t, KindStarted, "task_2", now.Add(-time.Minute), map[string]any{"execution": 1, "run_id": "demo-task-2-e1"}, graph)
	model := PanelModel(records, now, "")
	if model.Detail.LogPath != "" {
		t.Fatalf("guessed log path: %q", model.Detail.LogPath)
	}
	if err := loadPanelLog(t.TempDir(), &model); err != nil || len(model.LogLines) != 0 {
		t.Fatalf("older journal log = %v, %v", model.LogLines, err)
	}
}

func TestPanelModelGroupsTasksIntegratedBeforeRun(t *testing.T) {
	records, graph, now := modelFixture(t)
	graph.Waves = graph.Waves[1:]
	records = append(records, panelRecord(t, KindProgress, "task_2", now, map[string]any{"execution": 1, "criterion": 2, "state": "DONE"}, graph))
	model := PanelModel(records, now, "")
	if len(model.Waves) != 3 {
		t.Fatalf("waves: %+v", model.Waves)
	}
	before, pending := model.Waves[1], model.Waves[2]
	if before.State != "before_run" || before.Number != 0 || before.Total != 1 || before.Done != 1 || len(before.Rows) != 1 || before.Rows[0].Task != "task_1" {
		t.Fatalf("before-run wave: %+v", before)
	}
	if pending.State != "pending" || pending.Total != 1 || pending.Done != 0 || len(pending.Rows) != 1 || pending.Rows[0].Task != "task_3" {
		t.Fatalf("pending wave: %+v", pending)
	}
	if model.Progress.WavesTotal != 1 || model.Progress.WavesDone != 0 || model.Progress.TasksDone != 1 {
		t.Fatalf("progress includes synthetic waves: %+v", model.Progress)
	}
}

func TestPanelModelGateColumns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report gates.Report
		want   [4]string
	}{
		{"pending", gates.Report{}, [4]string{"pending", "pending", "pending", "pending"}},
		{"pass", gates.Report{Finished: gates.Verdict{Name: "finished", Pass: true}, Tree: gates.Verdict{Name: "tree", Pass: true}, Tests: gates.Verdict{Name: "tests", Pass: true}, Scope: gates.Verdict{Name: "scope", Pass: true}}, [4]string{"pass", "pass", "pass", "pass"}},
		{"silent and failures", gates.Report{Finished: gates.Verdict{Name: "finished"}, Tree: gates.Verdict{Name: "tree", Pass: true, Signal: "silent: the session wrote nothing"}, Tests: gates.Verdict{Name: "tests"}, Scope: gates.Verdict{Name: "scope", Pass: true}, Proofs: []gates.Verdict{{Name: "criterion 1", Pass: false}}}, [4]string{"fail", "silent", "fail", "fail"}},
		{"verifier failure", gates.Report{Scope: gates.Verdict{Name: "scope", Pass: true}, Verifier: &gates.Verdict{Name: "verifier"}}, [4]string{"pending", "pending", "pending", "fail"}},
		{"missing scope", gates.Report{Verifier: &gates.Verdict{Name: "verifier", Pass: true}}, [4]string{"pending", "pending", "pending", "pending"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records, graph, now := modelFixture(t)
			tc.report.Execution = 1
			records = append(records, panelRecord(t, KindGates, "task_2", now, tc.report, graph))
			if got := PanelModel(records, now, "").Waves[1].Rows[0].Gates; got != tc.want {
				t.Fatalf("gates=%v want %v", got, tc.want)
			}
			// Latest report wins, but only for the latest attempt and matching task.
			records = append(records, panelRecord(t, KindGates, "task_1", now, gates.Report{Execution: 1}, graph))
			if got := PanelModel(records, now, "").Waves[1].Rows[0].Gates; got != tc.want {
				t.Fatalf("other task changed gates: %v", got)
			}
			graph.Tasks[1].Attempts = append(graph.Tasks[1].Attempts, routing.GraphTaskAttempt{Execution: 2})
			records = append(records, panelRecord(t, KindStarted, "task_2", now, map[string]any{"execution": 2}, graph))
			want := [4]string{"pending", "pending", "pending", "pending"}
			if got := PanelModel(records, now, "").Waves[1].Rows[0].Gates; got != want {
				t.Fatalf("stale gates: %v", got)
			}
		})
	}
}

func TestPanelTSVCompatibility(t *testing.T) {
	records, _, now := panelFixture(t)
	want := "delivery greetings   branch main @ 4e2651c   wave 2   elapsed 04:12\n" +
		"task    lane            executor/model  exec  state       detail\n" +
		"task_1  backend/low     codex/fake-low  1     integrated  commit 440c12b\n" +
		"task_2  backend/low     codex/fake-low  2     running     02:31 · 0 items · gate —\n" +
		"task_3  backend/medium  -               -     pending     after task_1, task_2\n" +
		"last     gates_reported task_2 e1 passed=false (scope: outside.txt)\n"
	if got := RenderPanel(records, now); got != want {
		t.Fatalf("TSV changed:\n%q\nwant:\n%q", got, want)
	}
	if got := RenderPanel(nil, now); got != "no records\n" {
		t.Fatalf("empty: %q", got)
	}
	records[len(records)-1].Graph = json.RawMessage("invalid")
	if got := RenderPanel(records, now); got != "invalid graph\n" {
		t.Fatalf("invalid: %q", got)
	}
}

func TestPanelModelLifecycle(t *testing.T) {
	t.Run("retry waves stay in the attempt column", func(t *testing.T) {
		records, graph, now := modelFixture(t)
		graph.Tasks[1].Attempts = append(graph.Tasks[1].Attempts, routing.GraphTaskAttempt{Execution: 2})
		graph.Waves = append(graph.Waves, routing.DeliveryWave{Number: 3, BaseHeadSHA: "commit-one", TaskIDs: []string{"task_2"}})
		records = append(records, panelRecord(t, KindFailure, "task_2", now.Add(-5*time.Second), map[string]any{"execution": 1, "same_runtime": true, "feedback": []string{"tests failed"}}, graph))
		records = append(records, panelRecord(t, KindStarted, "task_2", now, map[string]any{"execution": 2, "executor": "codex", "model": "small"}, graph))
		got := PanelModel(records, now, "")
		if got.Progress.WavesTotal != 2 || len(got.Waves) != 3 || got.Waves[1].Rows[0].Attempt != 2 || got.Context.Sessions != 3 || got.Context.Retries != 1 || got.Context.Escalations != 0 {
			t.Fatalf("retry changed phase counts: %+v", got)
		}
		if !strings.Contains(RenderPanel(records, now), "wave 3") {
			t.Fatal("TSV must retain the engine wave count")
		}
		graph.Tasks[1].State = routing.GraphTaskIntegrated
		graph.Tasks[1].IntegratedCommitSHA = "retry-commit"
		records = append(records, panelRecord(t, KindSettled, "", now, map[string]any{"wave": 3, "final_head": "retry-commit"}, graph))
		got = PanelModel(records, now, "")
		if got.Progress.WavesDone != 2 || got.Waves[1].Integrated != "retry-commit" || got.Waves[1].Rows[0].Commit != "retry-commit" {
			t.Fatalf("retry settlement: %+v", got)
		}
	})
	t.Run("limit clears with executor activity", func(t *testing.T) {
		records, graph, now := modelFixture(t)
		records = append(records, panelRecord(t, KindLimitWait, "task_2", now, map[string]any{"execution": 1, "wait": 1}, graph))
		records = append(records, panelRecord(t, KindProgress, "task_1", now, map[string]any{"execution": 1, "criterion": 1, "state": "DONE"}, graph))
		if got := PanelModel(records, now, ""); got.Attention.Kind != "limit" || got.Context.Retries != 0 {
			t.Fatalf("parallel limit: %+v", got)
		}
		records = append(records, panelRecord(t, KindProgress, "task_2", now, map[string]any{"execution": 1, "criterion": 2, "state": "DONE"}, graph))
		if got := PanelModel(records, now, ""); got.Attention.Kind != "none" || got.Detail.CriterionState != "DONE" {
			t.Fatalf("resumed progress: %+v", got)
		}
	})
	t.Run("latest gate report replaces earlier report", func(t *testing.T) {
		records, graph, now := modelFixture(t)
		records = append(records, panelRecord(t, KindGates, "task_2", now, gates.Report{Execution: 1, Tests: gates.Verdict{Name: "tests", Pass: false}}, graph))
		records = append(records, panelRecord(t, KindGates, "task_2", now, gates.Report{Execution: 1, Tests: gates.Verdict{Name: "tests", Pass: true}}, graph))
		if got := PanelModel(records, now, "").Waves[1].Rows[0].Gates[2]; got != "pass" {
			t.Fatalf("latest report: %s", got)
		}
	})
}

func TestPanelModelCarriesPendingDependenciesWithoutInventingMetadata(t *testing.T) {
	records, graph, now := modelFixture(t)
	graph.Tasks[2].Dependencies = []string{"task_1", "task_2"}
	graph.Tasks = append(graph.Tasks, routing.GraphTask{TaskID: "task_4", State: routing.GraphTaskPending, Dependencies: []string{"task_2"}})
	records = append(records, panelRecord(t, KindProgress, "task_2", now, map[string]any{"execution": 1, "criterion": 2, "state": "START"}, graph))
	model := PanelModel(records, now, "")
	pending := model.Waves[len(model.Waves)-1]
	if pending.Number != 0 || !reflect.DeepEqual(pending.Dependencies, []string{"task_1", "task_2"}) {
		t.Fatalf("pending dependencies: %+v", pending)
	}
	if model.Detail.AttemptLimit != 0 || model.Detail.CriterionTotal != 0 || model.Detail.CriterionTitle != "" || model.LogTitle != "" || len(model.LogLines) != 0 {
		t.Fatalf("invented metadata: %+v", model)
	}
}
