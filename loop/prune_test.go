package loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/worktree"
)

func pruneWorktree(t *testing.T, f fixture, home, slug string, task, execution int) worktree.ManagedWorktree {
	t.Helper()
	provider, err := worktree.New(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	name := slug + "-task-" + itoa(task) + "-e" + itoa(execution)
	branch := "batuta/" + slug + "/task-" + itoa(task) + "-e" + itoa(execution)
	path := filepath.Join(home, name)
	if _, err := provider.AddAt(context.Background(), path, branch, f.base, []string{home}); err != nil {
		t.Fatal(err)
	}
	return worktree.ManagedWorktree{Path: path, Branch: branch, Slug: slug, Task: task, Execution: execution}
}

func itoa(n int) string { return strconv.Itoa(n) }

func pruneJournal(t *testing.T, root, delivery, slug, path string, task, execution int, state string) {
	t.Helper()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord := func(kind journal.Kind, detail any, graph any, taskID string) {
		t.Helper()
		encodedDetail, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		encodedGraph, err := json.Marshal(graph)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(delivery, journal.Record{Kind: kind, TaskID: taskID, Detail: encodedDetail, Graph: encodedGraph}); err != nil {
			t.Fatal(err)
		}
	}
	graph := map[string]any{}
	appendRecord(KindOpened, openedDetail{Slug: slug, Workspace: root}, graph, "")
	if path != "" {
		taskID := "task_" + itoa(task)
		graph = map[string]any{"tasks": []any{map[string]any{"task_id": taskID, "attempts": []any{map[string]any{"execution": execution, "worktree_root": path}}}}}
		appendRecord(KindWorktree, map[string]any{"execution": execution, "worktree": attemptWorktree{Root: path}}, graph, taskID)
	}
	if state != "" {
		appendRecord(KindTerminal, terminalDetail{State: state}, graph, "")
	}
}

func TestPruneReportsOrphans(t *testing.T) {
	f := setup(t)
	cache := tempDir(t)
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LocalAppData", cache)
	cacheHome, err := worktree.CacheHome(f.root)
	if err != nil {
		t.Fatal(err)
	}
	repoHome := worktree.RepositoryHome(f.root)
	repo := pruneWorktree(t, f, repoHome, "old", 1, 1)
	remote := pruneWorktree(t, f, cacheHome, "stray", 2, 3)
	missing := pruneWorktree(t, f, repoHome, "gone", 1, 1)
	pruneJournal(t, f.root, "finished", "old", repo.Path, 1, 1, StateDone)
	if err := os.RemoveAll(missing.Path); err != nil {
		t.Fatal(err)
	}
	before := f.run(t, "worktree", "list", "--porcelain")
	got, err := Prune(context.Background(), f.root, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []PruneEntry{
		{Path: missing.Path, Branch: missing.Branch, Slug: missing.Slug, State: PruneMissing},
		{Path: repo.Path, Branch: repo.Branch, Slug: repo.Slug, State: PruneOrphan},
		{Path: remote.Path, Branch: remote.Branch, Slug: remote.Slug, State: PruneOrphan},
	}
	slices.SortFunc(want, func(a, b PruneEntry) int { return strings.Compare(a.Path, b.Path) })
	if !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("entries = %+v, want %+v", got.Entries, want)
	}
	if after := f.run(t, "worktree", "list", "--porcelain"); after != before {
		t.Fatalf("git registry changed:\n%s\n%s", before, after)
	}
	if _, err := os.Stat(repo.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remote.Path); err != nil {
		t.Fatal(err)
	}
}

func TestPruneKeepsOpenDeliveries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
	}{
		{name: "no terminal"},
		{name: StateWaitingInput, state: StateWaitingInput},
		{name: StateWaitingPlan, state: StateWaitingPlan},
		{name: StateBlocked, state: StateBlocked},
		{name: StateCanceled, state: StateCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			home := worktree.RepositoryHome(f.root)
			owned := pruneWorktree(t, f, home, "plan", 1, 1)
			orphan := pruneWorktree(t, f, home, "stray", 2, 1)
			pruneJournal(t, f.root, "open", "plan", owned.Path, 1, 1, tc.state)

			report, err := Prune(context.Background(), f.root, PruneOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := []PruneEntry{{Path: orphan.Path, Branch: orphan.Branch, Slug: orphan.Slug, State: PruneOrphan}}
			if !reflect.DeepEqual(report.Entries, want) {
				t.Fatalf("entries = %+v, want %+v", report.Entries, want)
			}
		})
	}
}

func TestPruneMatchesPathAndBranch(t *testing.T) {
	t.Run("finished and open deliveries share a slug", func(t *testing.T) {
		f := setup(t)
		home := worktree.RepositoryHome(f.root)
		old := pruneWorktree(t, f, home, "plan", 1, 1)
		current := pruneWorktree(t, f, home, "plan", 1, 2)
		pruneJournal(t, f.root, "old", "plan", old.Path, 1, 1, StateDone)
		pruneJournal(t, f.root, "current", "plan", current.Path, 1, 2, "")

		report, err := Prune(context.Background(), f.root, PruneOptions{})
		if err != nil {
			t.Fatal(err)
		}
		want := []PruneEntry{{Path: old.Path, Branch: old.Branch, Slug: old.Slug, State: PruneOrphan}}
		if !reflect.DeepEqual(report.Entries, want) {
			t.Fatalf("entries = %+v, want %+v", report.Entries, want)
		}
	})
	for _, tc := range []struct {
		name      string
		path      func(worktree.ManagedWorktree) string
		execution int
	}{
		{name: "path differs", path: func(wt worktree.ManagedWorktree) string { return wt.Path + "-other" }, execution: 1},
		{name: "branch differs", path: func(wt worktree.ManagedWorktree) string { return wt.Path }, execution: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			wt := pruneWorktree(t, f, worktree.RepositoryHome(f.root), "plan", 1, 1)
			pruneJournal(t, f.root, "open", "plan", tc.path(wt), 1, tc.execution, "")

			report, err := Prune(context.Background(), f.root, PruneOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := []PruneEntry{{Path: wt.Path, Branch: wt.Branch, Slug: wt.Slug, State: PruneOrphan}}
			if !reflect.DeepEqual(report.Entries, want) {
				t.Fatalf("entries = %+v, want %+v", report.Entries, want)
			}
		})
	}
}

func TestPruneFailsClosedOnJournalError(t *testing.T) {
	for _, scenario := range []string{"list failure", "read failure", "invalid opening"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			pruneWorktree(t, f, worktree.RepositoryHome(f.root), "stray", 1, 1)
			switch scenario {
			case "list failure":
				previous := pruneReadDir
				pruneReadDir = func(string) ([]os.DirEntry, error) { return nil, errors.New("list denied") }
				t.Cleanup(func() { pruneReadDir = previous })
			case "read failure":
				pruneJournal(t, f.root, "broken", "plan", "", 0, 0, "")
				store, err := journal.Open(f.root)
				if err != nil {
					t.Fatal(err)
				}
				file, err := os.OpenFile(store.Path("broken"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("not json\n"); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			case "invalid opening":
				store, err := journal.Open(f.root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Append("broken", journal.Record{Kind: KindTerminal, Detail: []byte(`{"state":"done"}`)}); err != nil {
					t.Fatal(err)
				}
			}
			report, err := Prune(context.Background(), f.root, PruneOptions{})
			if err == nil {
				t.Fatal("Prune succeeded with an unreadable journal store")
			}
			if !reflect.DeepEqual(report, PruneReport{}) {
				t.Fatalf("report = %+v, want empty on error", report)
			}
		})
	}
}

func TestPruneReportWarnsLiveRunner(t *testing.T) {
	f := setup(t)
	wt := pruneWorktree(t, f, worktree.RepositoryHome(f.root), "stray", 1, 1)
	if _, err := journal.Open(f.root); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 13, 12, 0, 0, 0, time.UTC)
	lockPath := filepath.Join(f.root, journal.Dir, "active-delivery.lock")
	if _, err := (presenceLock{PID: 123, RefreshedAt: now}).writeAtomic(lockPath); err != nil {
		t.Fatal(err)
	}

	report, err := Prune(context.Background(), f.root, PruneOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	want := []PruneEntry{{Path: wt.Path, Branch: wt.Branch, Slug: wt.Slug, State: PruneOrphan}}
	if !reflect.DeepEqual(report.Entries, want) {
		t.Fatalf("entries = %+v, want %+v", report.Entries, want)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "active-delivery") {
		t.Fatalf("warnings = %q, want live runner delivery", report.Warnings)
	}
}
