package loop

import (
	"fmt"
	"strings"
	"testing"

	"github.com/batuta-ai/core/routing"
)

func TestCommitMessageLongTitleTaskIdentity(t *testing.T) {
	t.Parallel()

	issueTitle := "review: findings always a list, structured review failures in the report and a new artifact"
	unicodeLongTitle := "ação: " + strings.Repeat("árvore ", 15) + "finalização completa de tarefas"
	noSpacesLongTitle := strings.Repeat("A", 80)

	tests := []struct {
		name        string
		task        routing.PlanTask
		slug        string
		wantSubject string
		wantBody    string
	}{
		{
			name: "issue example retains full title in body and uses task identity in subject",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{
					ID:    "task_1",
					Title: issueTitle,
				},
			},
			slug:        "review-contract-preflight",
			wantSubject: "feat: complete task_1",
			wantBody:    fmt.Sprintf("%s\n\nPlan review-contract-preflight, task_1. Delivered by batuta loop.\n", issueTitle),
		},
		{
			name: "unicode long title retains full title in body and uses task identity in subject",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{
					ID:    "task_2",
					Title: unicodeLongTitle,
				},
			},
			slug:        "unicode-plan",
			wantSubject: "feat: complete task_2",
			wantBody:    fmt.Sprintf("%s\n\nPlan unicode-plan, task_2. Delivered by batuta loop.\n", unicodeLongTitle),
		},
		{
			name: "long title without spaces retains full title in body and uses task identity in subject",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{
					ID:    "task_3",
					Title: noSpacesLongTitle,
				},
			},
			slug:        "nospaces-plan",
			wantSubject: "feat: complete task_3",
			wantBody:    fmt.Sprintf("%s\n\nPlan nospaces-plan, task_3. Delivered by batuta loop.\n", noSpacesLongTitle),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := commitMessage(tt.task, tt.slug)
			lines := strings.Split(got, "\n")
			if len(lines) == 0 {
				t.Fatalf("commitMessage returned empty string")
			}
			subject := lines[0]
			if subject != tt.wantSubject {
				t.Errorf("subject = %q, want %q", subject, tt.wantSubject)
			}
			parts := strings.SplitN(subject, ": ", 2)
			if len(parts) != 2 {
				t.Fatalf("expected subject line to contain ': ', got %q", subject)
			}
			if len(parts[1]) > 68 {
				t.Errorf("subject title part %q is %d bytes, exceeds budget 68", parts[1], len(parts[1]))
			}

			body := strings.Join(lines[2:], "\n")
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}
