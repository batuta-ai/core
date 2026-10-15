package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/worktree"
)

type loopProcessScanner func(context.Context, string) ([]worktree.Process, error)

func (f loopProcessScanner) Inside(ctx context.Context, dir string) ([]worktree.Process, error) {
	return f(ctx, dir)
}

type loopProcessStopper func(context.Context, string, []worktree.Process, time.Duration) ([]worktree.StoppedProcess, error)

func (f loopProcessStopper) Stop(ctx context.Context, dir string, procs []worktree.Process, grace time.Duration) ([]worktree.StoppedProcess, error) {
	return f(ctx, dir, procs, grace)
}

func TestLoopSkippedProcessNotReportedStopped(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.Processes = loopProcessScanner(func(_ context.Context, dir string) ([]worktree.Process, error) {
		if filepath.Base(dir) != "greetings-task-1-e1" {
			return nil, nil
		}
		return []worktree.Process{
			{PID: 900001, Command: "sleep", Cwd: dir, Start: time.Unix(1, 0)},
			{PID: 900002, Command: "editor", Cwd: dir, Start: time.Unix(1, 0)},
			{PID: 900003, Command: "server", Cwd: dir, Start: time.Unix(1, 0)},
		}, nil
	})
	opts.Stopper = loopProcessStopper(func(_ context.Context, _ string, procs []worktree.Process, _ time.Duration) ([]worktree.StoppedProcess, error) {
		return []worktree.StoppedProcess{
			{Process: procs[0], Signal: "TERM"},
			{Process: procs[1], Reason: "identity changed"},
			{Process: procs[2], Signal: "KILL", Reason: "process still running"},
		}, nil
	})
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	trail, err := os.ReadFile(r.trailPath("task_1"))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"output": out.String(), "trail": string(trail)} {
		if !strings.Contains(content, "task_1 stopped pid 900001 (sleep)") || !strings.Contains(content, "SIGTERM") {
			t.Errorf("%s missing stopped entry:\n%s", name, content)
		}
		if !strings.Contains(content, "warning:") || !strings.Contains(content, "900002") || !strings.Contains(content, "identity changed") {
			t.Errorf("%s missing skipped warning:\n%s", name, content)
		}
		if strings.Contains(content, "stopped pid 900002") {
			t.Errorf("%s claims skipped PID stopped:\n%s", name, content)
		}
		if !strings.Contains(content, "sent SIGKILL to pid 900003 (server)") || !strings.Contains(content, "could not confirm it ended: process still running") {
			t.Errorf("%s missing unconfirmed warning:\n%s", name, content)
		}
		if strings.Contains(content, "stopped pid 900003") {
			t.Errorf("%s claims unconfirmed PID stopped:\n%s", name, content)
		}
	}
	for _, proc := range r.allStoppedProcesses() {
		if proc.PID != 900001 {
			t.Errorf("journaled stops include PID %d; want only 900001", proc.PID)
		}
	}
}

func TestTrailHeadingAfterEarlyStopLines(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatal(err)
	}
	path := r.trailPath("task_1")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	early := "task_1 stopped pid 900001 (sleep) in /leftover with SIGTERM\n"
	if err := os.WriteFile(path, []byte(early), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	trail, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(trail), "# Run — ") || !strings.Contains(string(trail), early) || strings.Count(string(trail), "# Run — ") != 1 {
		t.Fatalf("trail lacks one run heading above the early stop line:\n%s", trail)
	}
}

func TestLoopProcessScanErrorKeepsRemoval(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.Processes = loopProcessScanner(func(context.Context, string) ([]worktree.Process, error) {
		return nil, errors.New("injected process scan failure")
	})
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	if got := strings.Count(out.String(), "warning: process cleanup: injected process scan failure"); got != 1 {
		t.Fatalf("scan warning count = %d, want 1:\n%s", got, out.String())
	}
	if trees := f.worktrees(t); len(trees) != 0 {
		t.Fatalf("worktrees left after scan error: %v", trees)
	}
}

type journalStops struct {
	mu      sync.Mutex
	nextPID int
	stopped []worktree.StoppedProcess
}

func (s *journalStops) options(opts *Options) {
	opts.Processes = loopProcessScanner(func(_ context.Context, dir string) ([]worktree.Process, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.nextPID += 3
		return []worktree.Process{
			{PID: s.nextPID, Command: "server", Cwd: dir, Start: time.Unix(1, 0)},
			{PID: s.nextPID + 1, Command: "worker", Cwd: dir, Start: time.Unix(1, 0)},
			{PID: s.nextPID + 2, Command: "changed", Cwd: dir, Start: time.Unix(1, 0)},
		}, nil
	})
	opts.Stopper = loopProcessStopper(func(_ context.Context, _ string, procs []worktree.Process, _ time.Duration) ([]worktree.StoppedProcess, error) {
		result := []worktree.StoppedProcess{
			{Process: procs[0], Signal: "TERM"},
			{Process: procs[1], Signal: "KILL"},
			{Process: procs[2], Reason: "identity changed"},
		}
		s.mu.Lock()
		s.stopped = append(s.stopped, result...)
		s.mu.Unlock()
		return result, nil
	})
}

func (s *journalStops) actual() []worktree.StoppedProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]worktree.StoppedProcess(nil), s.stopped...)
}

func journaledStops(t *testing.T, detail json.RawMessage) []worktree.StoppedProcess {
	t.Helper()
	var decoded struct {
		StoppedProcesses []worktree.StoppedProcess `json:"stopped_processes"`
	}
	if err := json.Unmarshal(detail, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.StoppedProcesses
}

func assertJournalStops(t *testing.T, got, observed []worktree.StoppedProcess) {
	t.Helper()
	want := map[int]worktree.StoppedProcess{}
	for _, proc := range observed {
		if proc.Signal == "TERM" || proc.Signal == "KILL" {
			want[proc.PID] = proc
		}
	}
	if len(got) != len(want) {
		t.Fatalf("journaled stops = %v, want %v", got, want)
	}
	for _, proc := range got {
		if !reflect.DeepEqual(proc, want[proc.PID]) {
			t.Fatalf("journaled stop = %+v, want %+v", proc, want[proc.PID])
		}
	}
}

func TestLoopJournalsStoppedProcesses(t *testing.T) {
	for _, tc := range []struct {
		name, scenario, state string
	}{
		{"done", "default", StateDone},
		{"blocked", "always-broken", StateBlocked},
		{"abandoned", "default", StateAbandoned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			cache := tempDir(t)
			t.Setenv("HOME", cache)
			t.Setenv("XDG_CACHE_HOME", cache)
			var out bytes.Buffer
			stops := new(journalStops)
			opts := f.options(tc.scenario, &out)
			stops.options(&opts)
			if tc.state == StateAbandoned {
				opts.MaxWaves, opts.KeepWorktrees = 1, true
			}
			r, err := New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if tc.state != StateAbandoned {
				attemptName := r.worktreeName("task_1", 90)
				attemptBranch := r.branchName("task_1", 90)
				attemptRoot, err := r.addWorktree(context.Background(), attemptName, attemptBranch, f.base)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.removeWorktree(context.Background(), attemptRoot, attemptBranch); err != nil {
					t.Fatal(err)
				}
				finalName := r.worktreeName("task_1", 91)
				finalBranch := r.branchName("task_1", 91)
				finalRoot, err := r.addWorktree(context.Background(), finalName, finalBranch, f.base)
				if err != nil {
					t.Fatal(err)
				}
				r.worktrees[attemptKey("task_1", 91)] = attemptWorktree{Name: finalName, Root: finalRoot, Branch: finalBranch}
			}
			state, runErr := r.Run(context.Background())
			if tc.state == StateAbandoned {
				if !errors.Is(runErr, ErrStopped) {
					t.Fatalf("Run() = %s, %v", state, runErr)
				}
				opts.Resume, opts.MaxWaves, opts.KeepWorktrees = r.Delivery(), 0, false
				state, runErr = Abandon(context.Background(), opts)
			}
			if runErr != nil || state != tc.state {
				t.Fatalf("delivery = %s, %v\n%s", state, runErr, out.String())
			}
			records := readJournal(t, f, r.Delivery())
			if records[len(records)-1].Kind != KindTerminal {
				t.Fatalf("last record = %s", records[len(records)-1].Kind)
			}
			observed := stops.actual()
			if len(observed) == 0 {
				t.Fatal("fake stopper was not called")
			}
			assertJournalStops(t, journaledStops(t, records[len(records)-1].Detail), observed)
			for _, record := range records {
				if record.Kind != KindCleanup {
					continue
				}
				var detail struct {
					Worktree attemptWorktree `json:"worktree"`
				}
				if err := json.Unmarshal(record.Detail, &detail); err != nil {
					t.Fatal(err)
				}
				var forTree []worktree.StoppedProcess
				for _, proc := range observed {
					if proc.Cwd == detail.Worktree.Root {
						forTree = append(forTree, proc)
					}
				}
				assertJournalStops(t, journaledStops(t, record.Detail), forTree)
			}
			if tc.state == StateDone {
				if kinds(records)[KindCleanup] == 0 {
					t.Fatal("delivery did not settle a worktree")
				}
			}
			if kinds(records)[journal.Kind("processes_stopped")] != 0 {
				t.Fatal("unexpected process journal kind")
			}
		})
	}
}

func TestLoopStoppedProcessesSurviveResume(t *testing.T) {
	f := setup(t)
	t.Setenv("XDG_CACHE_HOME", tempDir(t))
	var out bytes.Buffer
	stops := new(journalStops)
	opts := f.options("default", &out)
	stops.options(&opts)
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	name, branch := r.worktreeName("task_1", 91), r.branchName("task_1", 91)
	root, err := r.addWorktree(context.Background(), name, branch, f.base)
	if err != nil {
		t.Fatal(err)
	}
	wt := attemptWorktree{Name: name, Root: root, Branch: branch}
	r.worktrees[attemptKey("task_1", 91)] = wt
	if err := r.open(); err != nil {
		t.Fatal(err)
	}
	detail := terminalDetail{State: StateDone, PlanPath: r.plan.Path, Worktrees: []attemptWorktree{wt}, CleanupPending: true}
	if err := r.checkpointFinalization(detail); err != nil {
		t.Fatal(err)
	}
	if err := r.cleanFinalization(context.Background(), &detail); err != nil {
		t.Fatal(err)
	}
	if err := r.checkpointFinalization(detail); err != nil {
		t.Fatal(err)
	}
	before := readJournal(t, f, r.Delivery())
	assertJournalStops(t, journaledStops(t, before[len(before)-1].Detail), stops.actual())
	resumeOpts := f.options("default", &out)
	resumeOpts.Resume = r.Delivery()
	stops.options(&resumeOpts)
	resumed, err := Resume(context.Background(), resumeOpts)
	if err != nil {
		t.Fatal(err)
	}
	state, err := resumed.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("resumed Run() = %s, %v\n%s", state, err, out.String())
	}
	after := readJournal(t, f, r.Delivery())
	if after[len(after)-1].Kind != KindTerminal {
		t.Fatalf("last record = %s", after[len(after)-1].Kind)
	}
	assertJournalStops(t, journaledStops(t, after[len(after)-1].Detail), stops.actual())
}

func TestJournalStoppedProcessesReaders(t *testing.T) {
	t.Parallel()
	f := setup(t)
	readers := make([]*Runner, 2)
	for i := range readers {
		var err error
		readers[i], err = New(context.Background(), f.options("default", new(bytes.Buffer)))
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(f.root, ".batuta", "worktrees", "greetings-task-1-e1")
	pruneJournal(t, f.root, "without-stops", "greetings", path, 1, 1, StateBlocked)
	baseline, err := pruneOwned(f.root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	plain := readJournal(t, f, "without-stops")
	withStops := append([]journal.Record(nil), plain...)
	var terminal map[string]any
	if err := json.Unmarshal(withStops[len(withStops)-1].Detail, &terminal); err != nil {
		t.Fatal(err)
	}
	terminal["stopped_processes"] = []worktree.StoppedProcess{{Process: worktree.Process{PID: 901, Command: "server", Cwd: path, Start: time.Unix(1, 0)}, Signal: "TERM"}}
	withStops[len(withStops)-1].Detail, err = json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range withStops {
		if _, err := store.Append("with-stops", record); err != nil {
			t.Fatal(err)
		}
	}
	owned, err := pruneOwned(f.root)
	if err != nil || !reflect.DeepEqual(owned, baseline) {
		t.Fatalf("pruneOwned() = %v, %v; baseline %v", owned, err, baseline)
	}
	if terminalState(plain) != terminalState(withStops) || pendingFinalization(plain) != nil || pendingFinalization(withStops) != nil {
		t.Fatal("terminal reader changed with stopped processes")
	}
	var states []string
	for i, records := range [][]journal.Record{plain, withStops} {
		if err := readers[i].replay(records); err != nil {
			t.Fatal(err)
		}
		states = append(states, readers[i].terminal)
	}
	if !reflect.DeepEqual(states, []string{StateBlocked, StateBlocked}) {
		t.Fatalf("replayed states = %v", states)
	}
}
