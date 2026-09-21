package loop

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/batuta-ai/core/gates"
)

// ClaimKind is the atomic kind of an executor claim.
type ClaimKind string

const (
	ClaimKindPath      ClaimKind = "path"
	ClaimKindCriterion ClaimKind = "criterion"
	ClaimKindTests     ClaimKind = "tests"
	ClaimKindCommit    ClaimKind = "commit"
)

// ClaimStatus is how code or the judge settled a claim.
type ClaimStatus string

const (
	ClaimStatusUnsettled    ClaimStatus = "unsettled"
	ClaimStatusSupported    ClaimStatus = "supported"
	ClaimStatusContradicted ClaimStatus = "contradicted"
)

// ClaimSource is who settled the claim.
type ClaimSource string

const (
	ClaimSourceCode  ClaimSource = "code"
	ClaimSourceJudge ClaimSource = "judge"
)

// Claim is one atomic claim the executor made in its report.
type Claim struct {
	Kind      ClaimKind
	Text      string
	Line      string
	Path      string
	Criterion int
	Status    ClaimStatus
	Source    ClaimSource
	Evidence  string
}

// ClaimEvidence is the mechanical evidence code can compare claims against.
type ClaimEvidence struct {
	ChangedPaths  []string
	TreeChanged   bool
	Proofs        []gates.Verdict
	VerifierLines map[int]string
	TestsPass     bool
}

var (
	claimBacktick      = regexp.MustCompile("`([^`]+)`")
	claimPathsTouched  = regexp.MustCompile(`(?i)^\s*paths\s+touched\s*:?\s*(.*)$`)
	claimListItem      = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)
	claimProgressDone  = regexp.MustCompile(`^BATUTA-PROGRESS\s+([0-9]+)\s+DONE$`)
	claimTaskDone      = regexp.MustCompile(`(?i)^\s*TASK\s+([0-9]+)\s*:\s*DONE\b`)
	claimCriterionDone = regexp.MustCompile(`(?i)criterion\s+([0-9]+)\s*[:.]?\s*(?:passed|done)\b`)
	claimDoneCriterion = regexp.MustCompile(`(?i)\b(?:passed|done)\s+criterion\s+([0-9]+)\b`)
	claimSuitePassed   = regexp.MustCompile(`(?i)\bsuite passed\b`)
	claimGoTestOKLine  = regexp.MustCompile(`(?i)^\s*ok\s+\S+`)
	claimGoTestPassed  = regexp.MustCompile(`(?i)\bgo test\b.*\b(ok|passed)\b`)
	claimCommitted     = regexp.MustCompile(`(?i)\bcommitted\b`)
	claimGitCommit     = regexp.MustCompile(`(?i)\bgit commit\b`)
	claimCommitHash    = regexp.MustCompile(`(?i)\bcommit\s+[0-9a-f]{7,40}\b`)
	claimVerifierLine  = regexp.MustCompile(`(?m)^\s*TASK\s+([0-9]+)\s*:\s*(DONE|INCOMPLETE)\b\s*(?:[—:-]+\s*(.*))?$`)
)

var knownPathExt = map[string]bool{
	".go": true, ".md": true, ".json": true, ".yml": true, ".yaml": true,
	".toml": true, ".txt": true, ".rs": true, ".py": true, ".js": true,
	".ts": true, ".tsx": true, ".jsx": true, ".css": true, ".html": true,
	".sh": true, ".mod": true, ".sum": true, ".proto": true, ".sql": true,
	".c": true, ".h": true, ".cpp": true, ".cc": true, ".hpp": true,
}

// ExtractClaims reads the bounded executor report line by line and returns
// the atomic claims the executor made. It never calls the judge.
func ExtractClaims(report string, criteria []gates.Criterion) []Claim {
	lines := boundClaimReportLines(report)
	var claims []Claim
	seenPath := map[string]bool{}
	collectingPaths := false
	for _, line := range lines {
		if strings.IndexByte(line, 0) >= 0 {
			collectingPaths = false
			continue
		}
		trimmed := strings.TrimSpace(line)
		if collectingPaths {
			if trimmed == "" {
				collectingPaths = false
			} else if item := listItemPath(trimmed); item != "" {
				claims = appendPathClaim(claims, seenPath, item, line)
				continue
			} else if rest, ok := pathsTouchedRest(trimmed); ok {
				claims = appendPathTokens(claims, seenPath, rest, line)
				collectingPaths = rest == ""
				continue
			} else {
				collectingPaths = false
			}
		}
		if rest, ok := pathsTouchedRest(trimmed); ok {
			claims = appendPathTokens(claims, seenPath, rest, line)
			collectingPaths = rest == ""
		}
		for _, raw := range claimBacktick.FindAllStringSubmatch(line, -1) {
			if path, ok := pathToken(raw[1]); ok {
				claims = appendPathClaim(claims, seenPath, path, line)
			}
		}
		claims = append(claims, criterionClaims(trimmed, line, criteria)...)
		if testClaim, ok := testsClaim(trimmed, line); ok {
			claims = append(claims, testClaim)
		}
		if commitClaim, ok := commitClaim(trimmed, line); ok {
			claims = append(claims, commitClaim)
		}
	}
	return claims
}

func boundClaimReportLines(report string) []string {
	lines := splitReportLines(report)
	if len(lines) > claimEvidenceReportLines {
		lines = lines[len(lines)-claimEvidenceReportLines:]
	}
	return lines
}

func pathsTouchedRest(trimmed string) (string, bool) {
	match := claimPathsTouched.FindStringSubmatch(trimmed)
	if match == nil {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func listItemPath(trimmed string) string {
	match := claimListItem.FindStringSubmatch(trimmed)
	if match == nil {
		return ""
	}
	path, ok := pathToken(match[1])
	if !ok {
		return ""
	}
	return path
}

func appendPathTokens(claims []Claim, seen map[string]bool, rest, line string) []Claim {
	if rest == "" {
		return claims
	}
	for _, raw := range strings.Split(rest, ",") {
		raw = strings.TrimSpace(raw)
		raw = strings.TrimPrefix(raw, "and ")
		if path, ok := pathToken(raw); ok {
			claims = appendPathClaim(claims, seen, path, line)
		}
	}
	return claims
}

func appendPathClaim(claims []Claim, seen map[string]bool, path, line string) []Claim {
	if seen[path] {
		return claims
	}
	seen[path] = true
	return append(claims, Claim{
		Kind:   ClaimKindPath,
		Text:   path,
		Line:   line,
		Path:   path,
		Status: ClaimStatusUnsettled,
	})
}

func pathToken(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "`'\"")
	s = strings.TrimRight(s, ".,;:)")
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t") {
		return "", false
	}
	slash := filepath.ToSlash(s)
	if strings.Contains(slash, "://") {
		return "", false
	}
	if strings.HasPrefix(slash, "/") || strings.HasPrefix(s, `\`) {
		return "", false
	}
	if len(s) >= 2 && s[1] == ':' {
		return "", false
	}
	slash = strings.TrimPrefix(slash, "./")
	if slash == "" || slash == "." || slash == ".." {
		return "", false
	}
	if !strings.Contains(slash, "/") && !knownPathExt[strings.ToLower(filepath.Ext(slash))] {
		return "", false
	}
	return slash, true
}

func criterionClaims(trimmed, line string, criteria []gates.Criterion) []Claim {
	var claims []Claim
	if match := claimProgressDone.FindStringSubmatch(trimmed); match != nil {
		claims = append(claims, criterionClaim(match[1], line, criteria))
	}
	if match := claimTaskDone.FindStringSubmatch(trimmed); match != nil {
		claims = append(claims, criterionClaim(match[1], line, criteria))
	}
	seen := map[int]bool{}
	for _, match := range claimCriterionDone.FindAllStringSubmatch(trimmed, -1) {
		n, _ := strconv.Atoi(match[1])
		if n < 1 || seen[n] {
			continue
		}
		seen[n] = true
		claims = append(claims, criterionClaim(match[1], line, criteria))
	}
	for _, match := range claimDoneCriterion.FindAllStringSubmatch(trimmed, -1) {
		n, _ := strconv.Atoi(match[1])
		if n < 1 || seen[n] {
			continue
		}
		seen[n] = true
		claims = append(claims, criterionClaim(match[1], line, criteria))
	}
	var kept []Claim
	for _, claim := range claims {
		if claim.Criterion > 0 {
			kept = append(kept, claim)
		}
	}
	return kept
}

func criterionClaim(number, line string, criteria []gates.Criterion) Claim {
	n, _ := strconv.Atoi(number)
	if n < 1 {
		return Claim{}
	}
	text := line
	if n <= len(criteria) {
		text = criteria[n-1].Text
	}
	return Claim{
		Kind:      ClaimKindCriterion,
		Text:      text,
		Line:      line,
		Criterion: n,
		Status:    ClaimStatusUnsettled,
	}
}

func testsClaim(trimmed, line string) (Claim, bool) {
	if !claimSuitePassed.MatchString(trimmed) && !claimGoTestOKLine.MatchString(trimmed) && !claimGoTestPassed.MatchString(trimmed) {
		return Claim{}, false
	}
	return Claim{
		Kind:   ClaimKindTests,
		Text:   trimmed,
		Line:   line,
		Status: ClaimStatusUnsettled,
	}, true
}

func commitClaim(trimmed, line string) (Claim, bool) {
	if !claimCommitted.MatchString(trimmed) && !claimGitCommit.MatchString(trimmed) && !claimCommitHash.MatchString(trimmed) {
		return Claim{}, false
	}
	return Claim{
		Kind:   ClaimKindCommit,
		Text:   trimmed,
		Line:   line,
		Status: ClaimStatusUnsettled,
	}, true
}

// SettleClaims marks claims that code can settle exactly. Unsettled claims
// are left for the judge. It never calls the judge.
func SettleClaims(claims []Claim, ev ClaimEvidence) []Claim {
	out := make([]Claim, len(claims))
	copy(out, claims)
	for i := range out {
		switch out[i].Kind {
		case ClaimKindPath:
			out[i] = settlePathClaim(out[i], ev)
		case ClaimKindCriterion:
			out[i] = settleCriterionClaim(out[i], ev)
		case ClaimKindTests:
			out[i] = settleTestsClaim(out[i], ev)
		}
	}
	return out
}

func settlePathClaim(claim Claim, ev ClaimEvidence) Claim {
	if claim.Path == "" {
		return claim
	}
	if changedPathPresent(claim.Path, ev.ChangedPaths) {
		claim.Status = ClaimStatusSupported
		claim.Source = ClaimSourceCode
		claim.Evidence = "in changed_paths"
		return claim
	}
	claim.Status = ClaimStatusContradicted
	claim.Source = ClaimSourceCode
	if !ev.TreeChanged {
		claim.Evidence = "not in changed_paths; tree unchanged"
	} else {
		claim.Evidence = "not in changed_paths"
	}
	return claim
}

func settleCriterionClaim(claim Claim, ev ClaimEvidence) Claim {
	idx := claim.Criterion - 1
	var proof *gates.Verdict
	if idx >= 0 && idx < len(ev.Proofs) {
		item := ev.Proofs[idx]
		proof = &item
	}
	line, hasVerifier := ev.VerifierLines[claim.Criterion]
	if proof == nil && !hasVerifier {
		return claim
	}
	if proof != nil && !proof.Pass {
		claim.Status = ClaimStatusContradicted
		claim.Source = ClaimSourceCode
		claim.Evidence = "proof failed"
		if hasVerifier && verifierIncomplete(line) {
			claim.Evidence = "proof failed; verifier INCOMPLETE"
		}
		return claim
	}
	if hasVerifier && verifierIncomplete(line) {
		claim.Status = ClaimStatusContradicted
		claim.Source = ClaimSourceCode
		claim.Evidence = "verifier INCOMPLETE"
		return claim
	}
	if proof != nil && proof.Pass && hasVerifier && verifierDone(line) {
		claim.Status = ClaimStatusSupported
		claim.Source = ClaimSourceCode
		claim.Evidence = "proof passed; verifier DONE"
		return claim
	}
	return claim
}

func settleTestsClaim(claim Claim, ev ClaimEvidence) Claim {
	claim.Source = ClaimSourceCode
	if ev.TestsPass {
		claim.Status = ClaimStatusSupported
		claim.Evidence = "tests gate passed"
		return claim
	}
	claim.Status = ClaimStatusContradicted
	claim.Evidence = "tests gate failed"
	return claim
}

func verifierIncomplete(line string) bool {
	return strings.Contains(strings.ToUpper(line), "INCOMPLETE")
}

func verifierDone(line string) bool {
	upper := strings.ToUpper(line)
	return strings.Contains(upper, "DONE") && !strings.Contains(upper, "INCOMPLETE")
}

func changedPathPresent(path string, changed []string) bool {
	want := filepath.ToSlash(strings.TrimPrefix(path, "./"))
	for _, candidate := range changed {
		got := filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(candidate), "./"))
		if got == want {
			return true
		}
	}
	return false
}

// ParseVerifierLines maps criterion numbers to DONE|INCOMPLETE using the
// same TASK n: regex gates uses.
func ParseVerifierLines(detail string) map[int]string {
	out := map[int]string{}
	for _, match := range claimVerifierLine.FindAllStringSubmatch(detail, -1) {
		n, _ := strconv.Atoi(match[1])
		if n < 1 {
			continue
		}
		out[n] = match[2]
	}
	return out
}
