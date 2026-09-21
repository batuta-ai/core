package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

const (
	claimEvidenceDecision         = "claim_evidence"
	claimUnsupportedKey           = "claim_unsupported"
	verifierContradictedKey       = "verifier_contradicted"
	defaultClaimEvidenceThreshold = 0.9
	claimEvidenceReportLines      = 60
	claimEvidenceReportBytes      = 8 << 10
)

// judgment is the loop's view of one claim_evidence call. Shadow records it
// and stops; enforce may fail a passing attempt.
type judgment struct {
	Asked       bool
	Unavailable string
	Answers     map[string]float64
}

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

// ReplayClaimEvidenceInput carries what a delivery journal holds about one
// recorded attempt: the gates_reported detail, the tree_changed flag of the
// matching executor_finished detail, and the attempt's run log content.
type ReplayClaimEvidenceInput struct {
	Workspace   string
	TaskID      string
	TaskTitle   string
	Report      gates.Report
	TreeChanged bool
	RunLog      string
}

// ReplayClaimEvidenceState rebuilds the bounded claim_evidence state of a
// recorded attempt from its journal details and run log, so `judge replay`
// asks the same decision over the same evidence. The plan file is not part
// of the journal: the criteria come from the recorded proof signals, and the
// changed paths only from a failed scope verdict, so both are reconstructions
// and the progress events carry no timestamps.
func ReplayClaimEvidenceState(input ReplayClaimEvidenceInput, maxBytes int) (any, error) {
	tail, progress := ParseRunLog(input.RunLog)
	var changedPaths []string
	if !input.Report.Scope.Pass && input.Report.Scope.Detail != "" {
		changedPaths = splitReportLines(input.Report.Scope.Detail)
	}
	return BuildClaimEvidenceState(ClaimEvidenceInput{
		Workspace: input.Workspace,
		Task: routing.PlanTask{
			TaskArtifact: routing.TaskArtifact{ID: input.TaskID, Title: input.TaskTitle},
		},
		Criteria:     CriteriaFromProofs(input.Report.Proofs),
		Report:       input.Report,
		OutputTail:   tail,
		Progress:     progress,
		ChangedPaths: changedPaths,
		TreeChanged:  input.TreeChanged,
	}, maxBytes)
}

// CriteriaFromProofs recovers the acceptance criteria a recorded report's
// proof verdicts were run against. It inverts the signals gates.Proofs
// writes: `<text> — `<proof>` …`, `<text> — no proof command…`; a signal it
// cannot parse stays the criterion's whole text.
func CriteriaFromProofs(proofs []gates.Verdict) []gates.Criterion {
	criteria := make([]gates.Criterion, 0, len(proofs))
	for _, proof := range proofs {
		text, command := criterionFromSignal(proof.Signal)
		criteria = append(criteria, gates.Criterion{Text: text, Proof: command})
	}
	return criteria
}

const criterionNoProofSignal = " — no proof command; left to the verifier"

func criterionFromSignal(signal string) (text, proof string) {
	if strings.HasSuffix(signal, criterionNoProofSignal) {
		return strings.TrimSuffix(signal, criterionNoProofSignal), ""
	}
	if start := strings.Index(signal, " — "); start >= 0 {
		rest := strings.TrimPrefix(signal[start+len(" — "):], "could not run ")
		if strings.HasPrefix(rest, "`") {
			if end := strings.Index(rest[1:], "`"); end >= 0 {
				return signal[:start], rest[1 : 1+end]
			}
		}
	}
	return signal, ""
}

// ParseRunLog splits a run log written for an attempt back into the output
// tail and the progress events the claim_evidence state carries: the text
// between the stdout and stderr sections joined by a newline, and every
// BATUTA-PROGRESS line of both. Content with neither section is taken whole.
func ParseRunLog(content string) (tail string, progress []executor.ProgressEvent) {
	stdout, stderr := content, ""
	if _, rest, found := strings.Cut(content, "## stdout\n\n"); found {
		stdout, stderr, _ = strings.Cut(rest, "\n\n## stderr\n\n")
	}
	switch {
	case stdout == "" && stderr == "":
	case stderr == "":
		tail = stdout
	case stdout == "":
		tail = stderr
	default:
		tail = stdout + "\n" + stderr
	}
	for _, line := range strings.Split(tail, "\n") {
		if criterion, state, ok := executor.ParseProgress(line); ok {
			progress = append(progress, executor.ProgressEvent{Criterion: criterion, State: state})
		}
	}
	return tail, progress
}

// ReplayRunLogName is the run log file name the loop wrote for a recorded
// attempt: <date>-<slug>-<task>-e<n>.out.log under .batuta/runs. The date
// comes from the delivery identifier's stamp, falling back to the record
// time; the task id keeps its trail spelling (underscores as dashes).
func ReplayRunLogName(deliveryID, slug, taskID string, at time.Time, execution int) string {
	date := ""
	if parts := strings.Split(deliveryID, "-"); len(parts) >= 2 {
		if stamp := parts[len(parts)-2]; len(stamp) == 8 {
			date = stamp[:4] + "-" + stamp[4:6] + "-" + stamp[6:]
		}
	}
	if date == "" {
		date = at.UTC().Format("2006-01-02")
	}
	return date + "-" + slug + "-" + strings.ReplaceAll(taskID, "_", "-") + "-e" + strconv.Itoa(execution) + ".out.log"
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

func (r *Runner) judgeClaimEvidence(ctx context.Context, ac attemptContext, report *gates.Report, result executor.Result, treeChanged bool, changedPaths []string) (judgment, error) {
	if r.opts.Judge == nil {
		return judgment{}, nil
	}
	decision := r.opts.JudgeConfig.Decision(claimEvidenceDecision)
	if decision.Mode == judge.ModeOff {
		return judgment{}, nil
	}

	maxBytes := r.opts.JudgeConfig.MaxStateBytes
	if maxBytes <= 0 {
		maxBytes = 100000
	}
	state, err := BuildClaimEvidenceState(ClaimEvidenceInput{
		Workspace:    r.root,
		Task:         ac.plan,
		Criteria:     gates.ParseCriteria(ac.plan.Accept),
		Report:       *report,
		OutputTail:   claimEvidenceOutputTail(result),
		Progress:     result.Progress,
		ChangedPaths: changedPaths,
		TreeChanged:  treeChanged,
	}, maxBytes)
	if err != nil {
		if recErr := r.recordJudgeUnavailable(ac, "", nil, judge.ReasonStateTooLarge); recErr != nil {
			return judgment{}, recErr
		}
		return judgment{Asked: true, Unavailable: judge.ReasonStateTooLarge}, nil
	}

	var sinkErr error
	traced := &judge.Traced{
		Judge: r.opts.Judge,
		Sink: func(kind string, record any) error {
			err := r.locked(journal.Kind(kind), ac.taskID, record, nil)
			if err != nil && sinkErr == nil {
				sinkErr = err
			}
			return err
		},
	}
	resp, err := traced.Ask(ctx, judge.Request{
		Decision:  claimEvidenceDecision,
		State:     state,
		Questions: ClaimEvidenceQuestions(),
	})
	if sinkErr != nil {
		return judgment{Asked: true}, sinkErr
	}

	out := judgment{Asked: true, Answers: noulAnswers(resp.Answers)}
	if err != nil {
		var unavail *judge.UnavailableError
		if errors.As(err, &unavail) {
			out.Unavailable = unavail.Reason
			return out, nil
		}
		out.Unavailable = err.Error()
		return out, nil
	}

	if decision.Mode == judge.ModeEnforce && report.Passed {
		threshold := decision.Threshold
		if threshold == 0 {
			threshold = defaultClaimEvidenceThreshold
		}
		if key, probability, ok := claimEvidenceEnforced(out.Answers, threshold); ok {
			report.Passed = false
			report.Proofs = append(report.Proofs, gates.Verdict{
				Name:   "judge",
				Pass:   false,
				Signal: fmt.Sprintf("claim_evidence: %s %.2f (threshold %.2f)", key, probability, threshold),
				Detail: claimEvidenceContradiction(state),
			})
		}
	}
	return out, nil
}

func (r *Runner) recordJudgeUnavailable(ac attemptContext, digest string, keys []string, reason string) error {
	if keys == nil {
		keys = []string{claimUnsupportedKey, verifierContradictedKey}
	}
	intent := judge.IntentRecord{Decision: claimEvidenceDecision, QuestionKeys: keys, StateDigest: digest}
	if err := r.locked(KindJudgeIntent, ac.taskID, intent, nil); err != nil {
		return err
	}
	return r.locked(KindJudgeResult, ac.taskID, judge.ResultRecord{
		Decision:          claimEvidenceDecision,
		QuestionKeys:      keys,
		StateDigest:       digest,
		UnavailableReason: reason,
	}, nil)
}

func claimEvidenceOutputTail(result executor.Result) string {
	stdout := string(result.Stdout)
	stderr := string(result.Stderr)
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}

func noulAnswers(answers map[string]judge.Answer) map[string]float64 {
	out := make(map[string]float64, len(answers))
	for key, answer := range answers {
		out[key] = answer.Noul
	}
	return out
}

func claimEvidenceEnforced(answers map[string]float64, threshold float64) (string, float64, bool) {
	for _, key := range []string{claimUnsupportedKey, verifierContradictedKey} {
		if probability, ok := answers[key]; ok && probability >= threshold {
			return key, probability, true
		}
	}
	return "", 0, false
}

func claimEvidenceContradiction(state any) string {
	encoded, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	var parsed claimEvidenceState
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		return strings.TrimSpace(string(encoded))
	}
	var claims []string
	for _, line := range splitReportLines(parsed.ExecutorReport) {
		trimmed := strings.TrimSpace(line)
		if claimProgressLine.MatchString(trimmed) || claimTaskLine.MatchString(trimmed) {
			claims = append(claims, trimmed)
		}
	}
	if len(claims) == 0 {
		lines := splitReportLines(parsed.ExecutorReport)
		for i := len(lines) - 1; i >= 0 && len(claims) < 3; i-- {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed != "" {
				claims = append([]string{trimmed}, claims...)
			}
		}
	}
	return strings.Join(claims, "\n")
}
