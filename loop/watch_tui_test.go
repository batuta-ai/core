package loop

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/core/routing"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"
)

func updateWatch(t *testing.T, m watchModel, msg tea.Msg) (watchModel, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	got, ok := updated.(watchModel)
	if !ok {
		t.Fatalf("Update returned %T", updated)
	}
	return got, cmd
}

func TestWatchModelKeys(t *testing.T) {
	records, graph, now := keyRecords(t)
	root := t.TempDir()
	m := newWatchModel(root, records, Style{Width: 120, Lang: "en", Glyphs: "ascii"}, func() time.Time { return now })
	m, _ = updateWatch(t, m, tea.WindowSizeMsg{Width: 120, Height: 31})
	if m.panel.Detail.Task != "task_2" {
		t.Fatal(m.panel.Detail.Task)
	}
	for _, step := range []struct {
		key  tea.KeyPressMsg
		want string
	}{
		{tea.KeyPressMsg{Code: tea.KeyDown}, "task_3"},
		{tea.KeyPressMsg{Code: tea.KeyUp}, "task_2"},
		{tea.KeyPressMsg{Code: tea.KeyUp}, "task_1"},
		{tea.KeyPressMsg{Code: tea.KeyUp}, "task_1"},
		{tea.KeyPressMsg{Code: 'f', Text: "f"}, "task_2"},
	} {
		m, _ = updateWatch(t, m, step.key)
		if m.panel.Detail.Task != step.want {
			t.Fatalf("%s selected %s, want %s", step.key, m.panel.Detail.Task, step.want)
		}
	}
	page := max(1, len(panelTaskIDs(m.viewport)))
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if want := fmt.Sprintf("task_%d", 2+page); m.panel.Detail.Task != want {
		t.Fatalf("pgdown selected %s, want %s", m.panel.Detail.Task, want)
	}
	for i := 0; i < 24; i++ {
		m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.panel.Detail.Task != "task_24" || m.viewport.RowsAbove == 0 {
		t.Fatal("down did not scroll to and stop at the last task")
	}
	page = max(1, len(panelTaskIDs(m.viewport)))
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if want := fmt.Sprintf("task_%d", 24-page); m.panel.Detail.Task != want {
		t.Fatalf("pgup selected %s, want %s", m.panel.Detail.Task, want)
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	for _, want := range []bool{true, false} {
		m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
		if m.navigation.legend != want || strings.Contains(m.View().Content, "Legend") != want {
			t.Fatalf("legend = %v, want %v", m.navigation.legend, want)
		}
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.navigation.notice != "" {
		t.Fatal("answer offered for running task")
	}
	graph.Tasks[1].State = routing.GraphTaskWaitingInput
	graph.Tasks[1].Attempts[0].Question = &routing.TaskQuestion{Prompt: "Which format?"}
	records = append(records, panelRecord(t, KindQuestion, "task_2", now, map[string]any{"execution": 1, "question": "Which format?"}, graph))
	m, _ = updateWatch(t, m, journalMsg{records: records})
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if want := panelAnswerCommand(root, "task_2"); m.navigation.notice != want {
		t.Fatalf("notice = %q, want %q", m.navigation.notice, want)
	}
	t.Setenv("PAGER", "")
	m, cmd := updateWatch(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	path := filepath.Join(root, ".batuta/runs/2026-09-06-demo-task-2-e1.out.log")
	if m.navigation.notice != path || cmd != nil {
		t.Fatalf("open without pager: notice=%q, command present=%v", m.navigation.notice, cmd != nil)
	}
	t.Setenv("PAGER", "less -R")
	m, cmd = updateWatch(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if m.navigation.notice != path || cmd == nil || fmt.Sprintf("%T", cmd()) != "tea.execMsg" {
		t.Fatal("open did not return an ExecProcess command and log path")
	}
	m, _ = updateWatch(t, m, pagerDoneMsg{err: errors.New("pager failed")})
	if !strings.Contains(m.navigation.notice, "pager failed") {
		t.Fatal(m.navigation.notice)
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m.navigation.notice = ""
	m, cmd = updateWatch(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if m.navigation.notice != "" || cmd != nil {
		t.Fatal("opened a log for a task without one")
	}
	for _, key := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		_, cmd := updateWatch(t, m, key)
		if cmd == nil {
			t.Fatalf("%s did not quit", key)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s did not return QuitMsg", key)
		}
	}
}

func TestWatchModelViewMatchesRender(t *testing.T) {
	for _, state := range []string{"calm", "question", "limit", "blocked", "conflict"} {
		t.Run(state, func(t *testing.T) {
			records, graph, now := modelFixture(t)
			switch state {
			case "question":
				graph.Tasks[1].State = routing.GraphTaskWaitingInput
				graph.Tasks[1].Attempts[0].Question = &routing.TaskQuestion{Prompt: "Which format?"}
				records = append(records, panelRecord(t, KindQuestion, "task_2", now, map[string]any{"execution": 1, "question": "Which format?"}, graph))
			case "limit":
				records = append(records, panelRecord(t, KindLimitWait, "task_2", now, map[string]any{"execution": 1, "wait": 1, "seconds": 60}, graph))
			case "blocked":
				graph.Tasks[1].State = routing.GraphTaskBlocked
				records = append(records, panelRecord(t, KindFailure, "task_2", now, map[string]any{"execution": 1, "blocked": true, "feedback": []string{"G2 tests failed"}}, graph))
			case "conflict":
				graph.Tasks[1].Attempts[0].Conflict = &routing.ConflictProof{IntegrationHeadSHA: "new-base"}
				graph.Tasks[1].Attempts = append(graph.Tasks[1].Attempts, routing.GraphTaskAttempt{Execution: 2, BaseHeadSHA: "new-base"})
				records = append(records, panelRecord(t, KindSettled, "task_2", now, map[string]any{"wave": 2, "final_head": "new-base", "conflict_task": "task_2"}, graph))
			}
			root := t.TempDir()
			for _, width := range []int{120, 80, 60} {
				style := Style{Width: width, Lang: "en", Glyphs: "ascii"}
				m := newWatchModel(root, nil, style, func() time.Time { return now })
				m, _ = updateWatch(t, m, journalMsg{records: records})
				m, _ = updateWatch(t, m, tea.WindowSizeMsg{Width: width, Height: 40})
				if m.style.Width != width || m.height != 40 {
					t.Fatalf("size = %d x %d", m.style.Width, m.height)
				}
				for _, legend := range []bool{false, true} {
					if legend {
						m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
						m, _ = updateWatch(t, m, pagerDoneMsg{err: errors.New("pager failed")})
					}
					nav := panelNavigation{}
					panel := PanelModel(records, now, "")
					if err := loadPanelLog(root, &panel); err != nil {
						t.Fatal(err)
					}
					height, suffix := 40, ""
					if legend {
						suffix = panelLegend(style) + m.navigation.notice + "\n"
						height -= panelLineCount(panelLegend(style)) + 1
					}
					want := Render(nav.viewport(panel, style, height), style) + suffix
					got := m.View()
					if !got.AltScreen || got.Content != want {
						t.Fatalf("width %d legend %v: View differs from Render\ngot:\n%s\nwant:\n%s", width, legend, got.Content, want)
					}
				}
			}
		})
	}
}

func TestWatchModelJournalFollowsAndPreservesSelection(t *testing.T) {
	records, graph, now := modelFixture(t)
	m := newWatchModel(t.TempDir(), records, Style{Width: 120}, func() time.Time { return now })
	graph.Tasks[1].State = routing.GraphTaskIntegrated
	graph.Tasks[2].State = routing.GraphTaskRunning
	graph.Tasks[2].Attempts = []routing.GraphTaskAttempt{{Execution: 1}}
	fresh := append(records, panelRecord(t, KindStarted, "task_3", now, map[string]any{"execution": 1}, graph))
	m, _ = updateWatch(t, m, journalMsg{records: fresh})
	if m.panel.Detail.Task != "task_3" {
		t.Fatal("fresh journal did not follow active task")
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m, _ = updateWatch(t, m, journalMsg{records: fresh})
	if m.panel.Detail.Task != "task_2" {
		t.Fatal("fresh journal discarded explicit selection")
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	if m.panel.Detail.Task != "task_3" || m.navigation.selected != "" {
		t.Fatal("follow did not resume")
	}
	m, _ = updateWatch(t, m, journalMsg{})
	if m.View().Content != Render(PanelModel(nil, now, ""), m.style) {
		t.Fatal("empty journal retained stale view")
	}
}

func TestWatchProgramNavigates(t *testing.T) {
	records, _, now := modelFixture(t)
	m := newWatchModel(t.TempDir(), records, Style{Width: 120, Lang: "en", Glyphs: "ascii"}, func() time.Time { return now })
	m.navigation.selected = "task_1"
	m, _ = updateWatch(t, m, journalMsg{records: records})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { _ = tm.Quit() })
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: '?', Text: "?"})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("Legend")) })
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(time.Second))
	final := tm.FinalModel(t).(watchModel)
	if final.navigation.selected != "task_3" || !final.navigation.legend || strings.Count(final.View().Content, "Legend") != 1 {
		t.Fatalf("final navigation: %+v", final.navigation)
	}
}

func TestWatchProgramPager(t *testing.T) {
	t.Setenv("PAGER", "batuta-nonexistent-pager -R")
	records, _, now := modelFixture(t)
	root := t.TempDir()
	path := panelLogPath(root, PanelModel(records, now, ""))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("executor output\n"), 0644); err != nil {
		t.Fatal(err)
	}
	m := newWatchModel(root, records, Style{Width: 120, Lang: "en", Glyphs: "ascii"}, func() time.Time { return now })
	if !strings.Contains(m.View().Content, "executor output") {
		t.Fatal("selected log not loaded")
	}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { _ = tm.Quit() })
	tm.Send(tea.KeyPressMsg{Code: 'o', Text: "o"})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("executable file not found")) })
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(time.Second))
	if notice := tm.FinalModel(t).(watchModel).navigation.notice; !strings.Contains(notice, "batuta-nonexistent-pager") {
		t.Fatalf("pager error missing: %q", notice)
	}
}

func TestWatchQuitsOnTerminalState(t *testing.T) {
	for _, test := range []struct {
		name      string
		state     string
		attention string
	}{
		{name: "done", state: StateDone},
		{name: "waiting input", state: StateWaitingInput, attention: "Which format?"},
		{name: "blocked", state: StateBlocked, attention: "G2 tests failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			records, graph, now := modelFixture(t)
			switch test.state {
			case StateWaitingInput:
				graph.Tasks[1].State = routing.GraphTaskWaitingInput
				graph.Tasks[1].Attempts[0].Question = &routing.TaskQuestion{Prompt: test.attention}
				records = append(records, panelRecord(t, KindQuestion, "task_2", now, map[string]any{"execution": 1, "question": test.attention}, graph))
			case StateBlocked:
				graph.Tasks[1].State = routing.GraphTaskBlocked
				graph.Tasks[1].BlockerCode = "gates_failed"
				records = append(records, panelRecord(t, KindFailure, "task_2", now, map[string]any{"execution": 1, "blocked": true, "feedback": []string{test.attention}}, graph))
			}
			records = append(records, panelRecord(t, KindTerminal, "", now, map[string]any{"state": test.state}, graph))
			m := newWatchModel(t.TempDir(), nil, Style{Width: 120, Lang: "en", Glyphs: "ascii"}, func() time.Time { return now })
			m, cmd := updateWatch(t, m, journalMsg{records: records})

			if test.attention == "" {
				if cmd == nil {
					t.Fatal("terminal journal did not quit")
				}
				if _, ok := cmd().(tea.QuitMsg); !ok {
					t.Fatalf("terminal journal command emitted %T", cmd())
				}
				return
			}
			if cmd != nil {
				if _, quit := cmd().(tea.QuitMsg); quit {
					t.Fatalf("%s journal quit", test.state)
				}
			}
			if !strings.Contains(m.View().Content, test.attention) {
				t.Fatalf("attention line missing from view:\n%s", m.View().Content)
			}
			_, cmd = updateWatch(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
			if cmd == nil {
				t.Fatal("q did not quit")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("q command emitted %T", cmd())
			}
		})
	}
}
