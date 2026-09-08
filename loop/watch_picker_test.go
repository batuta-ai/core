package loop

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func pickerDelivery(t *testing.T, store *journal.Store, id, slug string, opened, updated time.Time, state string, taskState routing.GraphTaskState) []journal.Record {
	t.Helper()
	graph := routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: taskState}}}
	records := []journal.Record{
		panelRecord(t, KindOpened, "", opened, openedDetail{Slug: slug}, graph),
		panelRecord(t, KindStarted, "task_1", updated, map[string]any{"execution": 1}, graph),
	}
	if state != "" {
		records = append(records, panelRecord(t, KindTerminal, "", updated, map[string]any{"state": state}, graph))
	}
	for _, record := range records {
		if _, err := store.Append(id, record); err != nil {
			t.Fatal(err)
		}
	}
	return records
}

func TestPickerListsDeliveries(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	pickerDelivery(t, store, "older-open", "first-plan", now.Add(-time.Hour), now.Add(-10*time.Minute), "", routing.GraphTaskRunning)
	pickerDelivery(t, store, "newer-done", "second-plan", now.Add(-30*time.Minute), now.Add(-time.Minute), StateDone, routing.GraphTaskIntegrated)
	pickerDelivery(t, store, "newer-open", "third-plan", now.Add(-5*time.Minute), now.Add(-2*time.Minute), "", routing.GraphTaskRunning)
	writePresenceFixture(t, root, "newer-open", now)

	items, err := deliveryItems(root, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items=%d, want 3", len(items))
	}
	first, ok := items[0].(deliveryItem)
	if !ok {
		t.Fatalf("item type=%T", items[0])
	}
	if first.id != "newer-open" {
		t.Fatalf("first=%q, want newest open delivery", first.id)
	}
	joined := first.Title() + " " + first.Description()
	for _, want := range []string{"newer-open", "third-plan", "open", "●", "2m"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("item %q missing %q", joined, want)
		}
	}

	picker := list.New(items, list.NewDefaultDelegate(), 80, 16)
	picker.SetShowTitle(false)
	picker.SetFilterText("second-plan")
	if got := picker.VisibleItems(); len(got) != 1 || got[0].(deliveryItem).id != "newer-done" {
		t.Fatalf("filtered items=%v", got)
	}
}

func TestPickerSwitchesDelivery(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	oldRecords := pickerDelivery(t, store, "old", "old-plan", now.Add(-time.Hour), now.Add(-40*time.Minute), StateDone, routing.GraphTaskIntegrated)
	pickerDelivery(t, store, "active", "active-plan", now.Add(-20*time.Minute), now.Add(-time.Minute), "", routing.GraphTaskRunning)
	writePresenceFixture(t, root, "active", now)
	m := newPollingWatchModel(root, "old", store, oldRecords, time.Second, Style{Width: 100, Lang: "en", Glyphs: "unicode"}, func() time.Time { return now }, nil)
	m.navigation.selected = "missing"
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if !m.picking {
		t.Fatal("d did not open picker")
	}
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.picking || m.delivery != "active" || m.panel.Detail.Task != "task_1" {
		t.Fatalf("picking=%v delivery=%q selected=%q", m.picking, m.delivery, m.panel.Detail.Task)
	}
	if m.poll.presence != "running" || m.poll.journal == (watchFileStamp{}) {
		t.Fatalf("poll baseline was not reset: %+v", m.poll)
	}
}

func TestPickerCancels(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records := pickerDelivery(t, store, "active", "active-plan", now.Add(-20*time.Minute), now.Add(-time.Minute), "", routing.GraphTaskRunning)
	m := newPollingWatchModel(root, "active", store, records, time.Second, Style{Width: 100}, func() time.Time { return now }, nil)
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.picking || m.delivery != "active" {
		t.Fatalf("cancel left picking=%v delivery=%q", m.picking, m.delivery)
	}
}

func TestWatchOpensPickerWhenNothingIsOpen(t *testing.T) {
	previousTerminal, previousProgram := isTerminal, newWatchProgram
	t.Cleanup(func() { isTerminal, newWatchProgram = previousTerminal, previousProgram })
	isTerminal = func(fd uintptr) bool { return fd == os.Stdin.Fd() }

	for _, tc := range []struct {
		name         string
		addOpen      bool
		wantPicking  bool
		wantDelivery string
	}{
		{name: "no open deliveries", wantPicking: true},
		{name: "one open delivery", addOpen: true, wantDelivery: "only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if tc.addOpen {
				now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
				pickerDelivery(t, store, "only", "only-plan", now.Add(-time.Minute), now, "", routing.GraphTaskRunning)
			}
			var output bytes.Buffer
			started := false
			newWatchProgram = func(model tea.Model, options ...tea.ProgramOption) *tea.Program {
				started = true
				m, ok := model.(watchModel)
				if !ok || m.picking != tc.wantPicking || m.delivery != tc.wantDelivery {
					t.Fatalf("program model: type=%T picking=%v delivery=%q", model, m.picking, m.delivery)
				}
				options = append(options, tea.WithInput(strings.NewReader("q")), tea.WithOutput(&output), tea.WithoutRenderer())
				return tea.NewProgram(model, options...)
			}
			if err := Watch(context.Background(), root, "", time.Hour, &output); err != nil {
				t.Fatal(err)
			}
			if !started || strings.Contains(output.String(), "no open deliveries") {
				t.Fatalf("started=%v output=%q", started, output.String())
			}
		})
	}
}

func TestSnapshotStillReportsNoOpenDeliveries(t *testing.T) {
	var output strings.Builder
	if err := Snapshot(t.TempDir(), "", &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "no open deliveries\n" {
		t.Fatalf("snapshot=%q", output.String())
	}
}

func TestPickerKeepsBackgroundChains(t *testing.T) {
	records, graph, now := modelFixture(t)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records = storePanelRecords(t, store, "dashboard", records)
	var durations []time.Duration
	m := newPollingWatchModel(root, "dashboard", store, records, 37*time.Millisecond, Style{Width: 120}, func() time.Time { return now }, immediateTicker(now, &durations))
	poll, clock, spinner := m.pollCmd(), m.clockCmd(), m.spinnerCmd()
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, poll = updateWatch(t, m, poll())
	if poll == nil || !m.picking {
		t.Fatal("picker stopped the idle poll chain")
	}
	graph.Tasks[0].State = routing.GraphTaskPending
	storePanelRecords(t, store, "dashboard", []journal.Record{panelRecord(t, KindProgress, "task_2", now, map[string]any{"execution": 1, "criterion": 2, "state": "DONE"}, graph)})
	msg := poll()
	m, batch := updateWatch(t, m, msg)
	if len(m.records) != len(records)+1 || !m.progress.active || !m.picking || batch == nil {
		t.Fatal("picker did not process journal and start progress animation")
	}
	commands, ok := batch().(tea.BatchMsg)
	if !ok || len(commands) != 2 {
		t.Fatalf("journal commands=%v, want poll and progress", commands)
	}
	var progress tea.Cmd
	for _, command := range commands {
		switch msg := command().(type) {
		case watchPollMsg:
			m, poll = updateWatch(t, m, msg)
		case progressTickMsg:
			m, progress = updateWatch(t, m, msg)
		default:
			t.Fatalf("unexpected journal command %T", msg)
		}
	}
	for _, open := range []bool{true, false} {
		if !open {
			m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.picking {
				t.Fatal("esc did not close picker")
			}
		}
		if poll == nil || clock == nil || spinner == nil || progress == nil {
			t.Fatal("background chain stopped")
		}
		m, poll = updateWatch(t, m, poll())
		later := m.currentTime.Add(time.Second)
		clockMsg := clock().(clockMsg)
		clockMsg.at = later
		m, clock = updateWatch(t, m, clockMsg)
		if m.currentTime != later {
			t.Fatal("clock did not advance")
		}
		frame := m.style.Frame
		m, spinner = updateWatch(t, m, spinner())
		if m.style.Frame != frame+1 {
			t.Fatal("spinner did not advance")
		}
		frame = m.progress.frame
		m, progress = updateWatch(t, m, progress())
		if m.progress.frame != frame+1 {
			t.Fatal("progress did not advance")
		}
		if poll == nil || clock == nil || spinner == nil || progress == nil {
			t.Fatal("background chain did not reschedule")
		}
	}
}

func TestPickerResizes(t *testing.T) {
	records, _, now := modelFixture(t)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records = storePanelRecords(t, store, "dashboard", records)
	var durations []time.Duration
	m := newPollingWatchModel(root, "dashboard", store, records, time.Second, Style{Width: 120}, func() time.Time { return now }, immediateTicker(now, &durations))
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 20}, {Width: 40, Height: 8}, {Width: 140, Height: 50}} {
		m, _ = updateWatch(t, m, size)
		if !m.picking || m.style.Width != size.Width || m.height != size.Height {
			t.Fatalf("window not handled: width=%d height=%d picking=%v", m.style.Width, m.height, m.picking)
		}
		if m.deliveryPicker.Width() != size.Width || m.deliveryPicker.Height() != min(16, max(5, size.Height/2)) {
			t.Fatalf("picker size=%dx%d after %dx%d", m.deliveryPicker.Width(), m.deliveryPicker.Height(), size.Width, size.Height)
		}
	}
}
