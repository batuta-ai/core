package loop

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

func immediateTicker(at time.Time, durations *[]time.Duration) watchTicker {
	return func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		*durations = append(*durations, d)
		return func() tea.Msg { return fn(at) }
	}
}

func storePanelRecords(t *testing.T, store *journal.Store, delivery string, records []journal.Record) []journal.Record {
	t.Helper()
	stored := make([]journal.Record, 0, len(records))
	for _, record := range records {
		got, err := store.Append(delivery, record)
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, got)
	}
	return stored
}

func TestWatchPollEmitsOnlyOnChange(t *testing.T) {
	records, graph, now := modelFixture(t)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records = storePanelRecords(t, store, "dashboard", records)
	logPath := panelLogPath(root, PanelModel(records, now, ""))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("first line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var durations []time.Duration
	m := newPollingWatchModel(root, "dashboard", store, records, 37*time.Millisecond, Style{Width: 120}, func() time.Time { return now }, immediateTicker(now, &durations))
	msg := m.pollCmd()()
	if _, changed := msg.(journalMsg); changed {
		t.Fatal("unchanged files emitted journalMsg")
	}
	m, cmd := updateWatch(t, m, msg)
	if cmd == nil {
		t.Fatal("unchanged poll did not reschedule")
	}

	fresh := panelRecord(t, KindProgress, "task_2", now, map[string]any{"execution": 1, "criterion": 2, "state": "DONE"}, graph)
	if _, err := store.Append("dashboard", fresh); err != nil {
		t.Fatal(err)
	}
	msg = cmd()
	changed, ok := msg.(journalMsg)
	if !ok || len(changed.records) != len(records)+1 {
		t.Fatalf("journal change message = %#v", msg)
	}
	m, cmd = updateWatch(t, m, msg)
	if cmd == nil {
		t.Fatal("journal change did not reschedule")
	}

	if err := os.WriteFile(logPath, []byte("first line\nsecond line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg = cmd()
	changed, ok = msg.(journalMsg)
	if !ok || len(changed.logLines) != 2 || changed.logLines[1] != "second line" {
		t.Fatalf("log change message = %#v", msg)
	}
	m, cmd = updateWatch(t, m, msg)
	if cmd == nil || len(m.panel.LogLines) != 2 || m.panel.LogLines[1] != "second line" {
		t.Fatalf("loaded log = %#v; rescheduled = %v", m.panel.LogLines, cmd != nil)
	}
	if got := durations; len(got) != 4 || got[0] != 37*time.Millisecond || got[1] != 37*time.Millisecond || got[2] != 37*time.Millisecond || got[3] != 37*time.Millisecond {
		t.Fatalf("poll durations = %v", got)
	}
}

func TestWatchClockAdvancesElapsed(t *testing.T) {
	records, _, now := modelFixture(t)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records = storePanelRecords(t, store, "dashboard", records)
	later := now.Add(7 * time.Second)
	var durations []time.Duration
	m := newPollingWatchModel(root, "dashboard", store, records, 250*time.Millisecond, Style{Width: 120}, func() time.Time { return now }, immediateTicker(later, &durations))
	wantElapsed := m.panel.Header.Elapsed + 7*time.Second
	wantLastAge := m.panel.Detail.LastAge + 7*time.Second
	if err := os.Remove(store.Path("dashboard")); err != nil {
		t.Fatal(err)
	}

	msg := m.clockCmd()()
	if _, ok := msg.(clockMsg); !ok {
		t.Fatalf("clock command emitted %T", msg)
	}
	m, cmd := updateWatch(t, m, msg)
	if cmd == nil {
		t.Fatal("clock did not reschedule")
	}
	if m.panel.Header.Elapsed != wantElapsed || m.panel.Detail.LastAge != wantLastAge {
		t.Fatalf("elapsed = %v, last age = %v; want %v, %v", m.panel.Header.Elapsed, m.panel.Detail.LastAge, wantElapsed, wantLastAge)
	}
	if len(durations) != 2 || durations[0] != time.Second || durations[1] != time.Second {
		t.Fatalf("clock durations = %v", durations)
	}
}

func TestWatchPollPresenceChanges(t *testing.T) {
	records, _, now := modelFixture(t)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records = storePanelRecords(t, store, "dashboard", records)
	var durations []time.Duration
	m := newPollingWatchModel(root, "dashboard", store, records, time.Second, Style{Width: 120}, func() time.Time { return now }, immediateTicker(now, &durations))
	if m.panel.Header.Presence != "none" {
		t.Fatalf("initial presence: %+v", m.panel.Header)
	}
	check := func(state string, loops int) {
		t.Helper()
		msg := m.pollCmd()()
		if _, ok := msg.(journalMsg); !ok {
			t.Fatalf("presence change emitted %T", msg)
		}
		m, _ = updateWatch(t, m, msg)
		if m.panel.Header.Presence != state || m.panel.Header.Loops != loops || m.viewport.Header.Presence != state {
			t.Fatalf("header = %+v, want %s, %d", m.panel.Header, state, loops)
		}
		m, _ = updateWatch(t, m, clockMsg{at: now})
		if m.panel.Header.Presence != state || m.panel.Header.Loops != loops {
			t.Fatal("clock lost presence")
		}
	}
	path := writePresenceFixture(t, root, "dashboard", now)
	check("running", 1)
	other := writePresenceFixture(t, root, "other", now)
	check("running", 2)
	if err := os.Chtimes(path, now.Add(time.Second), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	check("running", 2)
	m.ticker = immediateTicker(now.Add(17*time.Second), &durations)
	check("stale", 0)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check("none", 0)
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	check("none", 0)
	if _, ok := m.pollCmd()().(watchPollMsg); !ok {
		t.Fatal("unchanged locks redrew")
	}
}

func TestPollResultsTaggedAndStaleDropped(t *testing.T) {
	for _, kind := range []string{"changed", "unchanged", "error"} {
		for _, change := range []string{"delivery", "generation", "log path"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				records, graph, now := modelFixture(t)
				root := t.TempDir()
				store, err := journal.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				records = storePanelRecords(t, store, "old", records)
				storePanelRecords(t, store, "new", records)
				var durations []time.Duration
				m := newPollingWatchModel(root, "old", store, records, 37*time.Millisecond, Style{Width: 120}, func() time.Time { return now }, immediateTicker(now, &durations))
				identity := m.pollIdentity()
				pending := m.pollCmd()
				switch kind {
				case "changed":
					storePanelRecords(t, store, "old", []journal.Record{panelRecord(t, KindProgress, "task_2", now, map[string]any{"execution": 1, "criterion": 2, "state": "DONE"}, graph)})
				case "error":
					if err := os.Remove(store.Path("old")); err != nil {
						t.Fatal(err)
					}
				}
				msg := pending()
				switch msg := msg.(type) {
				case journalMsg:
					if msg.identity != identity {
						t.Fatalf("journal identity=%+v, want %+v", msg.identity, identity)
					}
				case watchPollMsg:
					if msg.identity != identity {
						t.Fatalf("idle identity=%+v, want %+v", msg.identity, identity)
					}
				default:
					t.Fatalf("unexpected poll result %T", msg)
				}
				if kind == "error" {
					storePanelRecords(t, store, "old", records)
				}
				switch change {
				case "delivery", "generation":
					if err := m.switchDelivery("new"); err != nil {
						t.Fatal(err)
					}
					if change == "generation" {
						if err := m.switchDelivery("old"); err != nil {
							t.Fatal(err)
						}
					}
				case "log path":
					m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
				}
				beforeRecords, beforePoll, beforeView := m.records, m.poll, m.View().Content
				m, cmd := updateWatch(t, m, msg)
				if !reflect.DeepEqual(m.records, beforeRecords) || !reflect.DeepEqual(m.poll, beforePoll) || m.View().Content != beforeView {
					t.Fatal("stale poll changed the current model")
				}
				if (cmd != nil) != (change == "log path") {
					t.Fatalf("stale result rescheduled=%v, change=%s", cmd != nil, change)
				}
			})
		}
	}
}

func TestSinglePollChainAfterSwitch(t *testing.T) {
	records, _, now := modelFixture(t)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records = storePanelRecords(t, store, "old", records)
	storePanelRecords(t, store, "new", records)
	var durations []time.Duration
	m := newPollingWatchModel(root, "old", store, records, 37*time.Millisecond, Style{Width: 120}, func() time.Time { return now }, immediateTicker(now, &durations))
	generation := m.generation
	oldPoll := m.pollCmd()
	m, _ = updateWatch(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	for i, item := range m.deliveryPicker.Items() {
		if item.(deliveryItem).id == "new" {
			m.deliveryPicker.Select(i)
		}
	}
	m, next := updateWatch(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.generation != generation+1 {
		t.Fatal("delivery switch did not increment generation")
	}
	if m.delivery != "new" || next == nil {
		t.Fatal("switch did not start a poll")
	}
	m, staleNext := updateWatch(t, m, oldPoll())
	if staleNext != nil {
		t.Fatal("old delivery started a second poll chain")
	}
	for range 3 {
		m, next = updateWatch(t, m, next())
		if next == nil {
			t.Fatal("current poll chain stopped")
		}
	}
	if len(durations) != 5 {
		t.Fatalf("scheduled %d polls, want 5", len(durations))
	}
}

func TestWatchPollFollowsNewLog(t *testing.T) {
	records, graph, now := modelFixture(t)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records = storePanelRecords(t, store, "dashboard", records)
	oldPath := panelLogPath(root, PanelModel(records, now, ""))
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(root, "new-task.log")
	if err := os.WriteFile(newPath, []byte("new task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var durations []time.Duration
	m := newPollingWatchModel(root, "dashboard", store, records, time.Second, Style{Width: 120}, func() time.Time { return now }, immediateTicker(now, &durations))
	pending := m.pollCmd()
	graph.Tasks[1].State = routing.GraphTaskIntegrated
	graph.Tasks[2].State = routing.GraphTaskRunning
	graph.Tasks[2].Attempts = []routing.GraphTaskAttempt{{Execution: 1}}
	storePanelRecords(t, store, "dashboard", []journal.Record{panelRecord(t, KindStarted, "task_3", now, map[string]any{"execution": 1, "log_path": "new-task.log"}, graph)})
	msg := pending().(journalMsg)
	if msg.identity.logPath != oldPath || !reflect.DeepEqual(msg.logLines, []string{"old task"}) {
		t.Fatalf("poll log path=%q lines=%v", msg.identity.logPath, msg.logLines)
	}
	m, cmd := updateWatch(t, m, msg)
	if cmd == nil || m.panel.Detail.Task != "task_3" || !reflect.DeepEqual(m.panel.LogLines, []string{"new task"}) {
		t.Fatalf("journal did not follow new log: task=%s lines=%v", m.panel.Detail.Task, m.panel.LogLines)
	}
}

func TestFailedDeliverySwitchKeepsPollChain(t *testing.T) {
	records, graph, now := modelFixture(t)
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records = storePanelRecords(t, store, "old", records)
	broken := append(records, panelRecord(t, KindStarted, "task_2", now, map[string]any{"execution": 1, "log_path": "not-dir/log"}, graph))
	storePanelRecords(t, store, "broken", broken)
	if err := os.WriteFile(filepath.Join(root, "not-dir"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var durations []time.Duration
	m := newPollingWatchModel(root, "old", store, records, time.Second, Style{Width: 120}, func() time.Time { return now }, immediateTicker(now, &durations))
	pending := m.pollCmd()
	identity := m.pollIdentity()
	if err := m.switchDelivery("broken"); err == nil {
		t.Fatal("switch with unreadable log succeeded")
	}
	if m.pollIdentity() != identity {
		t.Fatal("failed switch invalidated the current chain")
	}
	_, next := updateWatch(t, m, pending())
	if next == nil {
		t.Fatal("failed switch stopped the current chain")
	}
}
