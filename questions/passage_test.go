package questions

import (
	"strings"
	"testing"

	"github.com/batuta-ai/core/routing"
)

func TestPassage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		context string
		want    string
	}{
		{
			name: "own context before shared context",
			context: "Shared decision.\n\n**Task 1.** Another task's rule.\n\n**Task 2.** Own rule.\n\n" +
				"MORE_SECRET=hidden\nSecond shared decision.",
			want: "Ship parser\nScope: questions/passage.go\nScope: questions/passage_test.go\n" +
				"Accept: passage is bounded\nAccept: secrets are absent\n\n**Task 2.** Own rule.\n\n" +
				"Shared decision.\n\nSecond shared decision.",
		},
		{
			name:    "unlabelled only",
			context: "First shared decision.\n\nSecond shared decision.",
			want: "Ship parser\nScope: questions/passage.go\nScope: questions/passage_test.go\n" +
				"Accept: passage is bounded\nAccept: secrets are absent\n\nFirst shared decision.\n\nSecond shared decision.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan := routing.Plan{Context: tt.context}
			task := routing.PlanTask{
				Number:       2,
				TaskArtifact: routing.TaskArtifact{Title: "Ship parser", Domain: routing.DomainBackend, Complexity: routing.ComplexityCritical},
				Scope:        []string{"questions/passage.go", "questions/passage_test.go"},
				Accept:       []string{"passage is bounded", "secrets are absent"},
			}
			if got := Passage(plan, task); got != tt.want {
				t.Errorf("Passage() = %q, want %q", got, tt.want)
			}
		})
	}
	t.Run("bounded to 4000 bytes", func(t *testing.T) {
		t.Parallel()
		plan := routing.Plan{Context: strings.Repeat("a", 5000)}
		task := routing.PlanTask{Number: 1, TaskArtifact: routing.TaskArtifact{Title: "Short title"}}
		got := Passage(plan, task)
		if len(got) != 4000 || !strings.HasPrefix(got, "Short title\n\n") {
			t.Errorf("Passage() length = %d, prefix = %q", len(got), got[:min(len(got), 30)])
		}
	})
}
