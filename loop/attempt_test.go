package loop

import (
	"strings"
	"testing"

	"github.com/batuta-ai/core/routing"
)

func TestCommitMessageKeepsCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		title    string
		expected string
	}{
		{
			name:     "PRD acronym keeps case",
			title:    "PRD-v1.md in English",
			expected: "PRD-v1.md in English",
		},
		{
			name:     "gofmt lowercase acronym keeps case",
			title:    "gofmt the tree",
			expected: "gofmt the tree",
		},
		{
			name:     "Download deadline becomes download deadline",
			title:    "Download deadline",
			expected: "download deadline",
		},
		{
			name:     "single character uppercase",
			title:    "A",
			expected: "A",
		},
		{
			name:     "single character lowercase",
			title:    "a",
			expected: "a",
		},
		{
			name:     "empty title",
			title:    "",
			expected: "",
		},
		{
			name:     "two uppercase characters",
			title:    "UI improvement",
			expected: "UI improvement",
		},
		{
			name:     "non-letter second character",
			title:    "C-style formatting",
			expected: "C-style formatting",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			task := routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{
					ID:    "task-1",
					Title: tt.title,
				},
			}
			got := commitMessage(task, "test-plan")
			lines := strings.Split(got, "\n")
			if len(lines) == 0 {
				t.Fatalf("commitMessage returned empty string")
			}
			parts := strings.SplitN(lines[0], ": ", 2)
			if len(parts) != 2 {
				t.Fatalf("expected subject line to contain ': ', got %q", lines[0])
			}
			subject := parts[1]
			if subject != tt.expected {
				t.Errorf("got subject %q, want %q", subject, tt.expected)
			}
		})
	}
}

func TestCommitMessageCutsAtWordBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		title    string
		expected string
	}{
		{
			name:     "PRD-v1.md translation cut at word boundary before 68 bytes",
			title:    "PRD-v1.md in English: the historical PRD is translated in place, strings",
			expected: "PRD-v1.md in English: the historical PRD is translated in place",
		},
		{
			name:     "long title without spaces cut at 68",
			title:    strings.Repeat("a", 80),
			expected: strings.Repeat("a", 68),
		},
		{
			name:     "title <= 68 remains intact",
			title:    "Short title under limit",
			expected: "short title under limit",
		},
		{
			name:     "trims trailing punctuation after word boundary cut",
			title:    "This is a long title that needs to be cut here, and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
		{
			name:     "trims trailing colon after cut",
			title:    "This is a long title that needs to be cut here: and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
		{
			name:     "trims trailing semicolon after cut",
			title:    "This is a long title that needs to be cut here; and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
		{
			name:     "trims trailing em-dash after cut",
			title:    "This is a long title that needs to be cut here— and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
		{
			name:     "trims trailing hyphen after cut",
			title:    "This is a long title that needs to be cut here- and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			task := routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{
					ID:    "task-1",
					Title: tt.title,
				},
			}
			got := commitMessage(task, "test-plan")
			lines := strings.Split(got, "\n")
			if len(lines) == 0 {
				t.Fatalf("commitMessage returned empty string")
			}
			parts := strings.SplitN(lines[0], ": ", 2)
			if len(parts) != 2 {
				t.Fatalf("expected subject line to contain ': ', got %q", lines[0])
			}
			subject := parts[1]
			if subject != tt.expected {
				t.Errorf("got subject %q, want %q", subject, tt.expected)
			}
		})
	}
}

func TestCommitMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		task         routing.PlanTask
		slug         string
		expectedKind string
	}{
		{
			name: "docs domain",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-1", Domain: routing.DomainDocs, Title: "Update documentation"},
			},
			slug:         "my-plan",
			expectedKind: "docs",
		},
		{
			name: "testing domain",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-2", Domain: routing.DomainTesting, Title: "Add unit tests"},
			},
			slug:         "my-plan",
			expectedKind: "test",
		},
		{
			name: "fix title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-3", Title: "Fix memory leak"},
			},
			slug:         "my-plan",
			expectedKind: "fix",
		},
		{
			name: "repair title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-4", Title: "Repair broken socket"},
			},
			slug:         "my-plan",
			expectedKind: "fix",
		},
		{
			name: "bug in title",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-5", Title: "Resolve concurrency bug in worker"},
			},
			slug:         "my-plan",
			expectedKind: "fix",
		},
		{
			name: "refactor title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-6", Title: "Refactor router handlers"},
			},
			slug:         "my-plan",
			expectedKind: "refactor",
		},
		{
			name: "extract title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-7", Title: "Extract helper functions"},
			},
			slug:         "my-plan",
			expectedKind: "refactor",
		},
		{
			name: "rename title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-8", Title: "Rename ambiguous variable"},
			},
			slug:         "my-plan",
			expectedKind: "refactor",
		},
		{
			name: "infra domain",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-9", Domain: routing.DomainInfra, Title: "Setup docker build"},
			},
			slug:         "my-plan",
			expectedKind: "chore",
		},
		{
			name: "default feat",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-10", Title: "Build awesome feature"},
			},
			slug:         "my-plan",
			expectedKind: "feat",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := commitMessage(tt.task, tt.slug)
			expectedPrefix := tt.expectedKind + ": "
			if !strings.HasPrefix(got, expectedPrefix) {
				t.Errorf("expected commit message to start with %q, got %q", expectedPrefix, got)
			}
			expectedTrailer := "\n\nPlan " + tt.slug + ", " + tt.task.ID + ". Delivered by batuta loop.\n"
			if !strings.HasSuffix(got, expectedTrailer) {
				t.Errorf("expected commit message to end with trailer %q, got %q", expectedTrailer, got)
			}
		})
	}
}
