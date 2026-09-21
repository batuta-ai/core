package loop

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

const (
	claimEvidenceReportLines = 60
	claimEvidenceReportBytes = 8 << 10
)

var (
	claimProgressLine = regexp.MustCompile(`^BATUTA-PROGRESS [0-9]+ (START|DONE)$`)
	claimTaskLine     = regexp.MustCompile(`(?i)^TASK\s+[0-9]+\s*:`)
	claimEnvLine      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*=`)
	claimAbsolutePath = regexp.MustCompile(`(?:[A-Za-z]:)?(?:/|\\)[^\s"'=]+`)
)

// ClaimEvidenceInput is the bounded evidence for the claim_evidence decision.
type ClaimEvidenceInput struct {
	Workspace    string
	Task         routing.PlanTask
	Criteria     []gates.Criterion
	Report       gates.Report
	OutputTail   string
	Progress     []executor.ProgressEvent
	ChangedPaths []string
	TreeChanged  bool
}

type claimEvidenceState struct {
	Task           claimEvidenceTask        `json:"task"`
	Criteria       []claimEvidenceCriterion `json:"criteria"`
	ExecutorReport string                   `json:"executor_report"`
	Progress       []claimEvidenceProgress  `json:"progress"`
	Tree           claimEvidenceTree        `json:"tree"`
	Verifier       *claimEvidenceVerifier   `json:"verifier,omitempty"`
	Outcome        claimEvidenceOutcome     `json:"outcome"`
}

type claimEvidenceTask struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Scope []string `json:"scope"`
}

type claimEvidenceCriterion struct {
	Index  int    `json:"index"`
	Text   string `json:"text"`
	Proof  string `json:"proof,omitempty"`
	Pass   bool   `json:"pass"`
	Signal string `json:"signal"`
}

type claimEvidenceProgress struct {
	Criterion int    `json:"criterion"`
	State     string `json:"state"`
	At        string `json:"at,omitempty"`
}

type claimEvidenceTree struct {
	Changed      bool     `json:"changed"`
	ChangedPaths []string `json:"changed_paths"`
}

type claimEvidenceVerifier struct {
	Signal string `json:"signal"`
	Detail string `json:"detail,omitempty"`
}

type claimEvidenceOutcome struct {
	Passed       bool     `json:"passed"`
	FailingGates []string `json:"failing_gates"`
}

// BuildClaimEvidenceState reduces an attempt's evidence to a JSON object that
// never exceeds maxBytes. Executor output is untrusted and is redacted before
// it is returned.
func BuildClaimEvidenceState(input ClaimEvidenceInput, maxBytes int) (any, error) {
	workspace := input.Workspace
	if workspace != "" {
		workspace = filepath.Clean(workspace)
	}

	scope := redactPaths(input.Task.Scope, workspace)
	criteria := make([]claimEvidenceCriterion, 0, len(input.Criteria))
	for index, criterion := range input.Criteria {
		item := claimEvidenceCriterion{
			Index: index + 1,
			Text:  redactText(criterion.Text, workspace),
			Proof: redactText(criterion.Proof, workspace),
		}
		if index < len(input.Report.Proofs) {
			item.Pass = input.Report.Proofs[index].Pass
			item.Signal = redactText(input.Report.Proofs[index].Signal, workspace)
		}
		criteria = append(criteria, item)
	}

	progress := make([]claimEvidenceProgress, 0, len(input.Progress))
	for _, event := range input.Progress {
		item := claimEvidenceProgress{Criterion: event.Criterion, State: event.State}
		if !event.At.IsZero() {
			item.At = event.At.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		progress = append(progress, item)
	}

	paths := redactPaths(input.ChangedPaths, workspace)
	state := claimEvidenceState{
		Task: claimEvidenceTask{
			ID:    input.Task.ID,
			Title: redactText(input.Task.Title, workspace),
			Scope: scope,
		},
		Criteria:       criteria,
		ExecutorReport: boundExecutorReport(input.OutputTail, workspace),
		Progress:       progress,
		Tree: claimEvidenceTree{
			Changed:      input.TreeChanged,
			ChangedPaths: paths,
		},
		Outcome: claimEvidenceOutcome{
			Passed:       input.Report.Passed,
			FailingGates: failingGateNames(input.Report),
		},
	}
	if input.Report.Verifier != nil {
		state.Verifier = &claimEvidenceVerifier{
			Signal: redactText(input.Report.Verifier.Signal, workspace),
			Detail: redactText(dropSecretLines(input.Report.Verifier.Detail), workspace),
		}
	}

	fitted, err := fitClaimEvidenceState(state, maxBytes)
	if err != nil {
		return nil, err
	}
	return fitted, nil
}

// ClaimEvidenceQuestions returns the two noul questions of the claim_evidence
// decision, with instructions written for a literal reader.
func ClaimEvidenceQuestions() map[string]judge.Question {
	return map[string]judge.Question{
		"claim_unsupported": {
			Type:         judge.QuestionNoul,
			Instructions: "The executor's report claims work (files changed, tests passed, criteria done) that the tree, proof and verifier evidence in this state does not show.",
			Criteria: map[string]string{
				"true":  "a claim in executor_report is contradicted by tree, proofs or verifier",
				"false": "every claim in executor_report is consistent with the evidence",
			},
		},
		"verifier_contradicted": {
			Type:         judge.QuestionNoul,
			Instructions: "The verifier's DONE lines are contradicted by a failed proof or by the executor's own report.",
			Criteria: map[string]string{
				"true":  "a verifier DONE line is contradicted by a failed proof or by executor_report",
				"false": "every verifier DONE line is consistent with the proofs and executor_report",
			},
		},
	}
}

func boundExecutorReport(output, workspace string) string {
	lines := splitReportLines(output)
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if claimEnvLine.MatchString(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, redactText(line, workspace))
	}
	if len(kept) > claimEvidenceReportLines {
		kept = kept[len(kept)-claimEvidenceReportLines:]
	}
	return capReportLines(kept, claimEvidenceReportBytes)
}

func splitReportLines(output string) []string {
	if output == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(output, "\n"), "\n")
}

func capReportLines(lines []string, maxBytes int) string {
	if len(lines) == 0 {
		return ""
	}
	var kept []string
	size := 0
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		extra := len(line)
		if len(kept) > 0 {
			extra++
		}
		if size+extra > maxBytes && !protectedReportLine(line) {
			break
		}
		if size+extra > maxBytes && protectedReportLine(line) {
			kept = append(kept, line)
			break
		}
		kept = append(kept, line)
		size += extra
	}
	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		kept[left], kept[right] = kept[right], kept[left]
	}
	return strings.Join(kept, "\n")
}

func protectedReportLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return claimProgressLine.MatchString(trimmed) || claimTaskLine.MatchString(trimmed)
}

func dropSecretLines(value string) string {
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if claimEnvLine.MatchString(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func redactText(value, workspace string) string {
	if value == "" {
		return ""
	}
	if workspace != "" {
		for _, prefix := range []string{
			workspace + string(filepath.Separator),
			workspace + "/",
			workspace + `\`,
			workspace,
		} {
			value = strings.ReplaceAll(value, prefix, "")
		}
	}
	matches := claimAbsolutePath.FindAllStringIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	var b strings.Builder
	last := 0
	for _, loc := range matches {
		start, end := loc[0], loc[1]
		if start > 0 {
			prev := value[start-1]
			if prev == '.' || prev == '\\' || alphanumeric(prev) {
				continue
			}
		}
		match := value[start:end]
		cleaned := strings.TrimRight(match, ".,;:)")
		if strings.HasPrefix(cleaned, "//") {
			continue
		}
		slash := filepath.ToSlash(cleaned)
		if !filepath.IsAbs(cleaned) && !strings.HasPrefix(slash, "/") {
			continue
		}
		b.WriteString(value[last:start])
		b.WriteString(filepath.ToSlash(filepath.Base(cleaned)))
		b.WriteString(match[len(cleaned):])
		last = end
	}
	b.WriteString(value[last:])
	return b.String()
}

func alphanumeric(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func redactPaths(paths []string, workspace string) []string {
	if paths == nil {
		return []string{}
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, relativePath(path, workspace))
	}
	return out
}

func relativePath(path, workspace string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if workspace != "" {
		if rel, err := filepath.Rel(workspace, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
		if rel, err := filepath.Rel(workspace, path); err == nil && rel == "." {
			return "."
		}
	}
	if filepath.IsAbs(path) || strings.HasPrefix(filepath.ToSlash(path), "/") {
		return filepath.ToSlash(filepath.Base(path))
	}
	return filepath.ToSlash(path)
}

func failingGateNames(report gates.Report) []string {
	var names []string
	add := func(verdict gates.Verdict) {
		if verdict.Name == "" || verdict.Pass {
			return
		}
		names = append(names, verdict.Name)
	}
	add(report.Finished)
	add(report.Tree)
	add(report.Tests)
	add(report.Scope)
	for _, proof := range report.Proofs {
		add(proof)
	}
	if report.Verifier != nil {
		add(*report.Verifier)
	}
	if names == nil {
		return []string{}
	}
	return names
}

func fitClaimEvidenceState(state claimEvidenceState, maxBytes int) (claimEvidenceState, error) {
	if maxBytes <= 0 {
		return state, nil
	}
	if sizeOf(state) <= maxBytes {
		return state, nil
	}

	lines := splitReportLines(state.ExecutorReport)
	for len(lines) > 0 && sizeOf(state) > maxBytes {
		lines = lines[1:]
		state.ExecutorReport = strings.Join(lines, "\n")
	}
	if sizeOf(state) <= maxBytes {
		return state, nil
	}

	for len(state.Tree.ChangedPaths) > 0 && sizeOf(state) > maxBytes {
		state.Tree.ChangedPaths = state.Tree.ChangedPaths[:len(state.Tree.ChangedPaths)-1]
	}
	if sizeOf(state) <= maxBytes {
		return state, nil
	}

	skeleton := state
	skeleton.ExecutorReport = ""
	skeleton.Tree.ChangedPaths = []string{}
	if sizeOf(skeleton) > maxBytes {
		return claimEvidenceState{}, fmt.Errorf("loop: claim-evidence state exceeds %d bytes", maxBytes)
	}
	return skeleton, nil
}

func sizeOf(state claimEvidenceState) int {
	encoded, err := json.Marshal(state)
	if err != nil {
		return 0
	}
	return len(encoded)
}
