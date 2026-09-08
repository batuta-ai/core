package review

import "testing"

func TestVerdictRules(t *testing.T) {
	tests := []struct {
		name     string
		findings []Finding
		spec     []Criterion
		want     Decision
	}{
		{name: "empty", want: Ship},
		{name: "polish", findings: []Finding{testFinding(Minor), testFinding(Nit)}, want: Ship},
		{name: "major", findings: []Finding{testFinding(Minor), testFinding(Major)}, want: FixBeforeShip},
		{name: "blocker", findings: []Finding{testFinding(Major), testFinding(Blocker)}, want: Rework},
		{name: "blocker first", findings: []Finding{testFinding(Blocker), testFinding(Major)}, want: Rework},
		{name: "violated criterion", spec: []Criterion{{ID: "1", Violated: true}}, want: Rework},
		{name: "criterion overrides major", findings: []Finding{testFinding(Major)}, spec: []Criterion{{ID: "1", Violated: true}}, want: Rework},
		{name: "satisfied criteria", spec: []Criterion{{ID: "1"}}, want: Ship},
		{name: "advisory major", findings: []Finding{{Severity: Major, Kind: Advisory}}, want: FixBeforeShip},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verdict(tt.findings, tt.spec); got != tt.want {
				t.Fatalf("Verdict = %s, want %s", got, tt.want)
			}
		})
	}
	if Ship != "SHIP" || FixBeforeShip != "FIX_BEFORE_SHIP" || Rework != "REWORK" {
		t.Fatal("verdict wire values drifted")
	}
}
