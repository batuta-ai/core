package review

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// IncrementalState keeps the covered checkpoint separate from dated reports.
// Pending files retain their hunk ranges as evidence of incomplete coverage.
type IncrementalState struct {
	Head    string          `json:"head"`
	Pending []PendingCohort `json:"pending,omitempty"`
}

type PendingCohort struct {
	Files []File `json:"files"`
}

func LoadIncrementalState(root, filename, base string, full bool) (IncrementalState, error) {
	state := IncrementalState{Head: base}
	if full {
		return state, nil
	}
	file, err := os.Open(filename)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("review: read prior state: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return state, fmt.Errorf("review: read prior state: %w", err)
	}
	if len(payload) > 1<<20 {
		return state, fmt.Errorf("review: prior state exceeds 1 MiB")
	}
	state = IncrementalState{}
	if json.Unmarshal(payload, &state) != nil || state.Head == "" || strings.TrimSpace(state.Head) != state.Head || strings.ContainsAny(state.Head, "\x00\r\n") {
		return state, fmt.Errorf("review: prior state has an invalid head")
	}
	resolved, err := gitOutput(root, "rev-parse", "--verify", "--end-of-options", state.Head+"^{commit}")
	if err != nil {
		return state, fmt.Errorf("review: prior head is unavailable")
	}
	state.Head = strings.TrimSpace(string(resolved))
	if _, err := gitOutput(root, "merge-base", "--is-ancestor", state.Head, "HEAD"); err != nil {
		return state, fmt.Errorf("review: prior head is not an ancestor of HEAD")
	}
	for _, cohort := range state.Pending {
		for _, file := range cohort.Files {
			if !validPath(file.Path) {
				return state, fmt.Errorf("review: invalid pending path %q", file.Path)
			}
		}
	}
	return state, nil
}

func WriteIncrementalState(filename string, state IncrementalState) error {
	payload, err := jsonPayload(state)
	if err != nil {
		return err
	}
	if len(payload) > 1<<20 {
		return fmt.Errorf("review: state exceeds 1 MiB")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	return writeArtifact(filepath.Dir(filename), filepath.Base(filename), payload)
}

// BuildIncrementalManifest recomputes pending hunks against the preserved base,
// including previously uncovered untracked files even without --worktree.
func BuildIncrementalManifest(root string, state IncrementalState, opts ManifestOptions) (Manifest, error) {
	manifest, err := BuildManifest(root, state.Head, nil, opts)
	if err != nil || opts.Worktree || len(state.Pending) == 0 {
		return manifest, err
	}
	seen := make(map[string]bool)
	for _, file := range manifest.Files {
		seen[file.Path] = true
	}
	workspace, err := os.OpenRoot(root)
	if err != nil {
		return manifest, err
	}
	defer workspace.Close()
	for _, cohort := range state.Pending {
		for _, previous := range cohort.Files {
			if seen[previous.Path] {
				continue
			}
			untracked, err := gitOutput(root, "ls-files", "--others", "--exclude-standard", "-z", "--", previous.Path)
			if err != nil {
				return manifest, err
			}
			if string(untracked) != previous.Path+"\x00" {
				continue
			}
			file := File{Path: previous.Path, Status: "?", Untracked: true}
			if err := inspectFile(root, workspace, manifest.Base, &file); err != nil {
				return manifest, err
			}
			file.Selected = !file.Ignored
			manifest.Files = append(manifest.Files, file)
			seen[file.Path] = true
		}
	}
	slices.SortFunc(manifest.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	limit := opts.CohortFiles
	if limit == 0 {
		limit = CohortFiles
	}
	manifest.Cohorts, err = buildCohorts(manifest.Files, limit)
	return manifest, err
}

func StateAfterReport(report Report, head string) IncrementalState {
	state := IncrementalState{Head: report.Manifest.Base}
	covered := make(map[int]bool)
	for _, result := range report.Cohorts {
		covered[result.Cohort] = result.Covered
	}
	files := make(map[string]File)
	for _, file := range report.Manifest.Files {
		files[file.Path] = file
	}
	for index, cohort := range report.Manifest.Cohorts {
		if covered[index] && (report.Spec == nil || report.Spec.Covered) {
			continue
		}
		pending := PendingCohort{}
		for _, name := range cohort.Files {
			pending.Files = append(pending.Files, files[name])
		}
		state.Pending = append(state.Pending, pending)
	}
	if len(state.Pending) == 0 && (report.Spec == nil || report.Spec.Covered) {
		state.Head = head
	}
	return state
}
