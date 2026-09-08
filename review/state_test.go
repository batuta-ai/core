package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIncrementalStateRejectsMalformedCheckpoint(t *testing.T) {
	root := reviewRepo(t)
	writeTestFile(t, root, "tracked.go", "package tracked\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "base")
	head := gitTest(t, root, "rev-parse", "HEAD")
	filename := filepath.Join(root, "state.json")
	for _, payload := range []string{`{}`, `null`, `{"head":null}`, `{"head":""}`, `{"head":"missing"}`, `{"head":"` + head + `","pending":[{"files":[{"path":"../escape"}]}]}`} {
		if err := os.WriteFile(filename, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadIncrementalState(root, filename, head, false); err == nil {
			t.Errorf("accepted malformed checkpoint %s", payload)
		}
		state, err := LoadIncrementalState(root, filename, head, true)
		if err != nil || state.Head != head || len(state.Pending) != 0 {
			t.Fatalf("full review = %+v, %v", state, err)
		}
	}
}

func TestStateAfterReportRequiresCompleteCoverage(t *testing.T) {
	file := File{Path: "change.go", Selected: true, Hunks: []Hunk{{Start: 3, Count: 2}}}
	manifest := Manifest{Base: "base", Files: []File{file}, Cohorts: []Cohort{{Files: []string{file.Path}}}}
	for _, tc := range []struct {
		name    string
		cohorts []CohortResult
		spec    *SpecSweep
		want    string
		pending int
	}{
		{"missing cohort", nil, nil, "base", 1},
		{"uncovered", []CohortResult{{Cohort: 0}}, nil, "base", 1},
		{"spec incomplete", []CohortResult{{Cohort: 0, Covered: true}}, &SpecSweep{}, "base", 1},
		{"covered", []CohortResult{{Cohort: 0, Covered: true}}, &SpecSweep{Covered: true}, "head", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := StateAfterReport(BuildReport(manifest, tc.cohorts, tc.spec), "head")
			if state.Head != tc.want || len(state.Pending) != tc.pending {
				t.Fatalf("state = %+v", state)
			}
			if tc.pending > 0 && !slices.Equal(state.Pending[0].Files[0].Hunks, file.Hunks) {
				t.Fatalf("pending hunks = %+v", state.Pending)
			}
			filename := filepath.Join(t.TempDir(), "state", "branch.json")
			if err := WriteIncrementalState(filename, state); err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(filename)
			var persisted IncrementalState
			if err != nil || json.Unmarshal(payload, &persisted) != nil || persisted.Head != state.Head || len(persisted.Pending) != len(state.Pending) {
				t.Fatalf("persisted state = %s, %v", payload, err)
			}
		})
	}
}

func TestPendingManifestDoesNotSelectOtherUntrackedFiles(t *testing.T) {
	root := reviewRepo(t)
	writeTestFile(t, root, "tracked.go", "package tracked\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "base")
	head := gitTest(t, root, "rev-parse", "HEAD")
	writeTestFile(t, root, "pending.go", "package pending\n")
	writeTestFile(t, root, "unrelated.go", strings.Repeat("line\n", CohortLines+1))
	state := IncrementalState{Head: head, Pending: []PendingCohort{{Files: []File{{Path: "pending.go", Untracked: true, Hunks: []Hunk{{Start: 1, Count: 1}}}}}}}
	manifest, err := BuildIncrementalManifest(root, state, ManifestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cohorts) != 1 || !slices.Equal(manifest.Cohorts[0].Files, []string{"pending.go"}) {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestStateKeepsPendingWhileSpecUncovered(t *testing.T) {
	root := reviewRepo(t)
	base := gitTest(t, root, "rev-parse", "HEAD")
	writeTestFile(t, root, "new.go", "package new\n")
	manifest, err := BuildManifest(root, base, nil, ManifestOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	report := BuildReport(manifest, []CohortResult{{Cohort: 0, Covered: true}}, &SpecSweep{})
	state := StateAfterReport(report, "later-head")
	if state.Head != base || len(state.Pending) != 1 || len(state.Pending[0].Files) != 1 || state.Pending[0].Files[0].Path != "new.go" {
		t.Fatalf("uncovered spec lost pending file: %+v", state)
	}
	filename := filepath.Join(t.TempDir(), "state.json")
	if err := WriteIncrementalState(filename, state); err != nil {
		t.Fatal(err)
	}
	state, err = LoadIncrementalState(root, filename, base, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "unrelated.go", "package unrelated\n")
	retry, err := BuildIncrementalManifest(root, state, ManifestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Files) != 1 || retry.Files[0].Path != "new.go" || !retry.Files[0].Untracked {
		t.Fatalf("retry = %+v", retry)
	}
	prompt := BuildSpecPrompt(retry, []SpecRule{{ID: "task-1.1", Text: "new.go is present"}})
	if !strings.Contains(prompt, "new.go (+1 -0)") {
		t.Fatalf("incomplete retry: %s", prompt)
	}
	completed := StateAfterReport(BuildReport(retry, []CohortResult{{Cohort: 0, Covered: true}}, &SpecSweep{Covered: true}), "later-head")
	if completed.Head != "later-head" || len(completed.Pending) != 0 {
		t.Fatalf("completed = %+v", completed)
	}
}
