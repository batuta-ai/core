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
		{"spec incomplete", []CohortResult{{Cohort: 0, Covered: true}}, &SpecSweep{}, "base", 0},
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
