package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
	"github.com/charmbracelet/x/term"
)

func keyRecords(t *testing.T) ([]journal.Record, routing.DeliveryGraph, time.Time) {
	t.Helper()
	records, graph, now := modelFixture(t)
	for i := 4; i <= 24; i++ {
		graph.Tasks = append(graph.Tasks, routing.GraphTask{TaskID: fmt.Sprintf("task_%d", i), State: routing.GraphTaskPending})
	}
	records = append(records, panelRecord(t, KindProgress, "task_2", now, map[string]any{"execution": 1, "criterion": 1, "state": "START"}, graph))
	return records, graph, now
}

func keyStore(t *testing.T, records []journal.Record) (string, *journal.Store) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if _, err := store.Append("demo", record); err != nil {
			t.Fatal(err)
		}
	}
	return root, store
}

func TestPanelKeysScrollAndQuit(t *testing.T) {
	records, _, now := keyRecords(t)
	state := panelNavigation{}
	model := state.model(records, now)
	if model.Detail.Task != "task_2" {
		t.Fatal(model.Detail.Task)
	}
	for _, step := range []struct{ key, want string }{
		{"down", "task_3"}, {"up", "task_2"}, {"pageDown", "task_9"}, {"pageUp", "task_2"},
		{"pageDown", "task_9"}, {"pageDown", "task_16"},
	} {
		state.move(step.key, model, 7)
		model = state.model(records, now)
		if model.Detail.Task != step.want {
			t.Fatalf("%s selected %s, want %s", step.key, model.Detail.Task, step.want)
		}
	}
	for _, width := range []int{120, 80, 60} {
		view := state.viewport(model, Style{Width: width, Lang: "en", Glyphs: "ascii"}, 31)
		if view.RowsAbove == 0 || view.RowsBelow == 0 {
			t.Fatalf("scroll counts: %+v", view)
		}
		found := false
		for _, wave := range view.Waves {
			for _, row := range wave.Rows {
				found = found || row.Task == "task_16"
			}
		}
		if !found {
			t.Fatal("selected row scrolled out of view")
		}
	}
	state.move("f", model, 7)
	if state.model(records, now).Detail.Task != "task_2" {
		t.Fatal("follow did not relock")
	}
}

func TestPanelKeysLegendStyles(t *testing.T) {
	for _, style := range []Style{{Width: 60, Lang: "pt", Glyphs: "ascii"}, {Width: 120, Lang: "en", Glyphs: "unicode"}} {
		legend := panelLegend(style)
		if style.Lang == "pt" && !strings.Contains(legend, "Legenda") {
			t.Fatal(legend)
		}
		if style.Glyphs == "ascii" && strings.ContainsAny(legend, "✓✗┌│└") {
			t.Fatal(legend)
		}
	}
}

func TestRenderLegendMentionsColours(t *testing.T) {
	for _, test := range []struct {
		lang string
		want []string
	}{
		{lang: "en", want: []string{"green · integrated", "blue · running", "red · blocked", "yellow · waiting answer", "dim · pending", "reverse · selected task"}},
		{lang: "pt", want: []string{"verde · integrada", "azul · em execução", "vermelho · bloqueada", "amarelo · aguarda resposta", "atenuado · pendente", "invertido · task selecionada"}},
	} {
		t.Run(test.lang, func(t *testing.T) {
			legend := panelLegend(Style{Width: 120, Lang: test.lang, Glyphs: "unicode"})
			for _, want := range test.want {
				if strings.Count(legend, want) != 1 {
					t.Errorf("legend contains %q %d times, want once:\n%s", want, strings.Count(legend, want), legend)
				}
			}
		})
	}
}

func TestRenderLegendMentionsPresence(t *testing.T) {
	for _, test := range []struct {
		lang string
		want []string
	}{
		{lang: "en", want: []string{"loop ● shown delivery running", "loop ○ no loop", "loop ○ stale lock expired", "N loops = fresh locks in workspace"}},
		{lang: "pt", want: []string{"loop ● entrega exibida em execução", "loop ○ sem loop", "loop ○ stale lock expirado", "N loops = locks recentes no workspace"}},
	} {
		t.Run(test.lang, func(t *testing.T) {
			legend := panelLegend(Style{Width: 120, Lang: test.lang, Glyphs: "unicode"})
			for _, want := range test.want {
				if !strings.Contains(legend, want) {
					t.Errorf("legend is missing %q:\n%s", want, legend)
				}
			}
		})
	}
}

func TestRenderLegendMentionsAnswerKeys(t *testing.T) {
	for _, test := range []struct {
		lang string
		want string
	}{
		{lang: "en", want: "enter send · ctrl+j newline · esc cancel"},
		{lang: "pt", want: "enter envia · ctrl+j nova linha · esc cancela"},
	} {
		t.Run(test.lang, func(t *testing.T) {
			legend := panelLegend(Style{Width: 120, Lang: test.lang, Glyphs: "unicode"})
			if !strings.Contains(legend, test.want) {
				t.Fatalf("legend is missing %q:\n%s", test.want, legend)
			}
		})
	}
}

type keyFrameWriter struct{ write func(string) }

func (w *keyFrameWriter) Write(p []byte) (int, error) { w.write(string(p)); return len(p), nil }

func TestWatchWithoutATTYAutoFollows(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	if term.IsTerminal(input.Fd()) {
		t.Fatal("pipe was treated as a terminal")
	}
	previous := os.Stdin
	os.Stdin = input
	t.Cleanup(func() { os.Stdin = previous })
	records, graph, now := modelFixture(t)
	root, store := keyStore(t, records)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frames []string
	output := &keyFrameWriter{write: func(frame string) {
		frames = append(frames, frame)
		if len(frames) == 1 {
			graph.Tasks[1].State = routing.GraphTaskIntegrated
			graph.Tasks[2].State = routing.GraphTaskRunning
			graph.Tasks[2].Attempts = []routing.GraphTaskAttempt{{Execution: 1}}
			_, err := store.Append("demo", panelRecord(t, KindStarted, "task_3", now, map[string]any{"execution": 1}, graph))
			if err != nil {
				t.Fatal(err)
			}
		} else if len(frames) == 2 {
			if _, err := store.Append("demo", panelRecord(t, KindTerminal, "", now, map[string]any{"state": StateDone}, graph)); err != nil {
				t.Fatal(err)
			}
		}
	}}
	if err := Watch(ctx, root, "demo", time.Nanosecond, output); err != nil {
		t.Fatal(err)
	}
	for i, frame := range frames {
		if strings.Contains(frame, "\x1b") {
			t.Fatal("plain watch emitted escape sequences")
		}
		if i > 0 && !strings.HasPrefix(frame, "\n") {
			t.Fatal("frames need a blank line separator")
		}
	}
	if len(frames) != 3 || !strings.Contains(frames[0], "View model") || !strings.Contains(frames[1], "Renderer") {
		t.Fatalf("did not follow: %v", frames)
	}
}

func TestWatchPlainSkipsUnchangedJournal(t *testing.T) {
	records, graph, now := modelFixture(t)
	root, store := keyStore(t, records)
	ticks := make(chan time.Time)
	frames := make(chan string, 8)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output := &keyFrameWriter{write: func(frame string) { frames <- frame }}
	go func() { done <- watchPlain(ctx, root, "demo", store, output, ticks) }()
	select {
	case <-frames:
	case <-ctx.Done():
		t.Fatal("missing first frame")
	}
	writePanelLogAt(t, root, "2026-09-06-demo-task-2-e1", []string{"changed log only"})
	for range 2 {
		select {
		case ticks <- now:
		case <-ctx.Done():
			t.Fatal("poll did not finish")
		}
	}
	select {
	case frame := <-frames:
		t.Fatalf("unchanged journal rendered again: %s", frame)
	default:
	}
	if _, err := store.Append("demo", panelRecord(t, KindTerminal, "", now, map[string]any{"state": StateDone}, graph)); err != nil {
		t.Fatal(err)
	}
	select {
	case ticks <- now:
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("terminal journal did not stop watch")
	}
	if ctx.Err() != nil {
		t.Fatal("watch waited for cancellation instead of stopping on completion")
	}
	if len(frames) != 1 {
		t.Fatalf("want one changed frame, got %d", len(frames))
	}
	if frame := <-frames; !strings.HasPrefix(frame, "\n") || strings.Contains(frame, "\x1b") {
		t.Fatalf("not a plain separated frame: %q", frame)
	}
}

type panelErrorWriter struct{ err error }

func (w panelErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWatchPlainReturnsWriteError(t *testing.T) {
	records, _, _ := modelFixture(t)
	root, store := keyStore(t, records)
	failure := errors.New("write failed")
	if err := watchPlain(context.Background(), root, "demo", store, panelErrorWriter{failure}, nil); !errors.Is(err, failure) {
		t.Fatalf("got %v, want write error", err)
	}
}
