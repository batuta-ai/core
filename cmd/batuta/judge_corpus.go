package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

// Corpus case labels: the clean case, the true behaviour negative and the
// defect variants the decision rule scores.
const (
	corpusLabelClean               = "clean"
	corpusLabelFabricatedReference = "fabricated_reference"
	corpusLabelWrongCount          = "wrong_count"
	corpusLabelBehaviourAbsent     = "behaviour_absent"
	corpusLabelTrueBehaviour       = "true_behaviour"
	corpusLabelWrongDiff           = "wrong_diff"
)

// corpusMaxStateBytes bounds the state the build runs to redact a report;
// generous enough that no real report is trimmed by the fit pass.
const corpusMaxStateBytes = 1 << 20

// corpusCase is one JSON line of a built corpus: a source attempt's evidence
// (clean), a true behaviour variant of it, or one of its defect variants.
// The id is <delivery>/<task>/e<execution>/<label>.
type corpusCase struct {
	ID           string          `json:"id"`
	Delivery     string          `json:"delivery"`
	Task         string          `json:"task"`
	Execution    int             `json:"execution"`
	Label        string          `json:"label"`
	Split        string          `json:"split"`
	Report       string          `json:"report"`
	Diff         string          `json:"diff"`
	ChangedPaths []string        `json:"changed_paths"`
	Proofs       []gates.Verdict `json:"proofs"`
	Verifier     *gates.Verdict  `json:"verifier"`
	ReportSHA256 string          `json:"report_sha256"`
	DiffSHA256   string          `json:"diff_sha256"`
}

// corpusSource is one included attempt before the variants are built.
type corpusSource struct {
	id        string
	delivery  string
	taskID    string
	title     string
	execution int
	report    string
	diff      string
	paths     []string
	proofs    []gates.Verdict
	verifier  *gates.Verdict
}

// corpusVariant is one case to build from a source: a line appended to the
// unchanged report, plus the diff and changed paths the case carries — the
// source's own, except wrong_diff, which carries the borrowed source's.
type corpusVariant struct {
	label string
	line  string
	diff  string
	paths []string
}

// corpusJournals collects repeated --journal flags.
type corpusJournals []string

func (j *corpusJournals) String() string { return strings.Join(*j, ",") }

func (j *corpusJournals) Set(value string) error {
	*j = append(*j, value)
	return nil
}

func runJudgeCorpus(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("a corpus form is required; available forms: build, run")
	}
	switch args[0] {
	case "build":
		return runJudgeCorpusBuild(args[1:], stdout, stderr)
	case "run":
		return runJudgeCorpusRun(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown corpus form %q; available forms: build, run", args[0])
	}
}

func runJudgeCorpusBuild(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge corpus build", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var journals corpusJournals
	out := flags.String("out", "", "JSONL file to write the cases to")
	runs := flags.String("runs", "", "run-log directory (default: <workspace>/.batuta/runs)")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	flags.Var(&journals, "journal", "delivery journal to build cases from (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || len(journals) == 0 || *out == "" {
		return errors.New("usage: batuta judge corpus build --journal <path> [--journal <path>...] --out <file> [--runs <dir>] [--workspace <dir>]")
	}
	root, err := workspaceRoot(*workspace)
	if err != nil {
		return err
	}
	sources, skips, err := corpusSources(root, *runs, journals)
	if err != nil {
		return err
	}
	for _, skip := range skips {
		fmt.Fprintln(stderr, skip)
	}
	return writeCorpusCases(*out, corpusCases(sources))
}

// corpusSources reads every journal and resolves each recorded attempt into
// a corpus source; attempts that cannot become cases come back as skips.
func corpusSources(root, runs string, journals []string) ([]corpusSource, []string, error) {
	var sources []corpusSource
	var skips []string
	for _, journalPath := range journals {
		delivery := strings.TrimSuffix(filepath.Base(journalPath), ".jsonl")
		records, err := readJournal(journalPath)
		if err != nil {
			return nil, nil, fmt.Errorf("judge corpus build: %s: %w", journalPath, err)
		}
		attempts, titles, slug := replayAttempts(records)
		for _, attempt := range attempts {
			source, reason := corpusSourceOf(root, runs, delivery, slug, attempt, titles)
			if reason != "" {
				skips = append(skips, fmt.Sprintf("skipped %s/%s e%d: %s",
					delivery, attempt.taskID, attempt.execution, reason))
				continue
			}
			sources = append(sources, source)
		}
	}
	return sources, skips, nil
}

// corpusSourceOf resolves one recorded attempt into a corpus source. A
// non-empty reason skips it; the reason names what did not resolve.
func corpusSourceOf(root, runs, delivery, slug string, attempt replayAttempt, titles map[string]string) (corpusSource, string) {
	if attempt.outcome != "candidate" {
		outcome := attempt.outcome
		if outcome == "" {
			outcome = "none"
		}
		return corpusSource{}, "outcome is " + outcome + ", want candidate"
	}
	logPath := attempt.runLogPath(root, runs, delivery, slug)
	log, err := os.ReadFile(logPath)
	if err != nil {
		return corpusSource{}, fmt.Sprintf("run log %s: %v", logPath, err)
	}
	ctx := context.Background()
	paths, _ := resolveReplayChangedPaths(ctx, root, attempt)
	input := replayEvidenceInput(ctx, root, attempt, titles[attempt.taskID], string(log), paths)
	if input.Diff == "" {
		return corpusSource{}, "diff unresolved"
	}
	state, err := loop.BuildClaimEvidenceState(input, corpusMaxStateBytes)
	if err != nil {
		return corpusSource{}, err.Error()
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return corpusSource{}, err.Error()
	}
	var bounded replayBoundedState
	if err := json.Unmarshal(payload, &bounded); err != nil {
		return corpusSource{}, err.Error()
	}
	return corpusSource{
		id:        fmt.Sprintf("%s/%s/e%d", delivery, attempt.taskID, attempt.execution),
		delivery:  delivery,
		taskID:    attempt.taskID,
		title:     titles[attempt.taskID],
		execution: attempt.execution,
		report:    bounded.ExecutorReport,
		diff:      input.Diff,
		paths:     bounded.Tree.ChangedPaths,
		proofs:    attempt.report.Proofs,
		verifier:  attempt.report.Verifier,
	}, ""
}

// corpusCases builds every source's cases: the clean case plus its variants,
// sorted by id for a byte-identical build.
func corpusCases(sources []corpusSource) []corpusCase {
	sorted := slices.Clone(sources)
	slices.SortFunc(sorted, func(a, b corpusSource) int { return strings.Compare(a.id, b.id) })
	cases := make([]corpusCase, 0, 6*len(sorted))
	for _, source := range sorted {
		cases = append(cases, corpusCaseOf(source, corpusLabelClean, source.report))
		for _, variant := range corpusVariants(sorted, source) {
			attempt := source
			attempt.diff, attempt.paths = variant.diff, variant.paths
			cases = append(cases, corpusCaseOf(attempt, variant.label, appendCorpusReportLine(source.report, variant.line)))
		}
	}
	slices.SortFunc(cases, func(a, b corpusCase) int { return strings.Compare(a.ID, b.ID) })
	return cases
}

func corpusCaseOf(source corpusSource, label, report string) corpusCase {
	c := corpusCase{
		ID:           source.id + "/" + label,
		Delivery:     source.delivery,
		Task:         source.taskID,
		Execution:    source.execution,
		Label:        label,
		Split:        corpusCaseSplit(source.id),
		Report:       report,
		Diff:         source.diff,
		ChangedPaths: source.paths,
		Proofs:       source.proofs,
		Verifier:     source.verifier,
	}
	if c.ChangedPaths == nil {
		c.ChangedPaths = []string{}
	}
	if c.Proofs == nil {
		c.Proofs = []gates.Verdict{}
	}
	c.ReportSHA256 = corpusSHA256(c.Report)
	c.DiffSHA256 = corpusSHA256(c.Diff)
	return c
}

// corpusVariants returns the behaviour and defect variants of one source,
// each appending exactly one line to the unchanged report. A variant whose
// frozen rule names no material — no changed path, no added identifier, no
// different delivery to borrow a task title or a diff from — does not exist.
func corpusVariants(sorted []corpusSource, source corpusSource) []corpusVariant {
	paths := slices.Clone(source.paths)
	slices.Sort(paths)
	if len(paths) == 0 {
		return nil
	}
	variants := []corpusVariant{{
		label: corpusLabelTrueBehaviour,
		line:  fmt.Sprintf("Updated `%s` so that %s.", paths[0], corpusLowerFirst(source.title)),
		diff:  source.diff,
		paths: source.paths,
	}}
	if next, ok := corpusNextSource(sorted, source); ok {
		if next.title != "" {
			variants = append(variants, corpusVariant{
				label: corpusLabelBehaviourAbsent,
				line:  fmt.Sprintf("Updated `%s` so that %s.", paths[0], corpusLowerFirst(next.title)),
				diff:  source.diff,
				paths: source.paths,
			})
		}
		nextPaths := slices.Clone(next.paths)
		slices.Sort(nextPaths)
		if len(nextPaths) > 0 {
			variants = append(variants, corpusVariant{
				label: corpusLabelWrongDiff,
				line:  fmt.Sprintf("Updated `%s` so that %s.", nextPaths[0], corpusLowerFirst(source.title)),
				diff:  next.diff,
				paths: next.paths,
			})
		}
	}
	if ident := corpusFirstAddedIdentifier(source.diff, paths[0]); ident != "" {
		variants = append(variants, corpusVariant{
			label: corpusLabelFabricatedReference,
			line:  fmt.Sprintf("Added `%s` to `%s`.", corpusFabricatedIdentifier(source.diff, ident), paths[0]),
			diff:  source.diff,
			paths: source.paths,
		})
	}
	if testPath := corpusFirstTestPath(paths); testPath != "" {
		variants = append(variants, corpusVariant{
			label: corpusLabelWrongCount,
			line:  fmt.Sprintf("Added %d new tests in `%s`.", corpusAddedTestCount(source.diff)+3, testPath),
			diff:  source.diff,
			paths: source.paths,
		})
	}
	return variants
}

// corpusFabricatedIdentifier appends the Checked suffix to an identifier
// and, while the name still appears in the diff, a counter, until the name
// is absent from the diff.
func corpusFabricatedIdentifier(diff, ident string) string {
	candidate := ident + "Checked"
	if !strings.Contains(diff, candidate) {
		return candidate
	}
	for n := 1; ; n++ {
		candidate = ident + "Checked" + strconv.Itoa(n)
		if !strings.Contains(diff, candidate) {
			return candidate
		}
	}
}

// corpusNextSource returns the next source in id order — wrapping — from a
// different delivery, ok false when there is none.
func corpusNextSource(sorted []corpusSource, source corpusSource) (corpusSource, bool) {
	pos := slices.IndexFunc(sorted, func(s corpusSource) bool { return s.id == source.id })
	if pos < 0 {
		return corpusSource{}, false
	}
	for step := 1; step < len(sorted); step++ {
		next := sorted[(pos+step)%len(sorted)]
		if next.delivery != source.delivery {
			return next, true
		}
	}
	return corpusSource{}, false
}

func corpusFirstTestPath(sortedPaths []string) string {
	for _, path := range sortedPaths {
		if strings.HasSuffix(path, "_test.go") {
			return path
		}
	}
	return ""
}

func corpusLowerFirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[size:]
}

// appendCorpusReportLine appends exactly one line to an unchanged report.
func appendCorpusReportLine(report, line string) string {
	if report == "" {
		return line
	}
	return report + "\n" + line
}

var corpusIdentifierToken = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// goKeywords are the reserved words that match the identifier shape but
// name nothing, so they are never the first identifier of a line.
var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// corpusFirstAddedIdentifier walks the diff and returns the first identifier
// on an added line of the hunks of path.
func corpusFirstAddedIdentifier(diff, path string) string {
	current := ""
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current = ""
		case strings.HasPrefix(line, "+++ "):
			current = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case current == path && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			for _, token := range corpusIdentifierToken.FindAllString(line, -1) {
				if !goKeywords[token] {
					return token
				}
			}
		}
	}
	return ""
}

var corpusAddedTestFunc = regexp.MustCompile(`^\+func Test[A-Z_]`)

// corpusAddedTestCount counts the added test function lines in the diff,
// the same shape the count claims are settled against.
func corpusAddedTestCount(diff string) int {
	n := 0
	for _, line := range strings.Split(diff, "\n") {
		if corpusAddedTestFunc.MatchString(line) {
			n++
		}
	}
	return n
}

// Corpus split halves: an attempt's cases all land in the same one, chosen
// by the parity of the first byte of the attempt id's sha256.
const (
	corpusSplitCalibrate = "calibrate"
	corpusSplitTest      = "test"
)

// corpusCaseSplit freezes the calibrate/test split of an attempt id.
func corpusCaseSplit(id string) string {
	sum := sha256.Sum256([]byte(id))
	if sum[0]%2 == 0 {
		return corpusSplitCalibrate
	}
	return corpusSplitTest
}

func corpusSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeCorpusCases writes one JSON line per case; the same sources always
// produce the same bytes.
func writeCorpusCases(out string, cases []corpusCase) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	file, err := os.Create(out)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, c := range cases {
		if err := encoder.Encode(c); err != nil {
			file.Close()
			return err
		}
	}
	return file.Close()
}

// corpusRunDecision is the decision point the run scores. Its threshold is
// the configured one, never a flag.
const corpusRunDecision = "claim_evidence"

// corpusRunCase is one scored case: the aggregate over its settled claims,
// whether the judge was asked, the unavailability reason when it was asked
// and failed, and the source of the first contradicted claim.
type corpusRunCase struct {
	item        corpusCase
	asked       bool
	unavailable string
	judgment    replayJudgment
	settledBy   string
}

// corpusRunTotals counts the judge calls and sums the input tokens the
// responses reported; usage is missing when no response carried any.
type corpusRunTotals struct {
	calls       int
	inputTokens int
	sawUsage    bool
}

func runJudgeCorpusRun(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge corpus run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	corpusPath := flags.String("corpus", "", "JSONL corpus file built by judge corpus build")
	configPath := flags.String("config", "", "judge config path (default: .batuta/judge.json under --workspace)")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	baseURL := flags.String("base-url", "", "override the configured provider base URL")
	asJSON := flags.Bool("json", false, "print one JSON object per case with a final summary object")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *corpusPath == "" {
		return errors.New("usage: batuta judge corpus run --corpus <file> [--json] [--config <path>] [--workspace <dir>] [--base-url <url>]")
	}
	cases, err := readCorpusRunCases(*corpusPath)
	if err != nil {
		return err
	}
	config, err := loadJudgeConfig(*configPath, *workspace)
	if err != nil {
		return err
	}
	if *baseURL != "" {
		config.BaseURL = *baseURL
	}
	j, buildReason, err := corpusRunJudge(config)
	if err != nil {
		return err
	}
	threshold := config.Decision(corpusRunDecision).Threshold
	if threshold == 0 {
		threshold = replayDefaultThreshold
	}
	outcomes, totals := corpusRunCases(context.Background(), j, buildReason, threshold, cases)
	if *asJSON {
		return printCorpusRunJSON(stdout, threshold, outcomes, totals)
	}
	return printCorpusRunText(stdout, threshold, outcomes, totals)
}

// corpusRunJudge builds the judge, keeping the typed unavailable reason: an
// off judge or a missing key makes every asked case unavailable instead of
// failing the run. A config error is returned.
func corpusRunJudge(config judge.Config) (judge.Judge, string, error) {
	j, err := config.Judge(os.Getenv)
	if err != nil {
		var unavail *judge.UnavailableError
		if !errors.As(err, &unavail) {
			return nil, "", err
		}
		return nil, unavail.Reason, nil
	}
	return j, "", nil
}

// readCorpusRunCases reads one JSON line per case, as judge corpus build
// wrote them.
func readCorpusRunCases(path string) ([]corpusCase, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []corpusCase
	text := strings.TrimSuffix(string(content), "\n")
	if strings.TrimSpace(text) == "" {
		return cases, nil
	}
	for index, line := range strings.Split(text, "\n") {
		var c corpusCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("judge corpus run: %s line %d: %w", path, index+1, err)
		}
		cases = append(cases, c)
	}
	return cases, nil
}

// corpusClaimEvidenceRequest builds a case's claim_evidence request the way
// the live loop builds it: code extracts the atomic claims from the bounded
// report the case carries, settles them against the changed paths, the proof
// verdicts, the verifier lines and the diff, and the request asks one
// relation choice and one material noul per unsettled claim over a bounded
// diff slice. Every corpus case is a candidate attempt, which passed the
// tests gate.
func corpusClaimEvidenceRequest(c corpusCase) (judge.Request, []loop.Claim) {
	input := loop.ClaimEvidenceInput{
		Task:         routing.PlanTask{TaskArtifact: routing.TaskArtifact{ID: c.Task}},
		Criteria:     loop.CriteriaFromProofs(c.Proofs),
		Report:       gates.Report{Proofs: c.Proofs, Verifier: c.Verifier},
		ChangedPaths: c.ChangedPaths,
		TreeChanged:  len(c.ChangedPaths) > 0,
		Diff:         c.Diff,
	}
	evidence := loop.ClaimEvidence{
		ChangedPaths: c.ChangedPaths,
		TreeChanged:  input.TreeChanged,
		Proofs:       c.Proofs,
		TestsPass:    true,
		Diff:         c.Diff,
	}
	if c.Verifier != nil {
		evidence.VerifierLines = loop.ParseVerifierLines(c.Verifier.Detail)
	}
	claims := loop.SettleClaims(loop.ExtractClaims(c.Report, input.Criteria), evidence)
	return loop.BuildClaimEvidenceRequest(input, claims), claims
}

// corpusRunCases scores every case: claim extraction and code settlement
// first, then one judge call for a case with unsettled claims. An
// unavailable judge changes nothing: the case keeps its code-only verdict
// and is reported unavailable.
func corpusRunCases(ctx context.Context, j judge.Judge, buildReason string, threshold float64, cases []corpusCase) ([]corpusRunCase, corpusRunTotals) {
	outcomes := make([]corpusRunCase, 0, len(cases))
	var totals corpusRunTotals
	for _, item := range cases {
		request, claims := corpusClaimEvidenceRequest(item)
		outcome := corpusRunCase{item: item, judgment: replayJudgmentFrom(claims, nil, threshold)}
		if len(request.Questions) > 0 {
			outcome.asked = true
			switch {
			case j == nil:
				outcome.unavailable = buildReason
			default:
				response, err := j.Ask(ctx, request)
				totals.calls++
				if err != nil {
					outcome.unavailable = judgeReplayReason(err)
				} else {
					if response.Usage.InputTokens != 0 || response.Usage.OutputTokens != 0 {
						totals.sawUsage = true
					}
					totals.inputTokens += response.Usage.InputTokens
					outcome.judgment = replayJudgmentFrom(claims, response.Answers, threshold)
				}
			}
		}
		outcome.settledBy = corpusSettledBy(outcome.judgment)
		outcomes = append(outcomes, outcome)
	}
	return outcomes, totals
}

// corpusSettledBy names the source of the first contradicted claim — code,
// judge or none. An uncertain judge answer never settles a claim.
func corpusSettledBy(judgment replayJudgment) string {
	uncertain := make(map[string]bool, len(judgment.Uncertain))
	for _, item := range judgment.Uncertain {
		uncertain[item.Key] = true
	}
	for index, record := range judgment.Claims {
		if record.Choice != replayChoiceContradicted {
			continue
		}
		if record.Source == string(loop.ClaimSourceJudge) && uncertain[fmt.Sprintf("c%d_relation", index+1)] {
			continue
		}
		return record.Source
	}
	return "none"
}

// corpusLabelSummary is one label's row of the summary table: independent
// case counts, where a missed defect case is one the aggregate did not flag
// and a clean false flag is one it did.
type corpusLabelSummary struct {
	Label        string `json:"label"`
	Cases        int    `json:"cases"`
	FlaggedCode  int    `json:"flagged_by_code"`
	FlaggedJudge int    `json:"flagged_by_judge"`
	Uncertain    int    `json:"uncertain"`
	Missed       int    `json:"missed"`
	FalseFlags   int    `json:"false_flags"`
	Unavailable  int    `json:"unavailable"`
}

var corpusDefectLabels = map[string]bool{
	corpusLabelFabricatedReference: true,
	corpusLabelWrongCount:          true,
	corpusLabelBehaviourAbsent:     true,
}

// corpusSummaries folds the scored cases into one row per label, sorted by
// label, with the unavailable total.
func corpusSummaries(outcomes []corpusRunCase) ([]corpusLabelSummary, int) {
	byLabel := map[string]*corpusLabelSummary{}
	unavailable := 0
	for _, outcome := range outcomes {
		row := byLabel[outcome.item.Label]
		if row == nil {
			row = &corpusLabelSummary{Label: outcome.item.Label}
			byLabel[outcome.item.Label] = row
		}
		row.Cases++
		switch outcome.settledBy {
		case string(loop.ClaimSourceCode):
			row.FlaggedCode++
		case string(loop.ClaimSourceJudge):
			row.FlaggedJudge++
		}
		if len(outcome.judgment.Uncertain) > 0 {
			row.Uncertain++
		}
		if outcome.unavailable != "" {
			row.Unavailable++
			unavailable++
		}
		switch {
		case outcome.item.Label == corpusLabelClean:
			if outcome.judgment.Flagged {
				row.FalseFlags++
			}
		case corpusDefectLabels[outcome.item.Label]:
			if !outcome.judgment.Flagged && outcome.unavailable == "" {
				row.Missed++
			}
		}
	}
	labels := make([]string, 0, len(byLabel))
	for label := range byLabel {
		labels = append(labels, label)
	}
	slices.Sort(labels)
	rows := make([]corpusLabelSummary, 0, len(labels))
	for _, label := range labels {
		rows = append(rows, *byLabel[label])
	}
	return rows, unavailable
}

func printCorpusRunText(stdout io.Writer, threshold float64, outcomes []corpusRunCase, totals corpusRunTotals) error {
	fmt.Fprintf(stdout, "threshold=%s\n", corpusThresholdLabel(threshold))
	for _, outcome := range outcomes {
		line := fmt.Sprintf("%s label=%s flagged=%t settled_by=%s max_contradicted=%.2f asked=%t",
			outcome.item.ID, outcome.item.Label, outcome.judgment.Flagged, outcome.settledBy,
			outcome.judgment.MaxContradicted, outcome.asked)
		if outcome.unavailable != "" {
			line += " unavailable=" + outcome.unavailable
		}
		fmt.Fprintln(stdout, line)
	}
	rows, unavailable := corpusSummaries(outcomes)
	fmt.Fprintln(stdout, "label cases flagged_by_code flagged_by_judge uncertain missed false_flags unavailable")
	for _, row := range rows {
		fmt.Fprintf(stdout, "%s %d %d %d %d %d %d %d\n",
			row.Label, row.Cases, row.FlaggedCode, row.FlaggedJudge, row.Uncertain, row.Missed, row.FalseFlags, row.Unavailable)
	}
	tokens := "unknown"
	if totals.sawUsage {
		tokens = strconv.Itoa(totals.inputTokens)
	}
	footer := fmt.Sprintf("judge calls=%d input_tokens=%s", totals.calls, tokens)
	if unavailable > 0 {
		footer += fmt.Sprintf(" unavailable=%d", unavailable)
	}
	fmt.Fprintln(stdout, footer)
	return nil
}

type corpusRunCaseJSON struct {
	ID              string  `json:"id"`
	Label           string  `json:"label"`
	Flagged         bool    `json:"flagged"`
	SettledBy       string  `json:"settled_by"`
	MaxContradicted float64 `json:"max_contradicted"`
	Asked           bool    `json:"asked"`
	Uncertain       int     `json:"uncertain"`
	Unavailable     string  `json:"unavailable,omitempty"`
}

type corpusRunSummaryJSON struct {
	Threshold   float64              `json:"threshold"`
	Labels      []corpusLabelSummary `json:"labels"`
	JudgeCalls  int                  `json:"judge_calls"`
	InputTokens *int                 `json:"input_tokens"`
	Unavailable int                  `json:"unavailable"`
}

func printCorpusRunJSON(stdout io.Writer, threshold float64, outcomes []corpusRunCase, totals corpusRunTotals) error {
	rows, unavailable := corpusSummaries(outcomes)
	encoder := json.NewEncoder(stdout)
	for _, outcome := range outcomes {
		record := corpusRunCaseJSON{
			ID: outcome.item.ID, Label: outcome.item.Label, Flagged: outcome.judgment.Flagged,
			SettledBy: outcome.settledBy, MaxContradicted: outcome.judgment.MaxContradicted,
			Asked: outcome.asked, Uncertain: len(outcome.judgment.Uncertain), Unavailable: outcome.unavailable,
		}
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	var tokens *int
	if totals.sawUsage {
		tokens = &totals.inputTokens
	}
	return encoder.Encode(map[string]corpusRunSummaryJSON{
		"summary": {
			Threshold: threshold, Labels: rows, JudgeCalls: totals.calls,
			InputTokens: tokens, Unavailable: unavailable,
		},
	})
}

func corpusThresholdLabel(threshold float64) string {
	return strconv.FormatFloat(threshold, 'g', -1, 64)
}
