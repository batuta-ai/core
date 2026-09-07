package loop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func TestWatchLogFocusScrolls(t *testing.T) {
	records, _, now := modelFixture(t)
	m := newWatchModel(t.TempDir(), records, Style{Width: 120, Lang: "en", Glyphs: "unicode"}, func() time.Time { return now })
	m.panel.LogLines = []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}
	m.viewport = m.navigation.viewport(m.panel, m.renderStyle(), m.viewportHeight())

	if got := m.View().Content; !strings.Contains(got, "Waves and tasks ·") || strings.Contains(got, "Recent log · task-2-e1 ·") {
		t.Fatalf("initial focus marker is not on the table:\n%s", got)
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'l', Text: "l"})
	if got := m.View().Content; m.focus != focusLog || !strings.Contains(got, "Recent log ·") {
		t.Fatalf("log focus missing: focus=%q\n%s", m.focus, got)
	}

	for _, step := range []struct {
		key  tea.KeyPressMsg
		want int
	}{
		{tea.KeyPressMsg{Code: tea.KeyUp}, 1},
		{tea.KeyPressMsg{Code: tea.KeyPgUp}, 2},
		{tea.KeyPressMsg{Code: tea.KeyDown}, 1},
		{tea.KeyPressMsg{Code: tea.KeyPgDown}, 0},
		{tea.KeyPressMsg{Code: tea.KeyUp}, 1},
		{tea.KeyPressMsg{Code: tea.KeyEnd}, 0},
	} {
		m, _ = updateWatch(t, m, step.key)
		if m.logOffset != step.want {
			t.Fatalf("%s offset=%d, want %d", step.key, m.logOffset, step.want)
		}
	}

	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'l', Text: "l"})
	if m.focus != focusTable || m.logOffset != 0 {
		t.Fatalf("table focus=%q offset=%d", m.focus, m.logOffset)
	}
}

func TestWatchLogFollowsTailUntilScrolled(t *testing.T) {
	records, _, now := modelFixture(t)
	m := newWatchModel(t.TempDir(), records, Style{Width: 120}, func() time.Time { return now })
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'l', Text: "l"})
	m, _ = updateWatch(t, m, journalMsg{records: records, logLoaded: true, logLines: []string{"one", "two", "three", "four", "five", "old", "tail"}})
	if m.logOffset != 0 || !strings.Contains(m.View().Content, "tail") {
		t.Fatal("tail-following journal update did not show the new tail")
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m, _ = updateWatch(t, m, journalMsg{records: records, logLoaded: true, logLines: []string{"zero", "one", "two", "three", "four", "five", "old", "tail", "new"}})
	if m.logOffset != 1 || strings.Contains(m.View().Content, "new") {
		t.Fatalf("journal update lost scroll position: offset=%d\n%s", m.logOffset, m.View().Content)
	}
}

func TestWatchMouseWheel(t *testing.T) {
	records, _, now := modelFixture(t)
	m := newWatchModel(t.TempDir(), records, Style{Width: 120}, func() time.Time { return now })
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode=%v", m.View().MouseMode)
	}
	m, _ = updateWatch(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.panel.Detail.Task != "task_3" {
		t.Fatalf("table wheel selected %q", m.panel.Detail.Task)
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'l', Text: "l"})
	m.panel.LogLines = []string{"one", "two", "three", "four", "five", "six", "seven"}
	m, _ = updateWatch(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.logOffset != 1 {
		t.Fatalf("log wheel offset=%d", m.logOffset)
	}
	m, _ = updateWatch(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.logOffset != 0 {
		t.Fatalf("log wheel down offset=%d", m.logOffset)
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
					interactiveStyle := m.renderStyle()
					nav := panelNavigation{}
					panel := PanelModel(records, now, "")
					if err := loadPanelLog(root, &panel); err != nil {
						t.Fatal(err)
					}
					height, suffix := 40, ""
					if legend {
						suffix = panelLegend(interactiveStyle) + m.navigation.notice + "\n"
						height -= panelLineCount(panelLegend(interactiveStyle)) + 1
					}
					want := Render(nav.viewport(panel, interactiveStyle, height), interactiveStyle) + suffix
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
	if m.View().Content != Render(PanelModel(nil, now, ""), m.renderStyle()) {
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

func TestWatchRunsTheProgram(t *testing.T) {
	records, _, _ := modelFixture(t)
	root, store := keyStore(t, records)
	previousTerminal, previousProgram := isTerminal, newWatchProgram
	t.Cleanup(func() { isTerminal, newWatchProgram = previousTerminal, previousProgram })
	isTerminal = func(fd uintptr) bool { return fd == os.Stdin.Fd() }
	for _, exit := range []string{"q", "ctrl+c", "context"} {
		t.Run(exit, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var buf bytes.Buffer
			started, handled := false, false
			newWatchProgram = func(model tea.Model, options ...tea.ProgramOption) *tea.Program {
				started = true
				m, ok := model.(watchModel)
				if !ok || m.store == nil || m.delivery != "demo" || !strings.Contains(m.View().Content, "View model") {
					t.Fatalf("program model: %#v", model)
				}
				input := "q"
				if exit == "ctrl+c" {
					input = "\x03"
				}
				options = append(options, tea.WithInput(strings.NewReader(input)), tea.WithOutput(&buf), tea.WithoutRenderer(),
					tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
						if _, ok := msg.(tea.KeyPressMsg); ok {
							handled = true
							if exit == "context" {
								cancel()
								return nil
							}
						}
						return msg
					}))
				return tea.NewProgram(model, options...)
			}
			if err := Watch(ctx, root, "demo", time.Hour, &buf); err != nil {
				t.Fatal(err)
			}
			if !started || !handled {
				t.Fatal("Watch did not run the program and process input")
			}
		})
	}
	after, err := store.Read("demo")
	if err != nil || len(after) != len(records) {
		t.Fatalf("watch changed journal: %v", err)
	}
}

func TestWatchProgramAlreadyDone(t *testing.T) {
	records, graph, now := modelFixture(t)
	records = append(records, panelRecord(t, KindTerminal, "", now, map[string]any{"state": StateDone}, graph))
	model := newWatchModel(t.TempDir(), records, Style{Width: 120}, func() time.Time { return now })
	model.ticker = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { _ = tm.Quit() })
	tm.WaitFinished(t, teatest.WithFinalTimeout(time.Second))
	if final := tm.FinalModel(t).(watchModel); final.panel.Header.State != StateDone {
		t.Fatalf("final state: %s", final.panel.Header.State)
	}
}

func TestWatchSpinnerTicksOnlyWhileRunning(t *testing.T) {
	records, graph, now := modelFixture(t)
	var durations []time.Duration
	m := newWatchModel(t.TempDir(), records, Style{Width: 120, Lang: "en", Glyphs: "unicode"}, func() time.Time { return now })
	m.ticker = immediateTicker(now, &durations)

	if cmd := m.Init(); cmd == nil {
		t.Fatal("running model did not start animation")
	}
	if !slices.Contains(durations, 80*time.Millisecond) {
		t.Fatalf("initial tick durations = %v", durations)
	}
	before := m.style.Frame
	m, cmd := updateWatch(t, m, spinnerTickMsg{})
	if m.style.Frame != before+1 || cmd == nil {
		t.Fatalf("spinner tick: frame=%d command=%v", m.style.Frame, cmd != nil)
	}
	if got := durations[len(durations)-1]; got != 80*time.Millisecond {
		t.Fatalf("spinner rescheduled after %v", got)
	}

	graph.Tasks[1].State = routing.GraphTaskIntegrated
	stopped := append(records, panelRecord(t, KindProgress, "task_2", now, map[string]any{"execution": 1, "criterion": 3, "state": "DONE"}, graph))
	m, _ = updateWatch(t, m, journalMsg{records: stopped})
	count := len(durations)
	frame := m.style.Frame
	m, cmd = updateWatch(t, m, spinnerTickMsg{})
	if cmd != nil || len(durations) != count || m.style.Frame != frame {
		t.Fatalf("idle spinner tick: frame=%d durations=%v command=%v", m.style.Frame, durations, cmd != nil)
	}

	idle := newWatchModel(t.TempDir(), nil, Style{Width: 120}, func() time.Time { return now })
	idle.ticker = immediateTicker(now, &durations)
	count = len(durations)
	if cmd := idle.Init(); cmd == nil {
		t.Fatal("idle model did not retain its clock command")
	}
	if slices.Contains(durations[count:], 80*time.Millisecond) {
		t.Fatalf("idle model scheduled spinner: %v", durations[count:])
	}
	idle, cmd = updateWatch(t, idle, journalMsg{records: records})
	if cmd == nil || durations[len(durations)-1] != 80*time.Millisecond {
		t.Fatalf("running journal did not start spinner: durations=%v command=%v", durations, cmd != nil)
	}
}

func TestWatchProgressEases(t *testing.T) {
	records, graph, now := modelFixture(t)
	var durations []time.Duration
	m := newWatchModel(t.TempDir(), records, Style{Width: 120}, func() time.Time { return now })
	m.ticker = immediateTicker(now, &durations)
	if m.panel.Progress.WavesShown != 1 || m.panel.Progress.TasksShown != 1 {
		t.Fatalf("initial shown progress = %+v", m.panel.Progress)
	}

	graph.Tasks[1].State = routing.GraphTaskIntegrated
	graph.Tasks[2].State = routing.GraphTaskRunning
	graph.Tasks[2].Attempts = []routing.GraphTaskAttempt{{Execution: 1}}
	fresh := append(records, panelRecord(t, KindStarted, "task_3", now, map[string]any{"execution": 1}, graph))
	m, cmd := updateWatch(t, m, journalMsg{records: fresh})
	if cmd == nil || durations[len(durations)-1] != 30*time.Millisecond {
		t.Fatalf("progress change did not start easing: durations=%v command=%v", durations, cmd != nil)
	}
	if m.panel.Progress.WavesShown != 1 || m.panel.Progress.TasksShown != 1 {
		t.Fatalf("progress jumped before first frame: %+v", m.panel.Progress)
	}

	for frame := 1; frame <= 12; frame++ {
		m, cmd = updateWatch(t, m, progressTickMsg{})
		want := 1 + float64(frame)/12
		if m.panel.Progress.WavesShown != want || m.panel.Progress.TasksShown != want {
			t.Fatalf("frame %d progress = %.12g/%.12g, want %.12g", frame, m.panel.Progress.WavesShown, m.panel.Progress.TasksShown, want)
		}
		if (frame < 12) != (cmd != nil) {
			t.Fatalf("frame %d command present=%v", frame, cmd != nil)
		}
	}
	if m.panel.Progress.WavesShown != float64(m.panel.Progress.WavesDone) || m.panel.Progress.TasksShown != float64(m.panel.Progress.TasksDone) {
		t.Fatalf("final values are not exact: %+v", m.panel.Progress)
	}
}
