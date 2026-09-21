package loop

import (
	"strings"
	"testing"

	"github.com/batuta-ai/core/gates"
)

func TestExtractClaims(t *testing.T) {
	t.Parallel()

	criteria := []gates.Criterion{
		{Text: "finds path claims", Proof: "go test ./loop -run TestExtractClaims"},
		{Text: "finds criterion claims", Proof: "go test ./loop -run TestExtractClaims"},
	}
	pathsTouched := "Paths touched: loop/claims.go, loop/claims_test.go"
	backticked := "edited `cmd/batuta/judge.go` and ignored `https://example.com/x.go` and `/usr/bin/go` and `README.md`"
	progressDone := "BATUTA-PROGRESS 1 DONE"
	taskDone := "TASK 1: DONE"
	criterionPassed := "criterion 2 passed"
	suitePassed := "the suite passed"
	goTestOK := "ok  \tgithub.com/batuta-ai/core/loop\t0.12s"
	goTestPassed := "go test ./... passed"
	committed := "committed 1a2b3c4 on the feature branch"
	report := strings.Join([]string{
		pathsTouched,
		backticked,
		"BATUTA-PROGRESS 1 START",
		progressDone,
		taskDone,
		criterionPassed,
		suitePassed,
		goTestOK,
		goTestPassed,
		committed,
		"left uncommitted notes in the log",
	}, "\n")

	claims := ExtractClaims(report, criteria)

	wantPaths := []string{"loop/claims.go", "loop/claims_test.go", "cmd/batuta/judge.go", "README.md"}
	var gotPaths []string
	for _, claim := range claims {
		if claim.Kind != ClaimKindPath {
			continue
		}
		gotPaths = append(gotPaths, claim.Path)
		if claim.Line == "" {
			t.Errorf("path %q has empty Line", claim.Path)
		}
		if claim.Status != ClaimStatusUnsettled {
			t.Errorf("path %q status = %q, want unsettled", claim.Path, claim.Status)
		}
	}
	if strings.Join(gotPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("path claims = %v, want %v", gotPaths, wantPaths)
	}

	if claim, ok := pathClaim(claims, "loop/claims.go"); !ok || claim.Line != pathsTouched {
		t.Fatalf("loop/claims.go line = %q, want %q", claim.Line, pathsTouched)
	}
	if claim, ok := pathClaim(claims, "cmd/batuta/judge.go"); !ok || claim.Line != backticked {
		t.Fatalf("cmd/batuta/judge.go line = %q, want %q", claim.Line, backticked)
	}
	if _, ok := pathClaim(claims, "https://example.com/x.go"); ok {
		t.Fatal("extracted a URL as a path claim")
	}
	if _, ok := pathClaim(claims, "/usr/bin/go"); ok {
		t.Fatal("extracted an absolute path")
	}

	var criterionClaims []Claim
	for _, claim := range claims {
		if claim.Kind == ClaimKindCriterion {
			criterionClaims = append(criterionClaims, claim)
		}
	}
	if len(criterionClaims) != 3 {
		t.Fatalf("criterion claims = %#v, want 3", criterionClaims)
	}
	if criterionClaims[0].Criterion != 1 || criterionClaims[0].Line != progressDone || criterionClaims[0].Text != criteria[0].Text {
		t.Fatalf("progress claim = %#v", criterionClaims[0])
	}
	if criterionClaims[1].Criterion != 1 || criterionClaims[1].Line != taskDone {
		t.Fatalf("task claim = %#v", criterionClaims[1])
	}
	if criterionClaims[2].Criterion != 2 || criterionClaims[2].Line != criterionPassed || criterionClaims[2].Text != criteria[1].Text {
		t.Fatalf("criterion-passed claim = %#v", criterionClaims[2])
	}

	var testClaims []Claim
	for _, claim := range claims {
		if claim.Kind == ClaimKindTests {
			testClaims = append(testClaims, claim)
		}
	}
	if len(testClaims) != 3 {
		t.Fatalf("test claims = %#v, want 3", testClaims)
	}
	if testClaims[0].Line != suitePassed || testClaims[1].Line != goTestOK || testClaims[2].Line != goTestPassed {
		t.Fatalf("test claim lines = %q, %q, %q", testClaims[0].Line, testClaims[1].Line, testClaims[2].Line)
	}

	var commitClaims []Claim
	for _, claim := range claims {
		if claim.Kind == ClaimKindCommit {
			commitClaims = append(commitClaims, claim)
		}
	}
	if len(commitClaims) != 1 || commitClaims[0].Line != committed {
		t.Fatalf("commit claims = %#v", commitClaims)
	}

	dup := ExtractClaims("Paths touched: loop/claims.go\nlater `loop/claims.go` again\n", nil)
	if paths := pathClaims(dup); len(paths) != 1 || paths[0].Path != "loop/claims.go" || paths[0].Line != "Paths touched: loop/claims.go" {
		t.Fatalf("deduped path claims = %#v", paths)
	}
}

func pathClaim(claims []Claim, path string) (Claim, bool) {
	for _, claim := range claims {
		if claim.Kind == ClaimKindPath && claim.Path == path {
			return claim, true
		}
	}
	return Claim{}, false
}

func TestSettleClaimsPaths(t *testing.T) {
	t.Parallel()

	present := Claim{Kind: ClaimKindPath, Text: "loop/claims.go", Line: "edited `loop/claims.go`", Path: "loop/claims.go", Status: ClaimStatusUnsettled}
	absent := Claim{Kind: ClaimKindPath, Text: "loop/missing.go", Line: "edited `loop/missing.go`", Path: "loop/missing.go", Status: ClaimStatusUnsettled}
	prose := Claim{Kind: ClaimKindCommit, Text: "the routing now matches the brief", Line: "the routing now matches the brief", Status: ClaimStatusUnsettled}

	supported := SettleClaims([]Claim{present}, ClaimEvidence{
		ChangedPaths: []string{"loop/claims.go", "loop/claims_test.go"},
		TreeChanged:  true,
	})
	if len(supported) != 1 || supported[0].Status != ClaimStatusSupported || supported[0].Source != ClaimSourceCode {
		t.Fatalf("present path = %#v", supported)
	}

	unchanged := SettleClaims([]Claim{absent}, ClaimEvidence{
		ChangedPaths: nil,
		TreeChanged:  false,
	})
	if len(unchanged) != 1 || unchanged[0].Status != ClaimStatusContradicted || unchanged[0].Source != ClaimSourceCode {
		t.Fatalf("unchanged tree = %#v", unchanged)
	}

	missing := SettleClaims([]Claim{absent}, ClaimEvidence{
		ChangedPaths: []string{"docs/loop.md"},
		TreeChanged:  true,
	})
	if len(missing) != 1 || missing[0].Status != ClaimStatusContradicted || missing[0].Source != ClaimSourceCode {
		t.Fatalf("absent path = %#v", missing)
	}

	got := SettleClaims([]Claim{present, absent, prose}, ClaimEvidence{
		ChangedPaths: []string{"loop/claims.go"},
		TreeChanged:  true,
	})
	if len(got) != 3 {
		t.Fatalf("settled %d claims, want 3", len(got))
	}
	if got[0].Status != ClaimStatusSupported || got[1].Status != ClaimStatusContradicted || got[2].Status != ClaimStatusUnsettled {
		t.Fatalf("mixed settlement = %#v", got)
	}
	if got[2].Source != "" {
		t.Fatalf("prose claim was settled by %q", got[2].Source)
	}
}

func TestSettleClaimsCriteria(t *testing.T) {
	t.Parallel()

	claim := Claim{Kind: ClaimKindCriterion, Text: "settles criteria", Line: "BATUTA-PROGRESS 1 DONE", Criterion: 1, Status: ClaimStatusUnsettled}
	pass := gates.Verdict{Name: "proof 1", Pass: true}
	fail := gates.Verdict{Name: "proof 1", Pass: false}

	failed := SettleClaims([]Claim{claim}, ClaimEvidence{
		Proofs:        []gates.Verdict{fail},
		VerifierLines: ParseVerifierLines("TASK 1: DONE"),
	})
	if len(failed) != 1 || failed[0].Status != ClaimStatusContradicted || failed[0].Source != ClaimSourceCode {
		t.Fatalf("failed proof = %#v", failed)
	}

	incomplete := SettleClaims([]Claim{claim}, ClaimEvidence{
		Proofs:        []gates.Verdict{pass},
		VerifierLines: ParseVerifierLines("TASK 1: INCOMPLETE — missing tests"),
	})
	if len(incomplete) != 1 || incomplete[0].Status != ClaimStatusContradicted || incomplete[0].Source != ClaimSourceCode {
		t.Fatalf("incomplete verifier = %#v", incomplete)
	}

	supported := SettleClaims([]Claim{claim}, ClaimEvidence{
		Proofs:        []gates.Verdict{pass},
		VerifierLines: ParseVerifierLines("TASK 1: DONE"),
	})
	if len(supported) != 1 || supported[0].Status != ClaimStatusSupported || supported[0].Source != ClaimSourceCode {
		t.Fatalf("both pass = %#v", supported)
	}

	unsettled := SettleClaims([]Claim{claim}, ClaimEvidence{})
	if len(unsettled) != 1 || unsettled[0].Status != ClaimStatusUnsettled || unsettled[0].Source != "" {
		t.Fatalf("no proof and no verifier = %#v", unsettled)
	}
}

func TestSettleClaimsTests(t *testing.T) {
	t.Parallel()

	claim := Claim{Kind: ClaimKindTests, Text: "the suite passed", Line: "the suite passed", Status: ClaimStatusUnsettled}
	failed := SettleClaims([]Claim{claim}, ClaimEvidence{TestsPass: false})
	if len(failed) != 1 || failed[0].Status != ClaimStatusContradicted || failed[0].Source != ClaimSourceCode {
		t.Fatalf("tests gate failed = %#v", failed)
	}

	passed := SettleClaims([]Claim{claim}, ClaimEvidence{TestsPass: true})
	if len(passed) != 1 || passed[0].Status != ClaimStatusSupported || passed[0].Source != ClaimSourceCode {
		t.Fatalf("tests gate passed = %#v", passed)
	}
}

func pathClaims(claims []Claim) []Claim {
	var out []Claim
	for _, claim := range claims {
		if claim.Kind == ClaimKindPath {
			out = append(out, claim)
		}
	}
	return out
}

func TestExtractClaimsBounds(t *testing.T) {
	t.Parallel()

	empty := ExtractClaims("", nil)
	if len(empty) != 0 {
		t.Fatalf("empty report = %#v", empty)
	}

	binary := ExtractClaims("\x00\xff`loop/secret.go`\nthe suite passed\n", nil)
	if _, ok := pathClaim(binary, "loop/secret.go"); ok {
		t.Fatal("extracted a path from a binary line")
	}

	var prefixed strings.Builder
	prefixed.WriteString("Paths touched: stale/old.go\n")
	for range claimEvidenceReportLines {
		prefixed.WriteString("noise line\n")
	}
	prefixed.WriteString("Paths touched: loop/claims.go\n")
	bounded := ExtractClaims(prefixed.String(), nil)
	if _, ok := pathClaim(bounded, "stale/old.go"); ok {
		t.Fatal("extracted a path from a line outside the report limit")
	}
	if _, ok := pathClaim(bounded, "loop/claims.go"); !ok {
		t.Fatalf("missing path from the kept tail: %#v", bounded)
	}

	huge := strings.Repeat("noise line\n", 100000) + "the suite passed\n"
	got := ExtractClaims(huge, nil)
	if len(got) != 1 || got[0].Kind != ClaimKindTests {
		t.Fatalf("huge report = %#v", got)
	}
}
