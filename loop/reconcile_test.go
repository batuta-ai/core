package loop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/core/journal"
)

func lastParkedSnapshot(t *testing.T, f fixture, delivery, taskID string) (string, string) {
	t.Helper()
	snapshots := taskSnapshotRecords(t, f, delivery, taskID)
	if len(snapshots) == 0 {
		t.Fatalf("delivery %s has no snapshot of %s", delivery, taskID)
	}
	return snapshotDetail(t, snapshots[len(snapshots)-1])
}

func commitFileOnBranch(t *testing.T, f fixture, path, content string) {
	t.Helper()
	full := filepath.Join(f.root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", path)
	f.run(t, "commit", "-q", "-m", "test: later change to "+path)
}

func blockedDelivery(t *testing.T) (fixture, string) {
	t.Helper()
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("always-broken", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateBlocked {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	return f, r.Delivery()
}

// workspaceState captures what Reconcile must leave alone.
func workspaceState(t *testing.T, f fixture, delivery string) map[string]string {
	t.Helper()
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return "<absent>"
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	entries, err := os.ReadDir(filepath.Join(f.root, journal.Dir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return map[string]string{
		"journal":   read(filepath.Join(f.root, journal.Dir, delivery+".jsonl")),
		"lock":      read(filepath.Join(f.root, journal.Dir, delivery+".lock")),
		"journals":  strings.Join(names, "\n"),
		"exclude":   read(filepath.Join(f.root, ".git", "info", "exclude")),
		"status":    f.run(t, "status", "--porcelain"),
		"worktrees": f.run(t, "worktree", "list"),
		"refs":      f.run(t, "for-each-ref", "--format=%(refname) %(objectname)"),
	}
}

func TestConductorReconcileReport(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(t *testing.T) (fixture, string)
		status    string
		changed   []string
		conflicts []string
	}{
		{
			name: "waiting task merges clean",
			prepare: func(t *testing.T) (fixture, string) {
				f, _, delivery, _, _ := parkedRecovery(t, "continuation-ask-untracked")
				return f, delivery
			},
			status:    ReconcileClean,
			changed:   []string{"out/1.txt"},
			conflicts: []string{},
		},
		{
			name: "later branch commit changes the same file",
			prepare: func(t *testing.T) (fixture, string) {
				f, _, delivery, _, _ := parkedRecovery(t, "continuation-ask-untracked")
				commitFileOnBranch(t, f, "out/1.txt", "a different greeting\n")
				return f, delivery
			},
			status:    ReconcileConflict,
			changed:   []string{"out/1.txt"},
			conflicts: []string{"out/1.txt"},
		},
		{
			name: "ref tree already in the branch history",
			prepare: func(t *testing.T) (fixture, string) {
				f, _, delivery, _, _ := parkedRecovery(t, "continuation-ask-untracked")
				_, ref := lastParkedSnapshot(t, f, delivery, "task_1")
				tree := f.run(t, "rev-parse", ref+"^{tree}")
				commit := f.run(t, "commit-tree", tree, "-p", "HEAD", "-m", "test: ref tree")
				f.run(t, "reset", "--hard", commit)
				return f, delivery
			},
			status:    ReconcileIntegrated,
			changed:   []string{"out/1.txt"},
			conflicts: []string{},
		},
		{
			name: "blocked delivery whose worktree is gone",
			prepare: func(t *testing.T) (fixture, string) {
				f, delivery := blockedDelivery(t)
				if wts := f.worktrees(t); len(wts) != 0 {
					t.Fatalf("worktrees left behind: %v", wts)
				}
				return f, delivery
			},
			status:    ReconcileClean,
			changed:   []string{"out/1.txt"},
			conflicts: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, delivery := tt.prepare(t)
			sha, ref := lastParkedSnapshot(t, f, delivery, "task_1")
			got, err := Reconcile(context.Background(), f.root, delivery, "task_1")
			if err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}
			want := ReconcileReport{Ref: ref, SHA: sha, Branch: "main", Status: tt.status, Changed: tt.changed, Conflicts: tt.conflicts}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Reconcile() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestConductorReconcileBindsRefToDelivery(t *testing.T) {
	f, opts, first, _, _ := parkedRecovery(t, "continuation-ask-untracked")
	firstSHA, ref := lastParkedSnapshot(t, f, first, "task_1")

	abandon := opts
	abandon.Resume = first
	if state, err := Abandon(context.Background(), abandon); err != nil || state != StateAbandoned {
		t.Fatalf("Abandon() = %s, %v", state, err)
	}
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("continuation-ask-untracked", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("second Run() = %s, %v\n%s", state, err, out.String())
	}
	second := r.Delivery()
	secondSHA, secondRef := lastParkedSnapshot(t, f, second, "task_1")
	if secondRef != ref || secondSHA == firstSHA {
		t.Fatalf("second delivery parked %s at %s; want %s overwritten with a new SHA (first %s)", secondRef, secondSHA, ref, firstSHA)
	}
	if got := f.run(t, "rev-parse", ref); got != secondSHA {
		t.Fatalf("%s = %s, want %s", ref, got, secondSHA)
	}

	report, err := Reconcile(context.Background(), f.root, second, "task_1")
	if err != nil {
		t.Fatalf("Reconcile(second) = %v", err)
	}
	if report.Ref != ref || report.SHA != secondSHA {
		t.Fatalf("Reconcile(second) = %+v, want %s at %s", report, ref, secondSHA)
	}
	_, err = Reconcile(context.Background(), f.root, first, "task_1")
	if err == nil || !strings.Contains(err.Error(), firstSHA) {
		t.Fatalf("Reconcile(first) = %v, want an error naming %s", err, firstSHA)
	}

	f.run(t, "update-ref", "-d", ref)
	_, err = Reconcile(context.Background(), f.root, second, "task_1")
	if err == nil || !strings.Contains(err.Error(), secondSHA) {
		t.Fatalf("Reconcile(deleted ref) = %v, want an error naming %s", err, secondSHA)
	}
}

func TestConductorReconcileIsReadOnly(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T) (fixture, string)
	}{
		{"waiting", func(t *testing.T) (fixture, string) {
			f, _, delivery, _, _ := parkedRecovery(t, "continuation-ask-untracked")
			return f, delivery
		}},
		{"conflict", func(t *testing.T) (fixture, string) {
			f, _, delivery, _, _ := parkedRecovery(t, "continuation-ask-untracked")
			commitFileOnBranch(t, f, "out/1.txt", "a different greeting\n")
			return f, delivery
		}},
		{"blocked", blockedDelivery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, delivery := tt.prepare(t)
			before := workspaceState(t, f, delivery)
			if _, err := Reconcile(context.Background(), f.root, delivery, "task_1"); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}
			if after := workspaceState(t, f, delivery); !reflect.DeepEqual(before, after) {
				for key := range before {
					if before[key] != after[key] {
						t.Errorf("%s changed:\nbefore: %q\nafter:  %q", key, before[key], after[key])
					}
				}
			}
		})
	}
}

func TestConductorReconcileRefusals(t *testing.T) {
	f, _, delivery, _, _ := parkedRecovery(t, "continuation-ask-untracked")
	tests := []struct {
		name      string
		workspace string
		delivery  string
		task      string
		want      string
	}{
		{"task without a snapshot record", f.root, delivery, "task_3", "task_3 has no worktree_snapshotted record"},
		{"unknown delivery", f.root, "greetings-19990101-000000", "task_1", "unknown delivery greetings-19990101-000000"},
		{"unknown task", f.root, delivery, "task_9", "unknown task task_9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Reconcile(context.Background(), tt.workspace, tt.delivery, tt.task)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Reconcile() = %v, want an error containing %q", err, tt.want)
			}
		})
	}
	t.Run("missing journal directory is not created", func(t *testing.T) {
		workspace := tempDir(t)
		_, err := Reconcile(context.Background(), workspace, delivery, "task_1")
		if err == nil || !strings.Contains(err.Error(), "unknown delivery "+delivery) {
			t.Fatalf("Reconcile() = %v, want unknown delivery", err)
		}
		if _, statErr := os.Stat(filepath.Join(workspace, ".batuta")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf(".batuta exists after a refused Reconcile: %v", statErr)
		}
	})
}
