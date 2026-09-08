package loop

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/core/journal"
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
