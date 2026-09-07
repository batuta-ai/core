package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func answerWatch(t *testing.T) (watchModel, *journal.Store) {
	t.Helper()
	records, graph, now := keyRecords(t)
	graph.Tasks[1].State = routing.GraphTaskWaitingInput
	graph.Tasks[1].Attempts[0].Question = &routing.TaskQuestion{Prompt: "Which format?"}
	records = append(records, panelRecord(t, KindQuestion, "task_2", now, map[string]any{"execution": 1, "question": "Which format?"}, graph))
	root, store := keyStore(t, records)
	return newWatchModel(root, records, Style{Width: 120, Lang: "en", Glyphs: "ascii"}, func() time.Time { return now }), store
}

func resumableAnswerWatch(t *testing.T) watchModel {
	t.Helper()
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("ask", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("park question: %s, %v", state, err)
	}
	records := readJournal(t, f, r.Delivery())
	return newPollingWatchModel(f.root, r.Delivery(), r.store, records, time.Second, Style{Width: 120, Lang: "en", Glyphs: "ascii"}, time.Now, nil)
}

func TestAnswerOverlayOpens(t *testing.T) {
	m, _ := answerWatch(t)
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'l', Text: "l"})
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if !m.answering || !m.answerEditor.Focused() || m.answerQuestion != "Which format?" {
		t.Fatal("answer editor did not open with the selected question and focus")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 80, Height: 12}, {Width: 60, Height: 8}, {Width: 620, Height: 40}} {
		m, _ = updateWatch(t, m, size)
		if m.answerEditor.Width() != size.Width-4 || m.answerEditor.Height() < 1 || m.answerEditor.Height() > min(8, size.Height-2) {
			t.Fatalf("editor size = %dx%d for %dx%d", m.answerEditor.Width(), m.answerEditor.Height(), size.Width, size.Height)
		}
		view := m.View().Content
		if panelLineCount(view) > size.Height || !strings.Contains(view, "Which format?") {
			t.Fatalf("overlay does not fit: %dx%d\n%s", size.Width, size.Height, view)
		}
	}
	selected, focus := m.panel.Detail.Task, m.focus
	for _, key := range []string{"q", "l", "f", "r", "R", "?", "o"} {
		var cmd tea.Cmd
		m, cmd = updateWatch(t, m, tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		if cmd != nil {
			if _, quit := cmd().(tea.QuitMsg); quit {
				t.Fatal("typing q quit the watch")
			}
		}
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = updateWatch(t, m, tea.PasteMsg{Content: "second line"})
	m, _ = updateWatch(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if got := m.answerEditor.Value(); got != "qlfrR?o\nsecond line" || m.panel.Detail.Task != selected || m.focus != focus || m.navigation.legend {
		t.Fatalf("editor keys escaped to dashboard: value=%q selected=%q focus=%q", got, m.panel.Detail.Task, m.focus)
	}
}

func TestAnswerOverlayKeyLine(t *testing.T) {
	for _, lang := range []string{"en", "pt", "unknown"} {
		m, _ := answerWatch(t)
		m.style.Lang = lang
		m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
		want, placeholder := "ctrl+enter / alt+enter / ctrl+s send · esc cancel · enter newline", "Type your answer..."
		if lang == "pt" {
			want, placeholder = "ctrl+enter / alt+enter / ctrl+s envia · esc cancela · enter nova linha", "Digite sua resposta..."
		}
		view := m.View()
		if !strings.Contains(view.Content, want) || m.answerEditor.Placeholder != placeholder {
			t.Fatalf("missing localized editor help or placeholder: %q\n%s", m.answerEditor.Placeholder, view.Content)
		}
		if !view.KeyboardEnhancements.ReportEventTypes {
			t.Fatal("view did not request keyboard enhancements")
		}
	}
}

func TestAnswerOverlaySubmits(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEnter, Mod: tea.ModCtrl}, {Code: tea.KeyEnter, Mod: tea.ModAlt}, {Code: 's', Mod: tea.ModCtrl}} {
		t.Run(key.String(), func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			r, err := New(context.Background(), f.options("ask", &out))
			if err != nil {
				t.Fatal(err)
			}
			if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
				t.Fatalf("park question: %s, %v", state, err)
			}
			records := readJournal(t, f, r.Delivery())
			m := newWatchModel(f.root, records, Style{Width: 120, Lang: "en"}, nil)
			m.spawn = func([]string, string, string) error { return nil }
			m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
			m, _ = updateWatch(t, m, tea.PasteMsg{Content: "  first line\nsecond line  \n"})
			m, _ = updateWatch(t, m, key)
			if m.answering || m.answerEditor.Focused() {
				t.Fatalf("submission did not close editor: %s", m.navigation.notice)
			}
			after := readJournal(t, f, r.Delivery())
			if len(after) != len(records)+1 || after[len(after)-1].Kind != KindAnswer {
				t.Fatal("submission must append exactly one answer before resuming")
			}
			var detail struct{ Answer string }
			if err := json.Unmarshal(after[len(after)-1].Detail, &detail); err != nil || detail.Answer != "first line\nsecond line" {
				t.Fatalf("recorded answer = %q, %v", detail.Answer, err)
			}
		})
	}
}

func TestAnswerResumesDetached(t *testing.T) {
	m := resumableAnswerWatch(t)
	var argv []string
	var dir, logPath string
	m.spawn = func(gotArgv []string, gotDir, gotLogPath string) error {
		argv = append([]string(nil), gotArgv...)
		dir, logPath = gotDir, gotLogPath
		return nil
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateWatch(t, m, tea.PasteMsg{Content: "use JSON"})
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{exe, "loop", "--resume", m.delivery}; !slices.Equal(argv, want) {
		t.Fatalf("argv = %q, want %q (answering=%v notice=%q)", argv, want, m.answering, m.navigation.notice)
	}
	if dir != m.workspace {
		t.Fatalf("cwd = %q, want %q", dir, m.workspace)
	}
	if want := filepath.Join(m.workspace, ".batuta", "runs", "loop-"+m.delivery+".log"); logPath != want {
		t.Fatalf("log path = %q, want %q", logPath, want)
	}
}

func TestAnswerResumeErrorNotice(t *testing.T) {
	m := resumableAnswerWatch(t)
	m.spawn = func([]string, string, string) error { return errors.New("spawn denied") }
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateWatch(t, m, tea.PasteMsg{Content: "use JSON"})
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantCommand := strings.Join([]string{exe, "loop", "--resume", m.delivery}, " ")
	if !m.answering || m.answerEditor.Value() != "use JSON" || !strings.Contains(m.navigation.notice, "spawn denied") || !strings.Contains(m.navigation.notice, wantCommand) {
		t.Fatalf("spawn failure lost draft or manual command: %q", m.navigation.notice)
	}
}

func TestWatchFollowsResumedLoop(t *testing.T) {
	m := resumableAnswerWatch(t)
	delivery := m.delivery
	m.spawn = func([]string, string, string) error {
		writePresenceFixture(t, m.workspace, delivery, m.currentTime)
		return nil
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateWatch(t, m, tea.PasteMsg{Content: "use JSON"})
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	msg := m.pollCmd()()
	m, _ = updateWatch(t, m, msg)

	if m.delivery != delivery || m.panel.Header.Presence != "running" || !strings.Contains(m.View().Content, "loop ●") {
		t.Fatalf("watch stopped following resumed delivery: delivery=%q header=%+v", m.delivery, m.panel.Header)
	}
}

func TestAnswerOverlayRefusesEmpty(t *testing.T) {
	m, store := answerWatch(t)
	before, _ := store.Read("demo")
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateWatch(t, m, tea.PasteMsg{Content: " \n\t "})
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	after, _ := store.Read("demo")
	if !m.answering || !m.answerEditor.Focused() || !strings.Contains(m.navigation.notice, "empty") || len(after) != len(before) || !strings.Contains(m.View().Content, m.navigation.notice) {
		t.Fatalf("empty answer not refused visibly: %q", m.navigation.notice)
	}
}

func TestAnswerOverlayCancels(t *testing.T) {
	m, store := answerWatch(t)
	before, _ := store.Read("demo")
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateWatch(t, m, tea.PasteMsg{Content: "discard this"})
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	after, _ := store.Read("demo")
	if m.answering || m.answerEditor.Focused() || len(after) != len(before) || strings.Contains(m.View().Content, "ctrl+s send") {
		t.Fatal("escape did not cancel without recording")
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.answerEditor.Value() != "" {
		t.Fatal("canceled draft survived reopening")
	}
	_, cmd := updateWatch(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not return QuitMsg")
	}
}

func TestAnswerCommandStillPrinted(t *testing.T) {
	m, _ := answerWatch(t)
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	if m.answering || m.navigation.notice != panelAnswerCommand(m.workspace, "task_2") {
		t.Fatal("R did not print the command for the waiting task")
	}
	m.panel.Detail.Question = ""
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.answering || m.navigation.notice != panelAnswerCommand(m.workspace, "task_2") {
		t.Fatal("r without a question did not print the command")
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	if m.answering || m.navigation.notice != panelAnswerCommand(m.workspace, "task_3") {
		t.Fatal("R did not print the command for a nonwaiting task")
	}
}

func TestAnswerOverlaySnapshotExcluded(t *testing.T) {
	m, store := answerWatch(t)
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	var output bytes.Buffer
	if err := Snapshot(m.workspace, "demo", &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "ctrl+s") || strings.Contains(output.String(), "Type your answer") {
		t.Fatal("snapshot rendered an editor")
	}
	output.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &keyFrameWriter{write: func(frame string) {
		output.WriteString(frame)
		cancel()
	}}
	if err := watchPlain(ctx, m.workspace, "demo", store, writer, nil); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 || strings.Contains(output.String(), "ctrl+s") || strings.Contains(output.String(), "Type your answer") {
		t.Fatal("plain watch rendered an editor or no frame")
	}
}

func TestAnswerOverlayPreservesDraftOnError(t *testing.T) {
	m, store := answerWatch(t)
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateWatch(t, m, tea.PasteMsg{Content: "keep this draft"})
	if _, err := store.Append("demo", panelRecord(t, KindTerminal, "", m.currentTime, map[string]any{"state": StateDone}, routing.DeliveryGraph{})); err != nil {
		t.Fatal(err)
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !m.answering || m.answerEditor.Value() != "keep this draft" || !strings.Contains(m.navigation.notice, "no open delivery") {
		t.Fatalf("failed submission lost draft or error: %q", m.navigation.notice)
	}
}
