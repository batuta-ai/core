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

	claims := ExtractClaims(report, criteria, anyKnownPath)

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

	dup := ExtractClaims("Paths touched: loop/claims.go\nlater `loop/claims.go` again\n", nil, anyKnownPath)
	if paths := pathClaims(dup); len(paths) != 1 || paths[0].Path != "loop/claims.go" || paths[0].Line != "Paths touched: loop/claims.go" {
		t.Fatalf("deduped path claims = %#v", paths)
	}
}

func TestExtractClaimsEditStatements(t *testing.T) {
	t.Parallel()

	t.Run("paths touched list items", func(t *testing.T) {
		t.Parallel()
		report := strings.Join([]string{
			"Paths touched:",
			"- loop/claims.go",
			"- `loop/claims_test.go`",
			"",
			"mentioned `loop/secret.go` later",
		}, "\n")
		got := pathClaimPaths(ExtractClaims(report, nil, anyKnownPath))
		want := []string{"loop/claims.go", "loop/claims_test.go"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("path claims = %v, want %v", got, want)
		}
	})

	t.Run("files changed list items stop at the next heading", func(t *testing.T) {
		t.Parallel()
		report := strings.Join([]string{
			"Files changed:",
			"- cmd/batuta/judge.go",
			"# Next section",
			"- loop/secret.go",
		}, "\n")
		got := pathClaimPaths(ExtractClaims(report, nil, anyKnownPath))
		if strings.Join(got, ",") != "cmd/batuta/judge.go" {
			t.Fatalf("path claims = %v, want [cmd/batuta/judge.go]", got)
		}
	})

	t.Run("files modified and paths edited headings", func(t *testing.T) {
		t.Parallel()
		report := strings.Join([]string{
			"Files modified: loop/judgment.go",
			"Paths edited:",
			"* cmd/batuta/judge_test.go",
		}, "\n")
		got := pathClaimPaths(ExtractClaims(report, nil, anyKnownPath))
		want := []string{"loop/judgment.go", "cmd/batuta/judge_test.go"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("path claims = %v, want %v", got, want)
		}
	})

	t.Run("edit verb sentences", func(t *testing.T) {
		t.Parallel()
		verbs := []string{
			"created", "added", "edited", "modified", "updated",
			"rewrote", "wrote", "removed", "deleted", "renamed", "moved",
		}
		var lines []string
		var want []string
		for _, verb := range verbs {
			path := "loop/" + verb + ".go"
			lines = append(lines, verb+" `"+path+"`")
			want = append(want, path)
		}
		lines = append(lines, "also created loop/bare.go")
		want = append(want, "loop/bare.go")
		got := pathClaimPaths(ExtractClaims(strings.Join(lines, "\n"), nil, anyKnownPath))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("path claims = %v, want %v", got, want)
		}
	})
}

func TestExtractClaimsRejectsMentions(t *testing.T) {
	t.Parallel()

	report := strings.Join([]string{
		"Paths touched: encoding/json, github.com/batuta-ai/core/judge, typesafe/jev-1.13, batuta/judge-package/task-1-e1, net/http, os/exec",
		"edited golang.org/x/mod and refs/heads/main and https://example.com/x.go and `.go` and `.md`",
		"I edited encoding/json",
		"I read loop/claims.go",
		"I referenced loop/claims_test.go",
		"I edited README.md which is frozen",
		"I updated cmd/batuta/judge.go and left it unchanged",
		"I edited `.batuta/judge.json` which is out of scope",
		"I created `.batuta/judge.json` fora do escopo",
		"I added docs/missing.md for example",
		"I modified the example `docs/missing.md`",
	}, "\n")
	got := pathClaimPaths(ExtractClaims(report, nil, anyKnownPath))
	if len(got) != 0 {
		t.Fatalf("path claims = %v, want none", got)
	}
}

func TestExtractClaimsPathPrecision(t *testing.T) {
	t.Parallel()

	tree := []string{"loop/claims.go", "cmd/batuta/judge.go", "README.md"}
	changed := []string{"loop/claims.go", "loop/claims_test.go"}
	scope := []string{"docs/**", "out/1.txt"}
	known := knownClaimPath(tree, changed, scope)

	report := strings.Join([]string{
		"Paths touched: loop/claims.go, encoding/json, github.com/batuta-ai/core/judge, typesafe/jev-1.13",
		"edited `cmd/batuta/judge.go` and `feat/judge-claims` and `https://example.com/x.go`",
		"updated `docs/judge.md` and `out/1.txt` and `README.md` and `loop/claims_test.go`",
		"created `mystery.go`",
	}, "\n")

	got := pathClaimPaths(ExtractClaims(report, nil, known))
	want := []string{"loop/claims.go", "cmd/batuta/judge.go", "docs/judge.md", "out/1.txt", "README.md", "loop/claims_test.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("path claims = %v, want %v", got, want)
	}
}

func TestExtractClaimsBenchmarkFalsePositives(t *testing.T) {
	t.Parallel()

	// Lines quoted from the Version 2.1 replay false positives in
	// .batuta/judge-benchmark.md: tokens the extractor accepted although
	// the report only mentioned them.
	cases := []struct {
		name string
		line string
	}{
		{"encoding/json", "The HTTP tests marshal fixtures with `encoding/json`."},
		{"github.com/batuta-ai/core/judge", "Package `github.com/batuta-ai/core/judge` is the decision client."},
		{"typesafe/jev-1.13", "Judge: provider auto → TypeSafe direct (`typesafe/jev-1.13`)."},
		{"batuta/judge-package/task-1-e1", "Worktree branch `batuta/judge-package/task-1-e1`."},
		{"fora do escopo", "`.batuta/judge.json` is fora do escopo."},
		{"bare extensions", "Touched source `.go` and docs `.md`."},
		{"docs/missing.md", "for example `docs/missing.md` is named only as an example."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := pathClaimPaths(ExtractClaims(tc.line, nil, anyKnownPath))
			if len(got) != 0 {
				t.Fatalf("line %q produced path claims %#v, want none", tc.line, got)
			}
		})
	}
}

func TestExtractClaimsCriterionIndex(t *testing.T) {
	t.Parallel()

	criteria := []gates.Criterion{
		{Text: "path claims are precise"},
		{Text: "criterion claims carry their index"},
		{Text: "evidence is attached"},
	}
	report := strings.Join([]string{
		"BATUTA-PROGRESS 1 DONE",
		"TASK 2: DONE",
		"BATUTA-PROGRESS 3 DONE",
		"TASK 4: DONE",
		"BATUTA-PROGRESS 0 DONE",
		"criterion 2 passed",
	}, "\n")

	var got []Claim
	for _, claim := range ExtractClaims(report, criteria, anyKnownPath) {
		if claim.Kind == ClaimKindCriterion {
			got = append(got, claim)
		}
	}
	if len(got) != 4 {
		t.Fatalf("criterion claims = %#v, want 4 in-range claims", got)
	}
	if got[0].Criterion != 1 || got[0].Text != criteria[0].Text || got[0].Line != "BATUTA-PROGRESS 1 DONE" {
		t.Fatalf("progress claim = %#v", got[0])
	}
	if got[1].Criterion != 2 || got[1].Text != criteria[1].Text || got[1].Line != "TASK 2: DONE" {
		t.Fatalf("task claim = %#v", got[1])
	}
	if got[2].Criterion != 3 || got[2].Text != criteria[2].Text || got[2].Line != "BATUTA-PROGRESS 3 DONE" {
		t.Fatalf("progress-3 claim = %#v", got[2])
	}
	if got[3].Criterion != 2 || got[3].Text != criteria[1].Text || got[3].Line != "criterion 2 passed" {
		t.Fatalf("criterion-passed claim = %#v", got[3])
	}
}

func anyKnownPath(string) bool { return true }

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
	pass := gates.Verdict{Name: "proof 1", Pass: true, Signal: "settles criteria — `go test ./loop -run TestSettleClaimsCriteria` passed"}
	fail := gates.Verdict{Name: "proof 1", Pass: false, Signal: "settles criteria — `go test ./loop -run TestSettleClaimsCriteria` failed"}
	done := ParseVerifierLines("TASK 1: DONE")
	incompleteLine := ParseVerifierLines("TASK 1: INCOMPLETE — missing tests")

	failed := SettleClaims([]Claim{claim}, ClaimEvidence{
		Proofs:        []gates.Verdict{fail},
		VerifierLines: done,
	})
	if len(failed) != 1 || failed[0].Status != ClaimStatusContradicted || failed[0].Source != ClaimSourceCode {
		t.Fatalf("failed proof = %#v", failed)
	}
	if failed[0].Evidence != "proof: "+fail.Signal+"; verifier: TASK 1: DONE" {
		t.Fatalf("failed proof evidence = %q", failed[0].Evidence)
	}

	incomplete := SettleClaims([]Claim{claim}, ClaimEvidence{
		Proofs:        []gates.Verdict{pass},
		VerifierLines: incompleteLine,
	})
	if len(incomplete) != 1 || incomplete[0].Status != ClaimStatusContradicted || incomplete[0].Source != ClaimSourceCode {
		t.Fatalf("incomplete verifier = %#v", incomplete)
	}
	if incomplete[0].Evidence != "proof: "+pass.Signal+"; verifier: TASK 1: INCOMPLETE — missing tests" {
		t.Fatalf("incomplete verifier evidence = %q", incomplete[0].Evidence)
	}

	supported := SettleClaims([]Claim{claim}, ClaimEvidence{
		Proofs:        []gates.Verdict{pass},
		VerifierLines: done,
	})
	if len(supported) != 1 || supported[0].Status != ClaimStatusSupported || supported[0].Source != ClaimSourceCode {
		t.Fatalf("both pass = %#v", supported)
	}
	if supported[0].Evidence != "proof: "+pass.Signal+"; verifier: TASK 1: DONE" {
		t.Fatalf("supported evidence = %q", supported[0].Evidence)
	}

	unsettled := SettleClaims([]Claim{claim}, ClaimEvidence{})
	if len(unsettled) != 1 || unsettled[0].Status != ClaimStatusUnsettled || unsettled[0].Source != "" {
		t.Fatalf("no proof and no verifier = %#v", unsettled)
	}
	if unsettled[0].Evidence != "proof: ; verifier: " {
		t.Fatalf("neither-exists evidence = %q", unsettled[0].Evidence)
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

func pathClaimPaths(claims []Claim) []string {
	var out []string
	for _, claim := range pathClaims(claims) {
		out = append(out, claim.Path)
	}
	return out
}

func TestExtractClaimsBounds(t *testing.T) {
	t.Parallel()

	empty := ExtractClaims("", nil, anyKnownPath)
	if len(empty) != 0 {
		t.Fatalf("empty report = %#v", empty)
	}

	binary := ExtractClaims("\x00\xff`loop/secret.go`\nthe suite passed\n", nil, anyKnownPath)
	if _, ok := pathClaim(binary, "loop/secret.go"); ok {
		t.Fatal("extracted a path from a binary line")
	}

	var prefixed strings.Builder
	prefixed.WriteString("Paths touched: stale/old.go\n")
	for range claimEvidenceReportLines {
		prefixed.WriteString("noise line\n")
	}
	prefixed.WriteString("Paths touched: loop/claims.go\n")
	bounded := ExtractClaims(prefixed.String(), nil, anyKnownPath)
	if _, ok := pathClaim(bounded, "stale/old.go"); ok {
		t.Fatal("extracted a path from a line outside the report limit")
	}
	if _, ok := pathClaim(bounded, "loop/claims.go"); !ok {
		t.Fatalf("missing path from the kept tail: %#v", bounded)
	}

	huge := strings.Repeat("noise line\n", 100000) + "the suite passed\n"
	got := ExtractClaims(huge, nil, anyKnownPath)
	if len(got) != 1 || got[0].Kind != ClaimKindTests {
		t.Fatalf("huge report = %#v", got)
	}
}
