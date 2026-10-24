package loop

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/routing"
)

// goldenBriefInput is a fixed brief input without a grant. Its rendering in
// testdata/brief-no-grant.golden was captured from the brief code before
// standing grants existed; a brief without a grant must keep matching it.
func goldenBriefInput() BriefInput {
	return BriefInput{
		Plan: routing.Plan{
			Title:   "golden plan",
			Goal:    "Keep the brief stable.",
			Context: "Shared decision.\n\n**Task 2.** Decision for two.",
			Tasks:   []routing.PlanTask{{Number: 1}, {Number: 2}},
		},
		Task: routing.PlanTask{
			TaskArtifact: routing.TaskArtifact{
				Title:        "Render the golden brief",
				Domain:       "backend",
				Complexity:   "medium",
				Dependencies: []string{"1"},
			},
			Number: 2,
			Scope:  []string{"loop/brief.go", "loop/testdata/**"},
			Accept: []string{"the brief renders → go test ./loop/"},
		},
		Profile:  Profile{Stack: "go", Test: "go test ./...", Build: "go build ./..."},
		Worktree: "/tmp/worktree",
		Base:     "0123456789abcdef0123456789abcdef01234567",
		Criteria: []gates.Criterion{{Text: "the brief renders", Proof: "go test ./loop/"}},
		Feedback: []string{"the previous attempt failed its proof"},
		Question: "Which file holds the brief?",
		Answer:   "loop/brief.go",
		Lane:     "backend/medium",
	}
}

func TestBriefWithoutGrantUnchanged(t *testing.T) {
	t.Parallel()
	want, err := os.ReadFile(filepath.Join("testdata", "brief-no-grant.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got := Brief(goldenBriefInput()); got != string(want) {
		t.Fatalf("brief without a grant changed:\n--- got\n%s\n--- want\n%s", got, want)
	}
}
