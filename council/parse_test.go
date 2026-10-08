package council

import (
	"strings"
	"testing"
)

func TestParseCritique(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output string
		valid  bool
	}{
		{"valid", "<<<COUNCIL\n{\"task\":1,\"severity\":\"blocker\",\"claim\":\"Missing fixture\",\"fix\":\"Add one\"}\nCOUNCIL>>>\nVERDICT: REVISE", true},
		{"empty", "<<<COUNCIL\nCOUNCIL>>>\nVERDICT: APPROVE", true},
		{"missing block", "VERDICT: APPROVE", false},
		{"missing close", "<<<COUNCIL\nVERDICT: APPROVE", false},
		{"duplicate block", "<<<COUNCIL\nCOUNCIL>>>\n<<<COUNCIL\nCOUNCIL>>>\nVERDICT: APPROVE", false},
		{"malformed JSON", "<<<COUNCIL\n{bad}\nCOUNCIL>>>\nVERDICT: REVISE", false},
		{"unknown severity", "<<<COUNCIL\n{\"task\":1,\"severity\":\"critical\",\"claim\":\"x\",\"fix\":\"y\"}\nCOUNCIL>>>\nVERDICT: REVISE", false},
		{"unknown field", "<<<COUNCIL\n{\"task\":1,\"severity\":\"minor\",\"claim\":\"x\",\"fix\":\"y\",\"extra\":1}\nCOUNCIL>>>\nVERDICT: REVISE", false},
		{"duplicate field", "<<<COUNCIL\n{\"task\":1,\"task\":2,\"severity\":\"minor\",\"claim\":\"x\",\"fix\":\"y\"}\nCOUNCIL>>>\nVERDICT: REVISE", false},
		{"wrong verdict", "<<<COUNCIL\nCOUNCIL>>>\nVERDICT: MAYBE", false},
		{"extra text", "<<<COUNCIL\nCOUNCIL>>>\nVERDICT: APPROVE\nmore", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCritique(tc.output, "A")
			if (err == nil) != tc.valid {
				t.Fatalf("ParseCritique() = %+v, %v; valid = %t", got, err, tc.valid)
			}
			if tc.name == "valid" && (got.Label != "A" || got.Verdict != Revise || len(got.Findings) != 1 || got.Findings[0].Task != 1) {
				t.Fatalf("critique = %+v", got)
			}
		})
	}
}

func TestParseCrossReview(t *testing.T) {
	t.Parallel()
	critiques := []Critique{
		{Label: "A", Findings: []Finding{{Task: 1, Severity: Blocker, Claim: "x", Fix: "y"}}},
		{Label: "B", Findings: []Finding{{Task: 2, Severity: Major, Claim: "x", Fix: "y"}}},
		{Label: "C"},
	}
	tests := []struct {
		name   string
		output string
		valid  bool
	}{
		{"valid", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. A", true},
		{"unknown id", "A2: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. A", false},
		{"unknown label", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. D", false},
		{"own label", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. C\n2. A", false},
		{"missing vote", "A1: AGREE\nFINAL RANKING:\n1. B\n2. A", false},
		{"duplicate vote", "A1: AGREE\nA1: DISAGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. A", false},
		{"bad vote", "A1: MAYBE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. A", false},
		{"repeated rank", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. A\n2. A", false},
		{"omitted rank", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. A", false},
		{"missing ranking", "A1: AGREE\nB1: DISAGREE", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCrossReview(tc.output, critiques, "C")
			if (err == nil) != tc.valid {
				t.Fatalf("ParseCrossReview() = %+v, %v; valid = %t", got, err, tc.valid)
			}
			if tc.valid && (got.Reviewer != "C" || !got.Votes["A1"] || got.Votes["B1"] || strings.Join(got.Ranking, ",") != "B,A") {
				t.Fatalf("cross-review = %+v", got)
			}
		})
	}
}
