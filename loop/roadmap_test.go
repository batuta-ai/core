package loop

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/core/journal"
)

func TestRoadmapJournalOpeningCompatibility(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"legacy-tail", "supervised-tail", "unknown-opening", "corrupt-opening", "missing-opening"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			root := tempDir(t)
			if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".batuta/roadmap.md"), []byte("# Roadmap — Waiting\n\n- [ ] 1. Missing → plans/missing.md\n"), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			detail := `{"supervision":false}`
			kind := KindOpened
			if scenario == "supervised-tail" {
				detail = `{"supervision":true}`
			}
			if scenario == "missing-opening" {
				kind = KindTerminal
			}
			if _, err := store.Append("old", journal.Record{Kind: kind, Detail: []byte(detail)}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(store.Path("old"))
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "unknown-opening":
				data = []byte("broken\n")
			case "corrupt-opening":
				data = bytes.Replace(data, []byte(`"supervision":false`), []byte(`"supervision":true`), 1)
			default:
				data = append(data, []byte(`{"incomplete":`)...)
			}
			if err := os.WriteFile(store.Path("old"), data, 0600); err != nil {
				t.Fatal(err)
			}
			state, err := RunRoadmap(context.Background(), Options{Workspace: root})
			if scenario == "legacy-tail" {
				if err != nil || state != StateWaitingPlan {
					t.Fatalf("legacy tail: %s, %v", state, err)
				}
			} else if err == nil || state != StateReviewBlocked {
				t.Fatalf("untrusted journal: %s, %v", state, err)
			}
		})
	}
}

func TestSupervisionGateRoadmapCompletedTickPreservesOperatorWork(t *testing.T) {
	t.Parallel()
	for _, changed := range []string{"branch", "staged", "roadmap"} {
		t.Run(changed, func(t *testing.T) {
			t.Parallel()
			observer, _, spec := supervisionGateRoadmapFixture(t)
			gateReview(t, observer, spec, "SHIP", true)
			f := fixture{root: observer.Workspace, git: "git"}
			opts := Options{Workspace: f.root}
			if state, err := RunRoadmap(context.Background(), opts); err != nil || state != StateDone {
				t.Fatalf("tick: %s, %v", state, err)
			}
			path := filepath.Join(f.root, ".batuta/roadmap.md")
			switch changed {
			case "branch":
				f.run(t, "checkout", "-qb", "operator")
			case "staged":
				if err := os.WriteFile(filepath.Join(f.root, "source.txt"), []byte("operator work"), 0600); err != nil {
					t.Fatal(err)
				}
				f.run(t, "add", "source.txt")
			case "roadmap":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, []byte("\nOperator notes.\n")...), 0600); err != nil {
					t.Fatal(err)
				}
			}
			head, index := f.run(t, "rev-parse", "HEAD"), f.run(t, "diff", "--cached")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := RunRoadmap(context.Background(), opts); err != nil || state != StateDone {
				t.Fatalf("re-observe: %s, %v", state, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || head != f.run(t, "rev-parse", "HEAD") || index != f.run(t, "diff", "--cached") {
				t.Fatal("changed operator work")
			}
		})
	}
}

func tempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
