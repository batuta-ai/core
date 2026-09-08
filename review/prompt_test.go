package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCohortPrompt(t *testing.T) {
	t.Parallel()
	manifest, _, opts := sessionFixture(t, 1)
	prompt, err := BuildCohortPrompt(opts.Root, manifest, manifest.Cohorts[0], "Review rubric.", []string{"Template conventions."})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"read-only", "blocker", "major", "minor", "nit", "Premise → Path → Verdict", "Premise → Improvement → Fix", "<<<FINDINGS", "FINDINGS>>>", "one JSON object per line", "free-text note", "every reported line", "file0.go", "new lines 1-1", "Review rubric.", "Template conventions.", "@@ -1 +1 @@\n-old\n+new\n"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
	if err := os.WriteFile(filepath.Join(opts.Root, "file0.go"), []byte("new\nnew line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildCohortPrompt(opts.Root, manifest, manifest.Cohorts[0], "", nil); err == nil {
		t.Fatal("accepted stale hunk ranges")
	}
}

func TestCohortPromptUntracked(t *testing.T) {
	t.Parallel()
	root := reviewRepo(t)
	writeTestFile(t, root, "odd name.go", "first\nsecond")
	manifest, err := BuildManifest(root, "HEAD", nil, ManifestOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := BuildCohortPrompt(root, manifest, manifest.Cohorts[0], "", nil)
	if err != nil || !strings.Contains(prompt, "@@ -0,0 +1,2 @@\n+first\n+second\n\\ No newline at end of file\n") {
		t.Fatalf("prompt=%s err=%v", prompt, err)
	}
}

func TestPromptMentionsOversized(t *testing.T) {
	t.Parallel()
	root := reviewRepo(t)
	writeTestFile(t, root, "huge.go", strings.Repeat("line\n", CohortLines+1))
	manifest, err := BuildManifest(root, "HEAD", nil, ManifestOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := BuildCohortPrompt(root, manifest, manifest.Cohorts[0], "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"oversized", "exceeds the usual", "review it in full"} {
		if !strings.Contains(strings.ToLower(prompt), want) {
			t.Fatalf("oversized prompt is missing %q:\n%s", want, prompt)
		}
	}
}

func TestSpecPromptListsWholeInventory(t *testing.T) {
	manifest := Manifest{Base: "base", Files: []File{
		{Path: "source.go", Selected: true, Added: 1},
		{Path: "go.sum", Ignored: true, IgnoreReason: "lock", Added: 2, Deleted: 1},
		{Path: "generated.go", Ignored: true, IgnoreReason: "generated", Added: 3},
		{Path: "unselected.go", Added: 4},
	}}
	prompt := BuildSpecPrompt(manifest, []SpecRule{{ID: "task-1.1", Text: "go.sum records the required checksums"}})
	for _, want := range []string{
		"source.go (+1 -0) [selected=true ignored=false]",
		"go.sum (+2 -1) [selected=false ignored=true reason=lock]",
		"generated.go (+3 -0) [selected=false ignored=true reason=generated]",
		"unselected.go (+4 -0) [selected=false ignored=false]",
		"go.sum records the required checksums",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q in prompt: %s", want, prompt)
		}
	}
}
