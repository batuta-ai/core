package loop

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

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
