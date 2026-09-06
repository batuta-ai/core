package loop

import (
	"strings"
	"testing"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/routing"
)

func TestBriefCarriesTheProgressProtocol(t *testing.T) {
	t.Parallel()
	brief := Brief(BriefInput{Criteria: []gates.Criterion{
		{Text: "first criterion", Proof: "test -f first"},
		{Text: "second criterion", Proof: "test -f second"},
	}})
	_, section, found := strings.Cut(brief, "## Progress protocol\n\n")
	if !found {
		t.Fatalf("brief has no progress protocol section:\n%s", brief)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, want := range []string{
		"BATUTA-PROGRESS <n> START", "before the first edit toward it",
		"BATUTA-PROGRESS <n> DONE", "when its proof passes locally",
		"plain text on stdout", "nothing else on that line", "no tool required",
		"1-based positions", "## Acceptance criteria", "BATUTA-QUESTION:",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("progress protocol lacks %q:\n%s", want, section)
		}
	}
}

func TestBriefCarriesOnlySharedAndOwnDecisions(t *testing.T) {
	t.Parallel()
	plan := routing.Plan{
		Context: "Shared decision.\n\n**Task 1.** Decision for one.\n\n**Tasks 2–3.** Decision for two and three.",
		Tasks: []routing.PlanTask{
			{Number: 1},
			{Number: 2},
			{Number: 3},
		},
	}
	for _, tc := range []struct {
		task  routing.PlanTask
		own   string
		other string
	}{
		{task: routing.PlanTask{Number: 1}, own: "Decision for one.", other: "Decision for two and three."},
		{task: routing.PlanTask{Number: 2}, own: "Decision for two and three.", other: "Decision for one."},
	} {
		brief := Brief(BriefInput{Plan: plan, Task: tc.task})
		if !strings.Contains(brief, "Shared decision.") || !strings.Contains(brief, tc.own) || strings.Contains(brief, tc.other) {
			t.Errorf("brief for task %d has wrong context:\n%s", tc.task.Number, brief)
		}
	}
}
