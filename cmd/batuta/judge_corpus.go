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
	"github.com/batuta-ai/core/loop"
)

// Corpus case labels: the clean case plus the report-only defect variants
// the decision rule scores.
const (
	corpusLabelClean               = "clean"
	corpusLabelFabricatedReference = "fabricated_reference"
	corpusLabelWrongCount          = "wrong_count"
	corpusLabelBehaviourAbsent     = "behaviour_absent"
)

// corpusMaxStateBytes bounds the state the build runs to redact a report;
// generous enough that no real report is trimmed by the fit pass.
const corpusMaxStateBytes = 1 << 20

// corpusCase is one JSON line of a built corpus: a source attempt's evidence
// (clean) or a report-only defect variant of it. The id is
// <delivery>/<task>/e<execution>/<label>.
type corpusCase struct {
	ID           string          `json:"id"`
	Delivery     string          `json:"delivery"`
	Task         string          `json:"task"`
	Execution    int             `json:"execution"`
	Label        string          `json:"label"`
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

type corpusVariant struct {
	label string
	line  string
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
		return errors.New("a corpus form is required; available forms: build")
	}
	switch args[0] {
	case "build":
		return runJudgeCorpusBuild(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown corpus form %q; available forms: build", args[0])
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

// corpusCases builds every source's cases: the clean case plus the
// report-only defect variants, sorted by id for a byte-identical build.
func corpusCases(sources []corpusSource) []corpusCase {
	sorted := slices.Clone(sources)
	slices.SortFunc(sorted, func(a, b corpusSource) int { return strings.Compare(a.id, b.id) })
	cases := make([]corpusCase, 0, 4*len(sorted))
	for _, source := range sorted {
		cases = append(cases, corpusCaseOf(source, corpusLabelClean, source.report))
		for _, variant := range corpusVariants(sorted, source) {
			cases = append(cases, corpusCaseOf(source, variant.label, appendCorpusReportLine(source.report, variant.line)))
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

// corpusVariants returns the report-only defect variants of one source,
// each appending exactly one line to the unchanged report. A variant whose
// frozen rule names no material — no changed path, no added identifier, no
// different delivery to borrow a task title from — does not exist.
func corpusVariants(sorted []corpusSource, source corpusSource) []corpusVariant {
	paths := slices.Clone(source.paths)
	slices.Sort(paths)
	if len(paths) == 0 {
		return nil
	}
	var variants []corpusVariant
	if ident := corpusFirstAddedIdentifier(source.diff, paths[0]); ident != "" {
		variants = append(variants, corpusVariant{
			label: corpusLabelFabricatedReference,
			line:  fmt.Sprintf("Added `%s` to `%s`.", corpusFabricatedIdentifier(source.diff, ident), paths[0]),
		})
	}
	if testPath := corpusFirstTestPath(paths); testPath != "" {
		variants = append(variants, corpusVariant{
			label: corpusLabelWrongCount,
			line:  fmt.Sprintf("Added %d new tests in `%s`.", corpusAddedTestCount(source.diff)+3, testPath),
		})
	}
	if title := corpusBehaviourTitle(sorted, source); title != "" {
		variants = append(variants, corpusVariant{
			label: corpusLabelBehaviourAbsent,
			line:  fmt.Sprintf("Updated `%s` so that %s.", paths[0], corpusLowerFirst(title)),
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

// corpusBehaviourTitle returns the task title of the next source in id
// order — wrapping — from a different delivery, empty when there is none.
func corpusBehaviourTitle(sorted []corpusSource, source corpusSource) string {
	pos := slices.IndexFunc(sorted, func(s corpusSource) bool { return s.id == source.id })
	if pos < 0 {
		return ""
	}
	for step := 1; step < len(sorted); step++ {
		next := sorted[(pos+step)%len(sorted)]
		if next.delivery != source.delivery {
			return next.title
		}
	}
	return ""
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
