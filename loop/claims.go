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
	ClaimStatusUnverifiable ClaimStatus = "unverifiable"
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
	claimBacktick       = regexp.MustCompile("`([^`]+)`")
	claimFilesHeading   = regexp.MustCompile(`(?i)^(?:#{1,6}\s+)?(?:paths|files)\s+(?:touched|changed|modified|edited)\b\s*:?\s*(.*)$`)
	claimListItem       = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)
	claimEditVerb       = regexp.MustCompile(`(?i)\b(?:created|added|edited|modified|updated|rewrote|wrote|removed|deleted|renamed|moved)\b`)
	claimPathDisqualify = regexp.MustCompile(`(?i)(?:\b(?:read|referenced|frozen|unchanged)\b|out of scope|fora do escopo|for example)`)
	claimProgressDone   = regexp.MustCompile(`^BATUTA-PROGRESS\s+([0-9]+)\s+DONE$`)
	claimTaskDone       = regexp.MustCompile(`(?i)^\s*TASK\s+([0-9]+)\s*:\s*DONE\b`)
	claimCriterionDone  = regexp.MustCompile(`(?i)criterion\s+([0-9]+)\s*[:.]?\s*(?:passed|done)\b`)
	claimDoneCriterion  = regexp.MustCompile(`(?i)\b(?:passed|done)\s+criterion\s+([0-9]+)\b`)
	claimSuitePassed    = regexp.MustCompile(`(?i)\bsuite passed\b`)
	claimGoTestOKLine   = regexp.MustCompile(`(?i)^\s*ok\s+\S+`)
	claimGoTestPassed   = regexp.MustCompile(`(?i)\bgo test\b.*\b(ok|passed)\b`)
	claimCommitted      = regexp.MustCompile(`(?i)\bcommitted\b`)
	claimGitCommit      = regexp.MustCompile(`(?i)\bgit commit\b`)
	claimCommitHash     = regexp.MustCompile(`(?i)\bcommit\s+[0-9a-f]{7,40}\b`)
	claimVerifierLine   = regexp.MustCompile(`(?m)^\s*TASK\s+([0-9]+)\s*:\s*(DONE|INCOMPLETE)\b\s*(?:[—:-]+\s*(.*))?$`)
)

var pathTokenPrefixes = []string{
	"http", "github.com", "golang.org", "encoding/", "net/", "os/", "refs/", "batuta/",
}

var knownPathExt = map[string]bool{
	".go": true, ".md": true, ".json": true, ".yml": true, ".yaml": true,
	".toml": true, ".txt": true, ".rs": true, ".py": true, ".js": true,
	".ts": true, ".tsx": true, ".jsx": true, ".css": true, ".html": true,
	".sh": true, ".mod": true, ".sum": true, ".proto": true, ".sql": true,
	".c": true, ".h": true, ".cpp": true, ".cc": true, ".hpp": true,
}

// ExtractClaims reads the bounded executor report line by line and returns
// the atomic claims the executor made. A path token is kept only when known
// returns true. It never calls the judge.
func ExtractClaims(report string, criteria []gates.Criterion, known ...func(path string) bool) []Claim {
	var check func(string) bool
	if len(known) > 0 {
		check = known[0]
	}
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
			if trimmed == "" || isSectionHeading(trimmed) {
				collectingPaths = false
			} else if sentenceDisqualified(trimmed) {
				continue
			} else if item := listItemPath(trimmed); item != "" {
				claims = appendPathClaim(claims, seenPath, item, line, check)
				continue
			} else {
				collectingPaths = false
			}
		}
		if rest, ok := filesHeadingRest(trimmed); ok {
			if !sentenceDisqualified(trimmed) {
				claims = appendPathTokens(claims, seenPath, rest, line, check)
			}
			collectingPaths = true
		} else {
			for _, sentence := range splitSentences(trimmed) {
				claims = appendEditPathClaims(claims, seenPath, sentence, line, check)
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

// knownClaimPath is true for paths in the tree listing, changed_paths,
// or Scope globs. It does not accept a token by extension alone.
func knownClaimPath(tree, changed, scope []string) func(string) bool {
	listed := make(map[string]bool, len(tree)+len(changed))
	add := func(paths []string) {
		for _, path := range paths {
			path = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(path), "./"))
			if path != "" {
				listed[path] = true
			}
		}
	}
	add(tree)
	add(changed)
	return func(path string) bool {
		if listed[path] {
			return true
		}
		return len(scope) > 0 && gates.InScope(path, scope)
	}
}

func boundClaimReportLines(report string) []string {
	lines := splitReportLines(report)
	if len(lines) > claimEvidenceReportLines {
		lines = lines[len(lines)-claimEvidenceReportLines:]
	}
	return lines
}

func filesHeadingRest(trimmed string) (string, bool) {
	match := claimFilesHeading.FindStringSubmatch(trimmed)
	if match == nil {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func isSectionHeading(trimmed string) bool {
	if strings.HasPrefix(trimmed, "#") {
		return true
	}
	_, ok := filesHeadingRest(trimmed)
	return ok
}

func sentenceDisqualified(sentence string) bool {
	return claimPathDisqualify.MatchString(sentence) || hasStandaloneExample(sentence)
}

func hasStandaloneExample(s string) bool {
	lower := strings.ToLower(s)
	for i := 0; i < len(lower); {
		j := strings.Index(lower[i:], "example")
		if j < 0 {
			return false
		}
		j += i
		end := j + len("example")
		if j > 0 && isWordByte(lower[j-1]) {
			i = end
			continue
		}
		if end < len(lower) && isWordByte(lower[end]) {
			i = end
			continue
		}
		if end < len(lower) && lower[end] == '.' && end+1 < len(lower) && isWordByte(lower[end+1]) {
			i = end
			continue
		}
		return true
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func splitSentences(line string) []string {
	if line == "" {
		return nil
	}
	replaced := strings.NewReplacer(". ", ".\n", "! ", "!\n", "? ", "?\n").Replace(line)
	return strings.Split(replaced, "\n")
}

func appendEditPathClaims(claims []Claim, seen map[string]bool, sentence, line string, known func(string) bool) []Claim {
	if sentenceDisqualified(sentence) {
		return claims
	}
	loc := claimEditVerb.FindStringIndex(sentence)
	if loc == nil {
		return claims
	}
	for _, path := range pathsInText(sentence[loc[1]:]) {
		claims = appendPathClaim(claims, seen, path, line, known)
	}
	return claims
}

func pathsInText(s string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		path, ok := pathToken(raw)
		if !ok || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, raw := range claimBacktick.FindAllStringSubmatch(s, -1) {
		add(raw[1])
	}
	for _, field := range strings.Fields(s) {
		if strings.Contains(field, "`") {
			continue
		}
		add(field)
	}
	return out
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

func appendPathTokens(claims []Claim, seen map[string]bool, rest, line string, known func(string) bool) []Claim {
	if rest == "" {
		return claims
	}
	for _, raw := range strings.Split(rest, ",") {
		raw = strings.TrimSpace(raw)
		raw = strings.TrimPrefix(raw, "and ")
		if path, ok := pathToken(raw); ok {
			claims = appendPathClaim(claims, seen, path, line, known)
		}
	}
	return claims
}

func appendPathClaim(claims []Claim, seen map[string]bool, path, line string, known func(string) bool) []Claim {
	if known != nil && !known(path) {
		return claims
	}
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
	lower := strings.ToLower(slash)
	for _, prefix := range pathTokenPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return "", false
		}
	}
	ext := strings.ToLower(filepath.Ext(slash))
	if knownPathExt[lower] {
		return "", false
	}
	if !strings.Contains(slash, "/") && ext == "" {
		return "", false
	}
	if !knownPathExt[ext] {
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
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 || n > len(criteria) {
		return Claim{}
	}
	return Claim{
		Kind:      ClaimKindCriterion,
		Text:      criteria[n-1].Text,
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
	proofText := ""
	if proof != nil {
		proofText = proof.Signal
	}
	verifierText := ""
	if hasVerifier {
		verifierText = line
	}
	claim.Evidence = "proof: " + proofText + "; verifier: " + verifierText
	if (proof != nil && !proof.Pass) || (hasVerifier && verifierIncomplete(line)) {
		claim.Status = ClaimStatusContradicted
		claim.Source = ClaimSourceCode
		return claim
	}
	if proof != nil && proof.Pass && hasVerifier && verifierDone(line) {
		claim.Status = ClaimStatusSupported
		claim.Source = ClaimSourceCode
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

// ParseVerifierLines maps criterion numbers to the matching TASK n: line
// using the same regex gates uses.
func ParseVerifierLines(detail string) map[int]string {
	out := map[int]string{}
	for _, match := range claimVerifierLine.FindAllStringSubmatch(detail, -1) {
		n, _ := strconv.Atoi(match[1])
		if n < 1 {
			continue
		}
		out[n] = strings.TrimSpace(match[0])
	}
	return out
}
