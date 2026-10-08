package council

import (
	"testing"
)

func TestAggregate(t *testing.T) {
	t.Parallel()
	pair := []CrossReview{{Reviewer: "A"}, {Reviewer: "B"}}
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
			reviews:     pair,
			wantVerdict: Approve, wantSupport: 1, wantFindings: 1,
		},
		{
			name:        "verdict majority overrides findings",
			critiques:   []Critique{{Label: "A", Verdict: Revise}, {Label: "B", Verdict: Revise}, {Label: "C", Verdict: Approve}},
			reviews:     pair,
			wantVerdict: Revise,
		},
		{
			name:        "one cross-review is incomplete",
			critiques:   []Critique{{Label: "A", Verdict: Revise}, {Label: "B", Verdict: Revise}, {Label: "C", Verdict: Approve}},
			reviews:     pair[:1],
			wantVerdict: Incomplete,
		},
		{
			name:        "no cross-review is incomplete",
			critiques:   []Critique{{Label: "A", Verdict: Approve}, {Label: "B", Verdict: Approve}},
			wantVerdict: Incomplete,
		},
		{
			name: "task keeps claims separate",
			critiques: []Critique{
				{Label: "A", Verdict: Approve, Findings: []Finding{{Task: 1, Severity: Minor, Claim: "Risk", Fix: "Explain"}}},
				{Label: "B", Verdict: Approve, Findings: []Finding{{Task: 2, Severity: Minor, Claim: "Risk", Fix: "Explain"}}},
			},
			reviews:     pair,
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

func TestAggregateMajorMajority(t *testing.T) {
	t.Parallel()
	pair := []CrossReview{{Reviewer: "A"}, {Reviewer: "B"}}
	finding := func(severity Severity) []Finding {
		return []Finding{{Task: 1, Severity: severity, Claim: "Risk", Fix: "Explain"}}
	}
	tests := []struct {
		name        string
		critiques   []Critique
		wantVerdict Verdict
	}{
		{
			name: "major raised by a majority revises",
			critiques: []Critique{
				{Label: "A", Verdict: Approve, Findings: finding(Major)},
				{Label: "B", Verdict: Approve, Findings: finding(Major)},
				{Label: "C", Verdict: Approve},
			},
			wantVerdict: Revise,
		},
		{
			name: "major minority approves",
			critiques: []Critique{
				{Label: "A", Verdict: Approve, Findings: finding(Major)},
				{Label: "B", Verdict: Approve},
				{Label: "C", Verdict: Approve},
			},
			wantVerdict: Approve,
		},
		{
			name: "minor with full support approves",
			critiques: []Critique{
				{Label: "A", Verdict: Approve, Findings: finding(Minor)},
				{Label: "B", Verdict: Approve, Findings: finding(Minor)},
				{Label: "C", Verdict: Approve, Findings: finding(Minor)},
			},
			wantVerdict: Approve,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Aggregate(tc.critiques, pair)
			if got.Recommendation != tc.wantVerdict {
				t.Fatalf("recommendation = %s, want %s: %+v", got.Recommendation, tc.wantVerdict, got)
			}
		})
	}
}

func TestAggregateRankEntries(t *testing.T) {
	t.Parallel()
	critiques := []Critique{
		{Label: "A", Verdict: Approve},
		{Label: "B", Verdict: Approve},
		{Label: "C", Verdict: Approve},
	}
	reviews := []CrossReview{
		{Reviewer: "A", Ranking: []string{"B", "C"}},
		{Reviewer: "B", Ranking: []string{"A", "C"}},
		{Reviewer: "C", Ranking: []string{"B", "A"}},
	}
	got := Aggregate(critiques, reviews)
	if len(got.Rankings) != len(critiques) {
		t.Fatalf("rankings = %+v, want %d entries", got.Rankings, len(critiques))
	}
	var found bool
	for _, rank := range got.Rankings {
		if rank.Label != "A" {
			continue
		}
		found = true
		if rank.Average != 1.5 || rank.Count != 2 {
			t.Errorf("A = average %v count %d, want 1.5 and 2", rank.Average, rank.Count)
		}
	}
	if !found {
		t.Fatalf("critique A missing from rankings: %+v", got.Rankings)
	}
}
