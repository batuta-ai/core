package council

import (
	"strings"
	"testing"
)

func TestCritiquePrompt(t *testing.T) {
	t.Parallel()
	plan := "# Plan\n- [ ] 1. Keep this line verbatim.\n\n"
	conventions := []string{"## Conventions\nUse table-driven tests.", "## More conventions\nKeep scope closed."}
	got := BuildCritiquePrompt(plan, conventions)
	for _, want := range []string{
		plan,
		conventions[0], conventions[1],
		"task size", "verifiable criteria", "Scope", "test fixtures", "vacuously", "dependency order", "risks",
		"<<<COUNCIL", "COUNCIL>>>", `{"task":1,"severity":"blocker","claim":"...","fix":"..."}`,
		"VERDICT: APPROVE", "VERDICT: REVISE", "read-only",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Count(got, plan) != 1 {
		t.Errorf("plan should appear exactly once, got %d", strings.Count(got, plan))
	}
}

func TestCrossReviewPrompt(t *testing.T) {
	t.Parallel()
	critiques := []Critique{
		{Label: "A", Findings: []Finding{{Task: 1, Severity: Blocker, Claim: "missing fixture", Fix: "add fixture"}}, Verdict: Revise},
		{Label: "B", Findings: []Finding{{Task: 2, Severity: Minor, Claim: "risk unclear", Fix: "state risk"}}, Verdict: Approve},
		{Label: "C", Verdict: Approve},
	}
	got := BuildCrossReviewPrompt(critiques, "C")
	for _, want := range []string{"Critique A", "Critique B", "A1:", "B1:", "AGREE", "DISAGREE", "FINAL RANKING:", "missing fixture", "risk unclear"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	for _, forbidden := range []string{"Critique C", "codex", "gpt-6-sol", "claude"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("prompt contains %q", forbidden)
		}
	}
}

func TestCrossReviewPromptFormatExample(t *testing.T) {
	t.Parallel()
	critiques := []Critique{
		{Label: "A", Findings: []Finding{{Task: 1}}, Verdict: Revise},
		{Label: "B", Findings: []Finding{{Task: 2}}, Verdict: Revise},
		{Label: "C", Verdict: Approve},
	}
	tests := []struct {
		name      string
		ownLabel  string
		want      []string
		forbidden []string
	}{
		{"two shown findings", "C", []string{"A1: AGREE", "B1: DISAGREE", "FINAL RANKING:\n1. Critique A\n2. Critique B"}, []string{"Critique C"}},
		{"own label excluded", "A", []string{"B1: AGREE", "FINAL RANKING:\n1. Critique B\n2. Critique C"}, []string{"A1: AGREE", "Critique A"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BuildCrossReviewPrompt(critiques, tc.ownLabel)
			for _, want := range append(tc.want, "A critique with no findings needs no votes.", "Print nothing after the ranking.") {
				if !strings.Contains(got, want) {
					t.Errorf("prompt missing %q", want)
				}
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(got, forbidden) {
					t.Errorf("prompt contains %q", forbidden)
				}
			}
		})
	}
}

func TestChairmanPrompt(t *testing.T) {
	t.Parallel()
	critiques := []Critique{
		{Label: "A", Findings: []Finding{{Task: 1, Severity: Blocker, Claim: "missing fixture", Fix: "add fixture"}}, Verdict: Revise},
		{Label: "B", Findings: []Finding{{Task: 2, Severity: Minor, Claim: "risk unclear", Fix: "state risk"}}, Verdict: Approve},
	}
	aggregate := Aggregate(critiques, []CrossReview{{Reviewer: "A", Ranking: []string{"B"}}, {Reviewer: "B", Ranking: []string{"A"}}})
	got := BuildChairmanPrompt(aggregate, critiques)
	for _, want := range []string{"Aggregate recommendation:", "Critique A", "Critique B", "missing fixture", "risk unclear", "support 1", "average rank"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	for _, forbidden := range []string{"codex", "gpt-6-sol", "claude"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("prompt contains %q", forbidden)
		}
	}
}
