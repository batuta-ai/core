package council

import (
	"testing"
)

func TestAggregate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		critiques    []Critique
		reviews      []CrossReview
		wantVerdict  Verdict
		wantSupport  int
		wantFindings int
		wantAverageA float64
	}{
		{
			name: "merged blocker with majority support",
			critiques: []Critique{
				{Label: "A", Verdict: Revise, Findings: []Finding{{Task: 1, Severity: Blocker, Claim: "Missing fixture.", Fix: "Add fixture"}}},
				{Label: "B", Verdict: Approve, Findings: []Finding{{Task: 1, Severity: Major, Claim: " missing  FIXTURE ", Fix: "Add test"}}},
				{Label: "C", Verdict: Approve},
			},
			reviews: []CrossReview{
				{Reviewer: "A", Votes: map[string]bool{"B1": true}, Ranking: []string{"B", "C"}},
				{Reviewer: "B", Votes: map[string]bool{"A1": true}, Ranking: []string{"A", "C"}},
				{Reviewer: "C", Votes: map[string]bool{"A1": false, "B1": false}, Ranking: []string{"A", "B"}},
			},
			wantVerdict: Revise, wantSupport: 2, wantFindings: 1, wantAverageA: 1,
		},
		{
			name: "minority blocker and approve majority",
			critiques: []Critique{
				{Label: "A", Verdict: Revise, Findings: []Finding{{Task: 1, Severity: Blocker, Claim: "Risk", Fix: "Explain"}}},
				{Label: "B", Verdict: Approve},
				{Label: "C", Verdict: Approve},
			},
			wantVerdict: Approve, wantSupport: 1, wantFindings: 1,
		},
		{
			name:        "verdict majority overrides findings",
			critiques:   []Critique{{Label: "A", Verdict: Revise}, {Label: "B", Verdict: Revise}, {Label: "C", Verdict: Approve}},
			wantVerdict: Revise,
		},
		{
			name: "task keeps claims separate",
			critiques: []Critique{
				{Label: "A", Verdict: Approve, Findings: []Finding{{Task: 1, Severity: Minor, Claim: "Risk", Fix: "Explain"}}},
				{Label: "B", Verdict: Approve, Findings: []Finding{{Task: 2, Severity: Minor, Claim: "Risk", Fix: "Explain"}}},
			},
			wantVerdict: Approve, wantFindings: 2, wantSupport: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Aggregate(tc.critiques, tc.reviews)
			if got.Recommendation != tc.wantVerdict || len(got.Findings) != tc.wantFindings {
				t.Fatalf("aggregate = %+v", got)
			}
			if tc.wantFindings > 0 && got.Findings[0].Support != tc.wantSupport {
				t.Errorf("support = %d, want %d", got.Findings[0].Support, tc.wantSupport)
			}
			if tc.wantAverageA > 0 {
				for _, rank := range got.Rankings {
					if rank.Label == "A" && rank.Average != tc.wantAverageA {
						t.Errorf("A average = %v, want %v", rank.Average, tc.wantAverageA)
					}
				}
			}
		})
	}
}
