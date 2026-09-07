package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

type keyTerminal struct {
	keys             chan string
	raw              bool
	starts, restores int
	reading          atomic.Int32
	restoredReading  bool
}

func (t *keyTerminal) enterRaw() (func() error, error) {
	t.raw = true
	t.starts++
	return func() error {
		t.restoredReading = t.reading.Load() != 0
		t.raw = false
		t.restores++
		return nil
	}, nil
}

func (t *keyTerminal) readKey(ctx context.Context) (string, error) {
	t.reading.Add(1)
	defer t.reading.Add(-1)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case key, ok := <-t.keys:
		if !ok {
			return "", io.EOF
		}
		return key, nil
	}
}

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
	root, store := keyStore(t, records)
	before, _ := store.Read("demo")
	for _, exit := range []string{"q", "cancel", "write error", "panic", "EOF"} {
		t.Run(exit, func(t *testing.T) {
			terminal := &keyTerminal{keys: make(chan string, 1)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			output := &panelCallbackWriter{fn: func() {
				if !terminal.raw {
					t.Error("watch did not enter raw mode")
				}
				switch exit {
				case "q":
					terminal.keys <- "q"
				case "cancel":
					cancel()
				case "panic":
					panic("writer panic")
				case "EOF":
					close(terminal.keys)
					cancel()
				}
			}}
			var writer io.Writer = output
			failure := errors.New("write failed")
			if exit == "write error" {
				writer = keyErrorWriter{failure}
			}
			var err error
			var caught any
			func() {
				defer func() { caught = recover() }()
				err = watchWithTerminal(ctx, root, "demo", time.Hour, writer, terminal, nil)
			}()
			if exit == "panic" && caught != "writer panic" || exit != "panic" && caught != nil {
				t.Fatalf("unexpected panic: %v", caught)
			}
			if exit == "write error" {
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if terminal.raw || terminal.starts != 1 || terminal.restores != 1 || terminal.restoredReading {
				t.Fatalf("terminal not restored: %+v", terminal)
			}
		})
	}
	after, _ := store.Read("demo")
	if len(before) != len(after) {
		t.Fatal("watch modified the delivery")
	}
}

func TestPanelKeysReaderExitRestoresTerminal(t *testing.T) {
	terminal := &keyTerminal{keys: make(chan string)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := startPanelKeys(ctx, terminal)
	if err != nil {
		t.Fatal(err)
	}
	defer session.stop()
	close(terminal.keys)
	if event := <-session.keys; !errors.Is(event.err, io.EOF) {
		t.Fatalf("reader exit: %+v", event)
	}
	<-session.done
	if terminal.raw || terminal.restores != 1 || terminal.restoredReading {
		t.Fatal("reader exited without restoring terminal mode")
	}
}

func TestPanelKeySequences(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"\x1b[A", "up"}, {"\x1bOA", "up"}, {"\x1b[B", "down"}, {"\x1bOB", "down"},
		{"\x1b[5~", "pageUp"}, {"\x1b[6~", "pageDown"}, {"\x03", "interrupt"},
		{"q", "q"}, {"f", "f"}, {"?", "?"}, {"o", "o"}, {"r", "r"},
		{"\x1b[Zq", "q"},
	} {
		var sequence, got string
		for i := range test.input {
			key := decodePanelKey(&sequence, test.input[i])
			if key != "" {
				if got != "" {
					t.Fatalf("%q decoded multiple keys", test.input)
				}
				got = key
			}
		}
		if got != test.want || sequence != "" {
			t.Fatalf("%q decoded as %q, pending %q", test.input, got, sequence)
		}
	}
}

type keyErrorWriter struct{ err error }

func (w keyErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestPanelKeysLegendLogAndAnswer(t *testing.T) {
	records, graph, now := modelFixture(t)
	graph.Tasks[1].State = routing.GraphTaskWaitingInput
	graph.Tasks[1].Attempts[0].Question = &routing.TaskQuestion{Prompt: "Which format?"}
	records = append(records, panelRecord(t, KindQuestion, "task_2", now, map[string]any{"execution": 1, "question": "Which format?"}, graph),
		panelRecord(t, KindTerminal, "", now, map[string]any{"state": StateWaitingInput}, graph))
	root, _ := keyStore(t, records)
	terminal := &keyTerminal{keys: make(chan string, 1)}
	steps := []string{"?", "?", "o", "r", "q"}
	var frames []string
	output := &keyFrameWriter{write: func(frame string) {
		frames = append(frames, frame)
		if len(steps) > 0 {
			terminal.keys <- steps[0]
			steps = steps[1:]
		}
	}}
	opened := ""
	pager := func(ctx context.Context, path string) error {
		if terminal.raw {
			t.Error("pager inherited raw mode")
		}
		opened = path
		return nil
	}
	if err := watchWithTerminal(context.Background(), root, "demo", time.Hour, output, terminal, pager); err != nil {
		t.Fatal(err)
	}
	if opened != filepath.Join(root, ".batuta/runs/2026-09-06-demo-task-2-e1.out.log") {
		t.Fatalf("opened %q", opened)
	}
	joined := strings.Join(frames, "\n")
	command := "batuta loop --workspace " + panelShellQuote(root) + " --answer 'task_2' \"<text>\""
	if !strings.Contains(joined, command) || !strings.Contains(joined, opened) {
		t.Fatalf("missing actions:\n%s", joined)
	}
	if len(frames) < 3 || !strings.Contains(frames[1], "Legend") || strings.Contains(frames[2], "Legend") {
		t.Fatal("legend did not toggle")
	}
	for _, gate := range []string{"G0", "G1", "G2", "G3"} {
		if !strings.Contains(frames[1], gate) {
			t.Fatal("legend missing " + gate)
		}
	}
	if terminal.raw || terminal.starts != 2 || terminal.restores != 2 {
		t.Fatalf("raw lifecycle: %+v", terminal)
	}
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

type keyFrameWriter struct{ write func(string) }

func (w *keyFrameWriter) Write(p []byte) (int, error) { w.write(string(p)); return len(p), nil }

func TestWatchWithoutATTYAutoFollows(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	if newPanelTerminal(input) != nil {
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
		} else {
			cancel()
		}
	}}
	if err := Watch(ctx, root, "demo", time.Nanosecond, output); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || !strings.Contains(frames[0], "View model") || !strings.Contains(frames[1], "Renderer") {
		t.Fatalf("did not follow: %v", frames)
	}
}
