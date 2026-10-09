package council

import (
	"os"
	"strings"
	"testing"
)

func TestParseRecordedAnswers(t *testing.T) {
	t.Parallel()
	read := func(t *testing.T, name string) string {
		t.Helper()
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	critique, err := ParseCritique(read(t, "claude-critique.txt"), "B")
	if err != nil {
		t.Fatal(err)
	}
	if critique.Label != "B" || critique.Verdict != Revise || len(critique.Findings) != 3 {
		t.Fatalf("claude critique = %+v", critique)
	}
	for i, finding := range critique.Findings {
		if finding.Task != i+1 {
			t.Fatalf("finding %d = %+v", i+1, finding)
		}
	}
	critiques := []Critique{{Label: "A"}, {Label: "C", Findings: make([]Finding, 4)}}
	tests := []struct {
		name     string
		reviewer string
		votes    map[string]bool
		ranking  string
	}{
		{"agy-cross-review.txt", "A", map[string]bool{"C1": true, "C2": true, "C3": true, "C4": true}, "C"},
		{"codex-cross-review.txt", "C", map[string]bool{}, "A"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCrossReview(read(t, tc.name), critiques, tc.reviewer)
			if err != nil {
				t.Fatal(err)
			}
			if got.Reviewer != tc.reviewer || len(got.Votes) != len(tc.votes) || strings.Join(got.Ranking, ",") != tc.ranking {
				t.Fatalf("cross-review = %+v", got)
			}
			for id, want := range tc.votes {
				if got.Votes[id] != want {
					t.Fatalf("vote %s = %t; want %t", id, got.Votes[id], want)
				}
			}
		})
	}
}

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
		{"extra text", "<<<COUNCIL\nCOUNCIL>>>\nVERDICT: APPROVE\nmore", true},
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

func TestParseCritiqueTrailingLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		trailing string
		verdict  Verdict
		valid    bool
	}{
		{"provider line", "VERDICT: REVISE\nprovider limit: seven_day allowed_warning", Revise, true},
		{"prose before and after", "Provider note\nVERDICT: APPROVE\nMore output", Approve, true},
		{"unrelated prefix", "VERDICT: APPROVE\nVERDICTORY provider note", Approve, true},
		{"two different verdicts", "VERDICT: APPROVE\nVERDICT: REVISE", "", false},
		{"repeated verdict", "VERDICT: APPROVE\nVERDICT: APPROVE", "", false},
		{"no verdict", "Provider note", "", false},
		{"invalid verdict", "VERDICT: MAYBE", "", false},
		{"second block", "VERDICT: APPROVE\n<<<COUNCIL\nCOUNCIL>>>\nVERDICT: REVISE", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCritique("<<<COUNCIL\nCOUNCIL>>>\n"+tc.trailing, "A")
			if (err == nil) != tc.valid {
				t.Fatalf("ParseCritique() = %+v, %v; valid = %t", got, err, tc.valid)
			}
			if tc.valid && got.Verdict != tc.verdict {
				t.Fatalf("verdict = %q; want %q", got.Verdict, tc.verdict)
			}
		})
	}
}

func TestParseCritiqueLeadingProse(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/codex-critique.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCritique(string(data), "C")
	if err != nil {
		t.Fatal(err)
	}
	wantTasks := []int{1, 1, 2, 3}
	wantSeverity := []Severity{Blocker, Major, Major, Blocker}
	if got.Label != "C" || got.Verdict != Revise || len(got.Findings) != len(wantTasks) {
		t.Fatalf("critique = %+v", got)
	}
	for i, finding := range got.Findings {
		if finding.Task != wantTasks[i] || finding.Severity != wantSeverity[i] {
			t.Fatalf("finding %d = %+v", i+1, finding)
		}
	}
}

func TestParseCritiqueStructure(t *testing.T) {
	t.Parallel()
	const finding = "{\"task\":1,\"severity\":\"minor\",\"claim\":\"x\",\"fix\":\"y\"}\n"
	tests := []struct {
		name   string
		output string
		valid  bool
	}{
		{"leading prose", "I will review the plan.\n<<<COUNCIL\n" + finding + "COUNCIL>>>\nVERDICT: REVISE", true},
		{"leading blank lines and prose", "\nNote\n\n<<<COUNCIL\nCOUNCIL>>>\nVERDICT: APPROVE", true},
		{"prose without block", "I will review the plan.\nVERDICT: APPROVE", false},
		{"second block after prose", "Note\n<<<COUNCIL\nCOUNCIL>>>\n<<<COUNCIL\nCOUNCIL>>>\nVERDICT: APPROVE", false},
		{"opening marker inside block", "Note\n<<<COUNCIL\n" + finding + "<<<COUNCIL\nCOUNCIL>>>\nVERDICT: APPROVE", false},
		{"prose inside block", "<<<COUNCIL\nnot json\n" + finding + "COUNCIL>>>\nVERDICT: REVISE", false},
		{"blank line inside block", "<<<COUNCIL\n\n" + finding + "COUNCIL>>>\nVERDICT: REVISE", false},
		{"prose before verdict", "<<<COUNCIL\nCOUNCIL>>>\nnote\nVERDICT: APPROVE", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCritique(tc.output, "A")
			if (err == nil) != tc.valid {
				t.Fatalf("ParseCritique() = %+v, %v; valid = %t", got, err, tc.valid)
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

func TestParseCrossReviewTolerant(t *testing.T) {
	t.Parallel()
	critiques := []Critique{
		{Label: "A", Findings: []Finding{{Task: 1}}},
		{Label: "B", Findings: []Finding{{Task: 2}}},
		{Label: "C"},
	}
	tests := []struct {
		name    string
		output  string
		valid   bool
		votes   map[string]bool
		ranking string
	}{
		{"prose and both ranking styles", "These findings need review.\n\nA1: AGREE\nA has a useful fix.\nB1: DISAGREE\n\nFINAL RANKING:\n1. Critique B\n2. A\n\nReading additional input from stdin...", true, map[string]bool{"A1": true, "B1": false}, "B,A"},
		{"unknown id", "A2: AGREE\nA1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. A", false, nil, ""},
		{"repeated vote", "A1: AGREE\nA1: DISAGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. A", false, nil, ""},
		{"missing vote", "A1: AGREE\nFINAL RANKING:\n1. B\n2. A", false, nil, ""},
		{"invalid vote", "A1: MAYBE\nA1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. A", false, nil, ""},
		{"malformed vote", "A1:AGREE\nA1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. A", false, nil, ""},
		{"vote without colon", "A1 AGREE\nA1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. A", false, nil, ""},
		{"unknown label", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. D\n2. A", false, nil, ""},
		{"repeated label", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\n2. Critique B", false, nil, ""},
		{"missing label", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B", false, nil, ""},
		{"malformed ranking", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. Critique B\n2. Critique  A", false, nil, ""},
		{"ranking ends at prose", "A1: AGREE\nB1: DISAGREE\nFINAL RANKING:\n1. B\nA note\n2. A", false, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCrossReview(tc.output, critiques, "C")
			if (err == nil) != tc.valid {
				t.Fatalf("ParseCrossReview() = %+v, %v; valid = %t", got, err, tc.valid)
			}
			if !tc.valid {
				return
			}
			if got.Reviewer != "C" || len(got.Votes) != len(tc.votes) || strings.Join(got.Ranking, ",") != tc.ranking {
				t.Fatalf("cross-review = %+v", got)
			}
			for id, want := range tc.votes {
				if got.Votes[id] != want {
					t.Fatalf("vote %s = %t; want %t", id, got.Votes[id], want)
				}
			}
		})
	}
}
