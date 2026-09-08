package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

type panelCallbackWriter struct {
	builder strings.Builder
	once    sync.Once
	fn      func()
}

func (w *panelCallbackWriter) Write(p []byte) (int, error) {
	w.once.Do(w.fn)
	return w.builder.Write(p)
}

func (w *panelCallbackWriter) String() string { return w.builder.String() }

func panelRecord(t *testing.T, kind journal.Kind, task string, at time.Time, detail any, graph routing.DeliveryGraph) journal.Record {
	t.Helper()
	d, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	g, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	return journal.Record{Kind: kind, TaskID: task, At: at, Detail: d, Graph: g}
}

func panelFixture(t *testing.T) ([]journal.Record, routing.DeliveryGraph, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 6, 3, 4, 12, 0, time.UTC)
	attempt := func(execution int, model string) []routing.GraphTaskAttempt {
		return []routing.GraphTaskAttempt{{Execution: execution, Runtime: routing.RuntimeValue{Provider: "codex", Model: model}}}
	}
	graph := routing.DeliveryGraph{
		Tasks: []routing.GraphTask{
			{TaskID: "task_1", Domain: "backend", Complexity: "low", State: routing.GraphTaskIntegrated, Attempts: attempt(1, "fake-low"), IntegratedCommitSHA: "440c12b123456"},
			{TaskID: "task_2", Domain: "backend", Complexity: "low", State: routing.GraphTaskRunning, Attempts: attempt(2, "fake-low")},
			{TaskID: "task_3", Domain: "backend", Complexity: "medium", State: routing.GraphTaskPending, Dependencies: []string{"task_1", "task_2"}},
		},
		Waves: []routing.DeliveryWave{{Number: 1}, {Number: 2}},
	}
	records := []journal.Record{
		panelRecord(t, KindOpened, "", now.Add(-252*time.Second), map[string]any{"slug": "greetings", "branch": "main", "head": "4e2651c123456"}, routing.DeliveryGraph{}),
		panelRecord(t, KindStarted, "task_2", now.Add(-151*time.Second), map[string]any{"execution": 2}, graph),
		panelRecord(t, KindGates, "task_2", now, map[string]any{"execution": 1, "passed": false, "scope": map[string]any{"name": "scope", "pass": false, "signal": "outside.txt"}}, graph),
	}
	return records, graph, now
}

func TestRenderPanelLayout(t *testing.T) {
	t.Parallel()
	records, _, now := panelFixture(t)
	before, _ := json.Marshal(records)
	got := RenderPanel(records, now)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	want := []string{
		"delivery greetings branch main @ 4e2651c wave 2 elapsed 04:12",
		"task lane executor/model exec state detail",
		"task_1 backend/low codex/fake-low 1 integrated commit 440c12b",
		"task_2 backend/low codex/fake-low 2 running 02:31 · 0 items · gate —",
		"task_3 backend/medium - - pending after task_1, task_2",
		"last gates_reported task_2 e1 passed=false (scope: outside.txt)",
	}
	if len(lines) != len(want) {
		t.Fatalf("panel has %d lines, want %d:\n%s", len(lines), len(want), got)
	}
	for i, line := range lines {
		if normalized := strings.Join(strings.Fields(line), " "); normalized != want[i] {
			t.Errorf("line %d = %q, want %q", i, normalized, want[i])
		}
	}
	for _, column := range []string{"backend/", "codex/"} {
		if strings.Index(lines[2], column) != strings.Index(lines[3], column) {
			t.Errorf("column %q is not aligned:\n%s", column, got)
		}
	}
	after, _ := json.Marshal(records)
	if string(after) != string(before) || RenderPanel(records, now) != got {
		t.Fatal("rendering must be pure and deterministic")
	}
}

func TestPanelHeaderShowsThePhase(t *testing.T) {
	t.Parallel()
	records, _, now := panelFixture(t)
	records[0] = panelRecord(t, KindOpened, "", now.Add(-252*time.Second), map[string]any{
		"slug": "greetings", "roadmap": "Greetings delivery", "phase": 1,
		"phase_title": "Build greetings", "branch": "main", "head": "4e2651c123456",
	}, routing.DeliveryGraph{})
	if got := RenderPanel(records, now); !strings.Contains(got, "phase 1 · Build greetings") {
		t.Fatalf("panel header does not show phase:\n%s", got)
	}
}

func TestRenderPanelShowsCriterionProgress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		gate any
		want string
	}{
		{"no current gate", nil, "gate —"},
		{"passed", map[string]any{"execution": 2, "passed": true}, "gate ok"},
		{"failed", map[string]any{"execution": 2, "passed": false}, "gate fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records, graph, now := panelFixture(t)
			for _, event := range []struct {
				task                 string
				execution, criterion int
				state                string
			}{
				{"task_2", 1, 3, "DONE"},
				{"task_1", 2, 3, "DONE"},
				{"task_2", 2, 1, "START"},
				{"task_2", 2, 1, "DONE"},
				{"task_2", 2, 1, "DONE"},
				{"task_2", 2, 2, "DONE"},
				{"task_2", 2, 3, "START"},
				{"task_2", 2, 0, "DONE"},
			} {
				records = append(records, panelRecord(t, KindProgress, event.task, now, map[string]any{"execution": event.execution, "criterion": event.criterion, "state": event.state}, graph))
			}
			if tc.gate != nil {
				records = append(records, panelRecord(t, KindGates, "task_2", now, tc.gate, graph))
			}
			got := RenderPanel(records, now)
			if !strings.Contains(got, "02:31 · 2 items · "+tc.want) {
				t.Fatalf("progress must count unique DONE criteria for the current task/execution:\n%s", got)
			}
		})
	}
}

func TestDashboardTSVUnchanged(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	graph := routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskPending}}}
	at := time.Date(2026, 9, 6, 12, 34, 56, 0, time.Local)
	if _, err := store.Append("demo", panelRecord(t, KindOpened, "", at, map[string]any{"slug": "demo"}, graph)); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%-10s%-7s%-8s%-12s%-16s%-6s%-10s%s\n", "delivery", "state", "task", "task_state", "executor/model", "exec", "worktree", "updated") +
		fmt.Sprintf("%-10s%-7s%-8s%-12s%-16s%-6s%-10s%s\n", "demo", "open", "task_1", "pending", "", "0", "", "12:34:56")
	for _, delivery := range []string{"demo", ""} {
		var got strings.Builder
		if err := Dashboard(root, delivery, &got); err != nil {
			t.Fatal(err)
		}
		if got.String() != want {
			t.Fatalf("Dashboard = %q, want %q", got.String(), want)
		}
	}
}

func TestPanelSnapshotSelectsOpenOrExplicitDoneDelivery(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("BATUTA_LANG", "en")
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Snapshot(root, "", &output); err != nil || output.String() != "no open deliveries\n" {
		t.Fatalf("empty snapshot = %q, %v", output.String(), err)
	}
	records, graph, now := modelFixture(t)
	for _, delivery := range []string{"done-demo", "open-demo"} {
		opened := panelRecord(t, KindOpened, "", now, openedDetail{Slug: delivery, Workspace: root}, graph)
		if _, err := store.Append(delivery, opened); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(delivery, records[4]); err != nil {
			t.Fatal(err)
		}
		if delivery == "done-demo" {
			if _, err := store.Append(delivery, panelRecord(t, KindTerminal, "", now, map[string]any{"state": StateDone}, graph)); err != nil {
				t.Fatal(err)
			}
		}
	}
	writePanelLogAt(t, root, "2026-09-06-demo-task-2-e1", []string{"snapshot log content"})
	for _, delivery := range []string{"", "done-demo"} {
		output.Reset()
		if err := Snapshot(root, delivery, &output); err != nil {
			t.Fatal(err)
		}
		want := delivery
		if want == "" {
			want = "open-demo"
		}
		got := output.String()
		if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
			t.Fatalf("snapshot must end with exactly one newline: %q", got)
		}
		if strings.Count(got, "batuta watch ·") != 1 || !strings.Contains(got, want) || !strings.Contains(got, "snapshot log content") || strings.Contains(got, "\x1b[") {
			t.Fatalf("snapshot %q = %q", delivery, got)
		}
	}
}

func tallPanelFixture() PanelView {
	model := renderFixture("calm")
	model.Waves = []PanelWave{{Number: 1, State: "running", Total: 18}}
	for i := 1; i <= 18; i++ {
		model.Waves[0].Rows = append(model.Waves[0].Rows, PanelRow{Task: fmt.Sprintf("task_%d", i), State: "running"})
	}
	model.LogLines = make([]string, 40)
	for i := range model.LogLines {
		model.LogLines[i] = fmt.Sprintf("line %03d", i)
	}
	return model
}

func TestFitPanelHeightShrinksLogFirst(t *testing.T) {
	for _, width := range []int{80, 120} {
		for _, offset := range []int{0, 5} {
			model := tallPanelFixture()
			style := Style{Width: width, LogOffset: offset}
			fullHeight := panelLineCount(Render(model, style))
			visible := 3
			if width == 120 {
				visible = 6
			}
			for shrink := 1; shrink <= visible; shrink++ {
				height := fullHeight - shrink
				fitted := fitPanelHeight(model, style, height)
				if got := panelLineCount(Render(fitted, style)); got != height {
					t.Errorf("width %d offset %d: height=%d, want %d", width, offset, got, height)
				}
				wantRows := panelTableRows(model) - max(0, shrink-(visible-2))
				if got := panelTableRows(fitted); got != wantRows {
					t.Errorf("width %d shrink %d: table rows=%d, want %d", width, shrink, got, wantRows)
				}
				if got := len(fitted.LogLines) - offset; got != max(2, visible-shrink) {
					t.Errorf("visible log lines=%d, want %d", got, max(2, visible-shrink))
				}
				if !strings.Contains(Render(fitted, style), model.LogLines[len(model.LogLines)-offset-1]) {
					t.Error("fitting lost the visible log tail")
				}
				if len(model.LogLines) != 40 || panelTableRows(model) != 19 {
					t.Fatal("fitting modified the source model")
				}
			}
		}
	}
}

func TestReadPanelLogKeepsLast200Lines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	lines := make([]string, 205)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %03d", i)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readPanelLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 200 || got[0] != "line 005" || got[199] != "line 204" {
		t.Fatalf("log tail: len=%d first=%q last=%q", len(got), got[0], got[len(got)-1])
	}
}

func TestWatchStopsAtTerminalState(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 12, 34, 56, 0, time.UTC)
	graph := routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskRunning}}}
	if _, err := store.Append("demo", panelRecord(t, KindOpened, "", now, map[string]any{"slug": "demo"}, graph)); err != nil {
		t.Fatal(err)
	}
	var appendErr error
	out := &panelCallbackWriter{fn: func() {
		_, appendErr = store.Append("demo", panelRecord(t, KindTerminal, "", now.Add(time.Second), map[string]any{"state": StateDone}, graph))
	}}
	if err := Watch(context.Background(), root, "demo", time.Millisecond, out); err != nil {
		t.Fatal(err)
	}
	if appendErr != nil {
		t.Fatal(appendErr)
	}
	if !strings.HasSuffix(out.String(), "\n") || strings.HasSuffix(out.String(), "\n\n") {
		t.Fatal("no-TTY watch must end with exactly one newline")
	}
	if got := strings.Count(out.String(), "batuta watch"); got != 2 || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("rendered %d frames, want 2 plain snapshots:\n%s", got, out.String())
	}
	finalRecords, err := store.Read("demo")
	if err != nil {
		t.Fatal(err)
	}
	style, height := watchPanelSize(out)
	expected, err := renderWatchPanel(root, finalRecords, now.Add(time.Second), style, height)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), expected) {
		t.Fatalf("terminal panel was not rendered:\n%s", out.String())
	}
}

func TestRenderPanelJournalDetails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		kind   journal.Kind
		detail any
		want   string
	}{
		{"progress summary", KindProgress, map[string]any{"execution": 2, "criterion": 4, "state": "DONE"}, "last task_progress task_2 execution=2 criterion=4 state=DONE"},
		{"passed gate summary", KindGates, map[string]any{"execution": 2, "passed": true}, "last gates_reported task_2 e2 passed=true"},
		{"settled head", KindSettled, map[string]any{"final_head": "7654321abcdef"}, "branch main @ 7654321"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records, graph, now := panelFixture(t)
			records = append(records, panelRecord(t, tc.kind, "task_2", now, tc.detail, graph))
			got := strings.Join(strings.Fields(RenderPanel(records, now)), " ")
			if !strings.Contains(got, tc.want) {
				t.Fatalf("panel does not contain %q:\n%s", tc.want, got)
			}
		})
	}
}

func TestWatchShowsTheRunLogTail(t *testing.T) {
	t.Parallel()
	records, graph, now := panelFixture(t)
	records = append(records, panelRecord(t, KindStarted, "task_2", now, map[string]any{
		"execution": 2, "run_id": "demo-task-2-e2", "log_path": ".batuta/runs/2026-09-06-demo-task-2-e2.out.log",
	}, graph))
	root := writePanelLog(t, "2026-09-06-demo-task-2-e2", []string{"old 1", "old 2", "line 3", "line 4", "line 5", "line 6", "line 7", "line 8"})

	got, err := renderWatchPanel(root, records, now, Style{Width: 120, Lang: "en", Glyphs: "unicode"}, 40)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Log  .batuta/runs/2026-09-06-demo-task-2-e2.out.log", "line 3", "line 4", "line 5", "line 6", "line 7", "line 8"} {
		if !strings.Contains(got, want) {
			t.Errorf("log tail missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old 1") || strings.Contains(got, "old 2") {
		t.Fatalf("log panel did not keep only the last six lines:\n%s", got)
	}

	writePanelLogAt(t, root, "2026-09-06-demo-task-2-e2", []string{"fresh redraw"})
	got, err = renderWatchPanel(root, records, now, Style{Width: 120, Lang: "en", Glyphs: "unicode"}, 40)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "fresh redraw") || strings.Contains(got, "line 8") {
		t.Fatalf("redraw did not reread the run log:\n%s", got)
	}
}

func TestWatchLogPanelAfterIntegration(t *testing.T) {
	t.Parallel()
	records, graph, now := panelFixture(t)
	graph.Tasks[1].State = routing.GraphTaskIntegrated
	graph.Tasks[1].IntegratedCommitSHA = "7654321abcdef"
	records = append(records,
		panelRecord(t, KindStarted, "task_2", now, map[string]any{"execution": 2, "run_id": "demo-task-2-e2", "log_path": ".batuta/runs/demo-task-2-e2.out.log"}, graph),
		panelRecord(t, KindTerminal, "", now.Add(time.Second), map[string]any{"state": StateDone}, graph),
	)
	root := writePanelLog(t, "demo-task-2-e2", []string{"executor finished", "final proof passed"})

	got, err := renderWatchPanel(root, records, now.Add(time.Second), Style{Width: 120, Lang: "en", Glyphs: "unicode"}, 40)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Recent log · task-2-e2", "executor finished", "final proof passed"} {
		if !strings.Contains(got, want) {
			t.Errorf("integrated task log missing %q:\n%s", want, got)
		}
	}
}

func TestWatchShrinkOrder(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 6, 3, 4, 12, 0, time.UTC)
	tasks := make([]routing.GraphTask, 8)
	summaries := make([]map[string]any, 8)
	ids := make([]string, 8)
	for i := range tasks {
		id := fmt.Sprintf("task_%d", i+1)
		ids[i] = id
		tasks[i] = routing.GraphTask{TaskID: id, State: routing.GraphTaskPending}
		summaries[i] = map[string]any{"task_id": id, "title": "Dashboard task"}
	}
	tasks[0].State = routing.GraphTaskRunning
	tasks[0].Attempts = []routing.GraphTaskAttempt{{Execution: 1}}
	graph := routing.DeliveryGraph{Tasks: tasks, Waves: []routing.DeliveryWave{{Number: 1, TaskIDs: ids}}}
	records := []journal.Record{
		panelRecord(t, KindOpened, "", now.Add(-time.Minute), map[string]any{"slug": "demo", "workspace": "/projects/core", "tasks": summaries}, graph),
		panelRecord(t, KindStarted, "task_1", now, map[string]any{"execution": 1, "run_id": "demo-task-1-e1", "log_path": ".batuta/runs/demo-task-1-e1.out.log"}, graph),
	}
	root := writePanelLog(t, "demo-task-1-e1", []string{"log 1", "log 2", "log 3", "log 4", "log 5", "log 6"})

	for _, tc := range []struct {
		name          string
		width, height int
		logs          []string
		absent        []string
	}{
		{"wide", 120, 40, []string{"log 1", "log 6"}, nil},
		{"short", 120, 31, []string{"log 5", "log 6"}, []string{"log 1"}},
		{"compact", 80, 31, []string{"log 4", "log 6"}, []string{"log 3", "Commit"}},
		{"narrow", 60, 31, nil, []string{"Recent log", "log 6"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderWatchPanel(root, records, now, Style{Width: tc.width, Lang: "en", Glyphs: "unicode"}, tc.height)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.logs {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("unexpected %q:\n%s", absent, got)
				}
			}
			if tc.height < 40 && strings.Count(got, "\n") > tc.height {
				t.Errorf("rendered %d lines into height %d", strings.Count(got, "\n"), tc.height)
			}
			if tc.width >= 76 {
				shown := 0
				for _, id := range ids {
					if strings.Contains(got, id) {
						shown++
					}
				}
				if shown < 1 {
					t.Errorf("table kept only %d task rows:\n%s", shown, got)
				}
			}
		})
	}
}

func writePanelLog(t *testing.T, runID string, lines []string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writePanelLogAt(t, root, runID, lines)
	return root
}

func writePanelLogAt(t *testing.T, root, runID string, lines []string) {
	t.Helper()
	dir := filepath.Join(root, ".batuta", "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runID+".out.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
