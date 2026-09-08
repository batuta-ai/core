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
