package questions

import (
	"strings"
	"testing"
	"unicode/utf8"

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

func TestPassageSecretEntries(t *testing.T) {
	t.Parallel()
	task := routing.PlanTask{
		Number:       1,
		TaskArtifact: routing.TaskArtifact{Title: "Ship parser"},
		Scope:        []string{"API_TOKEN=scope-secret", "questions/passage.go"},
		Accept:       []string{"DB_PASSWORD=accept-secret", "passage is bounded"},
	}
	got := Passage(routing.Plan{}, task)
	want := "Ship parser\nScope: questions/passage.go\nAccept: passage is bounded"
	if got != want {
		t.Errorf("Passage() = %q, want %q", got, want)
	}
}

func TestPassageUTF8Bound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		context string
		first   string
	}{
		{"two-byte rune across the bound", strings.Repeat("a", 3986) + strings.Repeat("é", 100), "é"},
		{"three-byte rune across the bound", strings.Repeat("a", 3985) + strings.Repeat("世", 100), "世"},
		{"four-byte rune across the bound", strings.Repeat("a", 3984) + strings.Repeat("😀", 100), "😀"},
		{"ascii at the bound", strings.Repeat("a", 5000), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			task := routing.PlanTask{Number: 1, TaskArtifact: routing.TaskArtifact{Title: "Short title"}}
			got := Passage(routing.Plan{Context: tt.context}, task)
			full := "Short title\n\n" + tt.context
			if len(got) > 4000 || !utf8.ValidString(got) || !strings.HasPrefix(full, got) {
				t.Fatalf("Passage() length = %d, valid = %t", len(got), utf8.ValidString(got))
			}
			if tt.first != "" {
				start := strings.Index(full, tt.first)
				if start >= 4000 || start+len(tt.first) <= 4000 {
					t.Fatalf("fixture: first %q spans bytes %d-%d, want across byte 4000", tt.first, start, start+len(tt.first))
				}
				if len(got) != start {
					t.Errorf("Passage() length = %d, want cut before %q at %d", len(got), tt.first, start)
				}
			}
			if len(got) < 4000-utf8.UTFMax+1 {
				t.Errorf("Passage() cut too early: length = %d", len(got))
			}
		})
	}
}
