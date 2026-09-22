package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

// judgeEnvVar selects another judge config file or turns the judge off;
// Config.Judge reads the API key from the env var named by key_env.
const judgeEnvVar = "BATUTA_JUDGE"

func runJudge(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("a judge form is required; available forms: ask, classify, corpus, probe, replay")
	}
	switch args[0] {
	case "ask":
		return runJudgeAsk(args[1:], stdout, stderr)
	case "classify":
		return runJudgeClassify(args[1:], stdout, stderr)
	case "corpus":
		return runJudgeCorpus(args[1:], stdout, stderr)
	case "probe":
		return runJudgeProbe(args[1:], stdout, stderr)
	case "replay":
		return runJudgeReplay(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown judge form %q; available forms: ask, classify, corpus, probe, replay", args[0])
	}
}

func runJudgeAsk(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge ask", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateFile := flags.String("state-file", "", "JSON file holding the bounded state")
	questionsFile := flags.String("questions-file", "", "JSON file with the questions map in the request shape")
	decision := flags.String("decision", "manual", "decision name carried into the trace records")
	configPath := flags.String("config", "", "judge config path (default: .batuta/judge.json under --workspace)")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	baseURL := flags.String("base-url", "", "override the configured provider base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *stateFile == "" || *questionsFile == "" {
		return errors.New("usage: batuta judge ask --state-file <path> --questions-file <path> [--decision <name>] [--config <path>] [--workspace <dir>] [--base-url <url>]")
	}

	state, err := readJSONFile(*stateFile)
	if err != nil {
		return fmt.Errorf("judge ask: state file: %w", err)
	}
	questionsPayload, err := readJSONFile(*questionsFile)
	if err != nil {
		return fmt.Errorf("judge ask: questions file: %w", err)
	}
	var questions map[string]judge.Question
	if err := json.Unmarshal(questionsPayload, &questions); err != nil {
		return fmt.Errorf("judge ask: questions file %s must be a JSON object in the request questions shape: %w", *questionsFile, err)
	}
	if questions == nil {
		return fmt.Errorf("judge ask: questions file %s must be a JSON object in the request questions shape", *questionsFile)
	}

	j, err := buildJudge(*configPath, *workspace, *baseURL)
	if err != nil {
		return judgeFailure(stderr, err)
	}
	response, err := j.Ask(context.Background(), judge.Request{Decision: *decision, State: json.RawMessage(state), Questions: questions})
	if err != nil {
		return judgeFailure(stderr, err)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(response)
}

func runJudgeProbe(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "judge config path (default: .batuta/judge.json under --workspace)")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	baseURL := flags.String("base-url", "", "override the configured provider base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: batuta judge probe [--config <path>] [--workspace <dir>] [--base-url <url>]")
	}

	config, err := loadJudgeConfig(*configPath, *workspace)
	if err != nil {
		return err
	}
	if *baseURL != "" {
		config.BaseURL = *baseURL
	}
	j, err := config.Judge(os.Getenv)
	if err != nil {
		return judgeFailure(stderr, err)
	}
	start := time.Now()
	response, err := j.Ask(context.Background(), judge.Request{
		Decision: "probe",
		State:    "connection check",
		Questions: map[string]judge.Question{
			"ok": {Type: judge.QuestionNoul, Instructions: "The state says the connection works."},
		},
	})
	if err != nil {
		return judgeFailure(stderr, err)
	}
	fmt.Fprintf(stdout, "provider=%s model=%s ms=%d tokens=%d/%d\n",
		judgeProviderName(j, config), response.Model, time.Since(start).Milliseconds(),
		response.Usage.InputTokens, response.Usage.OutputTokens)
	return nil
}

// loadJudgeConfig resolves the config: an explicit --config path wins over
// BATUTA_JUDGE, and the default stays .batuta/judge.json under the workspace.
func loadJudgeConfig(configPath, workspace string) (judge.Config, error) {
	root, err := workspaceRoot(workspace)
	if err != nil {
		return judge.Config{}, err
	}
	getenv := func(name string) string {
		if name == judgeEnvVar && configPath != "" {
			return configPath
		}
		return os.Getenv(name)
	}
	return judge.LoadConfig(root, getenv)
}

func buildJudge(configPath, workspace, baseURL string) (judge.Judge, error) {
	config, err := loadJudgeConfig(configPath, workspace)
	if err != nil {
		return nil, err
	}
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	return config.Judge(os.Getenv)
}

// judgeFailure separates the two failure classes: an unavailable judge is the
// fail-closed outcome on stderr with exit 2 and keeps today's deterministic
// rule, while a config error is a plain error and exits 1.
// readJournal decodes a delivery journal read-only: the file is opened for
// reading and decoded directly, never through the append paths of
// journal.Store.
func readJournal(path string) ([]journal.Record, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	return journal.Decode(handle)
}

type replayOpenedDetail struct {
	Slug  string              `json:"slug"`
	Tasks []replayTaskSummary `json:"tasks"`
}

type replayTaskSummary struct {
	ID    string `json:"task_id"`
	Title string `json:"title"`
}

type replayFinishedDetail struct {
	Execution   int    `json:"execution"`
	TreeChanged bool   `json:"tree_changed"`
	BaseHeadSHA string `json:"base_head_sha"`
}

type replayFailureDetail struct {
	Execution int    `json:"execution"`
	Blocker   string `json:"blocker"`
}

type replayExecutionDetail struct {
	Execution int `json:"execution"`
}

type replayCandidateDetail struct {
	Execution int    `json:"execution"`
	Commit    string `json:"commit"`
	Evidence  struct {
		BaseSHA string `json:"base_sha"`
	} `json:"evidence"`
}

type replaySnapshotDetail struct {
	Execution int    `json:"execution"`
	SHA       string `json:"sha"`
}

type replayAttemptKey struct {
	taskID    string
	execution int
}

type replayAttempt struct {
	taskID          string
	execution       int
	treeChanged     bool
	baseSHA         string
	candidateCommit string
	candidateBase   string
	snapshotSHA     string
	report          gates.Report
	outcome         string
	at              time.Time
}

// replayAttempts pairs every gates_reported record with its matching
// executor_finished record and the first outcome record that follows it.
// It also returns the delivery slug and the task titles from the
// delivery_opened record.
func replayAttempts(records []journal.Record) (attempts []replayAttempt, titles map[string]string, slug string) {
	type finishedKey struct {
		taskID    string
		execution int
	}
	finished := map[finishedKey]journal.Record{}
	outcomes := map[replayAttemptKey]string{}
	candidates := map[replayAttemptKey]replayCandidateDetail{}
	snapshots := map[replayAttemptKey]replaySnapshotDetail{}
	titles = map[string]string{}
	for _, record := range records {
		switch record.Kind {
		case loop.KindOpened:
			var detail replayOpenedDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				slug = detail.Slug
				for _, task := range detail.Tasks {
					titles[task.ID] = task.Title
				}
			}
		case loop.KindFinished:
			var detail replayFinishedDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				finished[finishedKey{record.TaskID, detail.Execution}] = record
			}
		case loop.KindCandidate:
			var detail replayCandidateDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				key := replayAttemptKey{record.TaskID, detail.Execution}
				if _, seen := outcomes[key]; !seen {
					outcomes[key] = "candidate"
				}
				candidates[key] = detail
			}
		case loop.KindSnapshot:
			var detail replaySnapshotDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				snapshots[replayAttemptKey{record.TaskID, detail.Execution}] = detail
			}
		case loop.KindQuestion:
			var detail replayExecutionDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				key := replayAttemptKey{record.TaskID, detail.Execution}
				if _, seen := outcomes[key]; !seen {
					outcomes[key] = "question"
				}
			}
		case loop.KindFailure:
			var detail replayFailureDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				key := replayAttemptKey{record.TaskID, detail.Execution}
				if _, seen := outcomes[key]; !seen {
					if detail.Blocker == "" {
						detail.Blocker = "failure"
					}
					outcomes[key] = detail.Blocker
				}
			}
		}
	}
	seen := map[replayAttemptKey]bool{}
	for _, record := range records {
		if record.Kind != loop.KindGates {
			continue
		}
		var report gates.Report
		if err := json.Unmarshal(record.Detail, &report); err != nil {
			continue
		}
		key := replayAttemptKey{record.TaskID, report.Execution}
		finishedRecord, ok := finished[finishedKey{record.TaskID, report.Execution}]
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		treeChanged := false
		baseSHA := ""
		var finishedDetail replayFinishedDetail
		if json.Unmarshal(finishedRecord.Detail, &finishedDetail) == nil {
			treeChanged = finishedDetail.TreeChanged
			baseSHA = finishedDetail.BaseHeadSHA
		}
		candidate := candidates[key]
		snapshot := snapshots[key]
		attempts = append(attempts, replayAttempt{
			taskID: record.TaskID, execution: report.Execution, treeChanged: treeChanged,
			baseSHA: baseSHA, candidateCommit: candidate.Commit, candidateBase: candidate.Evidence.BaseSHA,
			snapshotSHA: snapshot.SHA, report: report, outcome: outcomes[key], at: record.At,
		})
	}
	return attempts, titles, slug
}

// replayJSONTotals is the final --json object, mirroring the totals line.
type replayJSONTotals struct {
	Attempts     int `json:"attempts"`
	Asked        int `json:"asked"`
	Skipped      int `json:"skipped"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func runJudgeReplay(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge replay", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	journalPath := flags.String("journal", "", "delivery journal to replay (.batuta/journal/<delivery>.jsonl)")
	runs := flags.String("runs", "", "run-log directory (default: <workspace>/.batuta/runs)")
	configPath := flags.String("config", "", "judge config path (default: .batuta/judge.json under --workspace)")
	decision := flags.String("decision", "claim_evidence", "decision name asked and printed")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	baseURL := flags.String("base-url", "", "override the configured provider base URL")
	asJSON := flags.Bool("json", false, "print one JSON object per attempt with the per-claim list and a final totals object")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *journalPath == "" {
		return errors.New("usage: batuta judge replay --journal <path> [--runs <dir>] [--config <path>] [--decision <name>] [--json] [--workspace <dir>] [--base-url <url>]")
	}

	root, err := workspaceRoot(*workspace)
	if err != nil {
		return err
	}
	records, err := readJournal(*journalPath)
	if err != nil {
		return fmt.Errorf("judge replay: %s: %w", *journalPath, err)
	}
	config, err := loadJudgeConfig(*configPath, *workspace)
	if err != nil {
		return err
	}
	if *baseURL != "" {
		config.BaseURL = *baseURL
	}
	j, err := config.Judge(os.Getenv)
	if err != nil {
		return judgeFailure(stderr, err)
	}
	provider := judgeProviderName(j, config)

	attempts, titles, slug := replayAttempts(records)
	delivery := strings.TrimSuffix(filepath.Base(*journalPath), ".jsonl")
	threshold := config.Decision(*decision).Threshold
	if threshold == 0 {
		threshold = replayDefaultThreshold
	}
	maxBytes := config.MaxStateBytes
	if maxBytes <= 0 {
		maxBytes = 100000
	}
	answered, attemptCount, skipped := 0, 0, 0
	var inputTokens, outputTokens int
	encoder := json.NewEncoder(stdout)
	for _, attempt := range attempts {
		attemptCount++
		logPath := attempt.runLogPath(root, *runs, delivery, slug)
		log, err := os.ReadFile(logPath)
		if err != nil {
			skipped++
			if *asJSON {
				if err := encoder.Encode(replayJSONRecord{TaskID: attempt.taskID, Execution: attempt.execution, Skipped: logPath}); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(stdout, "%s e%d skipped %s\n", attempt.taskID, attempt.execution, logPath)
			}
			continue
		}
		paths, known := resolveReplayChangedPaths(context.Background(), root, attempt)
		input := replayEvidenceInput(context.Background(), root, attempt, titles[attempt.taskID], string(log), paths)
		state, err := loop.BuildClaimEvidenceState(input, maxBytes)
		if err != nil {
			return fmt.Errorf("judge replay: %s e%d: %w", attempt.taskID, attempt.execution, err)
		}
		request, claims, err := replayClaimEvidenceRequest(input, state, known)
		if err != nil {
			return fmt.Errorf("judge replay: %s e%d: %w", attempt.taskID, attempt.execution, err)
		}
		asked := len(request.Questions) > 0
		var answers map[string]judge.Answer
		unavailable := ""
		var model string
		var attemptInput, attemptOutput int
		var latencyMS *int64
		if asked {
			start := time.Now()
			response, err := j.Ask(context.Background(), request)
			elapsed := time.Since(start).Milliseconds()
			latencyMS = &elapsed
			if err != nil {
				if answered == 0 {
					return judgeFailure(stderr, err)
				}
				unavailable = judgeReplayReason(err)
			} else {
				answered++
				provider = judgeProviderName(j, config)
				answers = response.Answers
				model = response.Model
				attemptInput, attemptOutput = response.Usage.InputTokens, response.Usage.OutputTokens
				inputTokens += attemptInput
				outputTokens += attemptOutput
			}
		}
		judgment := aggregateReplayClaims(claims, answers, threshold)
		if *asJSON {
			record := replayJSONRecord{
				TaskID: attempt.taskID, Execution: attempt.execution, Outcome: attempt.outcome,
				Unavailable: unavailable, Asked: asked, Flagged: judgment.Flagged,
				MaxContradicted: judgment.MaxContradicted, MaterialMax: judgment.MaterialMax,
				Provider: provider, Model: model, InputTokens: attemptInput, OutputTokens: attemptOutput,
				LatencyMS: latencyMS, Claims: judgment.Claims, Uncertain: judgment.Uncertain,
			}
			if err := encoder.Encode(record); err != nil {
				return err
			}
			continue
		}
		line := fmt.Sprintf("%s e%d outcome=%s asked=%t claims=%d changed_paths=%s code_contradicted=%d judge_contradicted=%d uncertain=%d max_contradicted=%.2f material_max=%.2f flagged=%t",
			attempt.taskID, attempt.execution, attempt.outcome, asked,
			len(judgment.Claims), replayChangedPathsCount(paths, known), judgment.CodeContradicted, judgment.JudgeContradicted,
			len(judgment.Uncertain), judgment.MaxContradicted, judgment.MaterialMax, judgment.Flagged)
		if unavailable != "" {
			line += " unavailable=" + unavailable
		}
		line += fmt.Sprintf(" provider=%s", provider)
		if asked && unavailable == "" {
			line += fmt.Sprintf(" tokens=%d/%d ms=%d", attemptInput, attemptOutput, *latencyMS)
		}
		fmt.Fprintln(stdout, line)
	}
	if *asJSON {
		if err := encoder.Encode(struct {
			Totals replayJSONTotals `json:"totals"`
		}{replayJSONTotals{
			Attempts: attemptCount, Asked: answered, Skipped: skipped,
			InputTokens: inputTokens, OutputTokens: outputTokens,
		}}); err != nil {
			return err
		}
		return nil
	}
	fmt.Fprintf(stdout, "attempts=%d asked=%d skipped=%d input_tokens=%d output_tokens=%d\n",
		attemptCount, answered, skipped, inputTokens, outputTokens)
	return nil
}

// replayClaimEvidenceRequest mirrors the loop's live claim_evidence build:
// code extracts the atomic claims from the bounded, redacted executor report
// the v1 state builder produces, settles what it can settle exactly against
// the redacted changed paths, the proof verdicts, the verifier lines and the
// tests gate, and asks one choice question per unsettled claim.
func replayClaimEvidenceRequest(input loop.ClaimEvidenceInput, state any, pathsKnown bool) (judge.Request, []loop.Claim, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return judge.Request{}, nil, err
	}
	var bounded replayBoundedState
	if err := json.Unmarshal(payload, &bounded); err != nil {
		return judge.Request{}, nil, err
	}
	evidence := loop.ClaimEvidence{
		ChangedPaths: bounded.Tree.ChangedPaths,
		TreeChanged:  input.TreeChanged,
		Proofs:       input.Report.Proofs,
		TestsPass:    input.Report.Tests.Pass,
		Diff:         input.Diff,
	}
	if input.Report.Verifier != nil {
		evidence.VerifierLines = loop.ParseVerifierLines(input.Report.Verifier.Detail)
	}
	claims := loop.SettleClaims(loop.ExtractClaims(bounded.ExecutorReport, input.Criteria), evidence)
	if !pathsKnown {
		for i := range claims {
			if claims[i].Kind != loop.ClaimKindPath {
				continue
			}
			claims[i].Status = loop.ClaimStatus(replayChoiceUnverifiable)
			claims[i].Source = loop.ClaimSourceCode
			claims[i].Evidence = "changed_paths unknown"
		}
	}
	return loop.BuildClaimEvidenceRequest(input, claims), claims, nil
}

// replayBoundedState is the part of the v1 state replay reuses for claim
// extraction: the executor report as the loop bounds and redacts it, and the
// changed paths as it redacts them.
type replayBoundedState struct {
	ExecutorReport string `json:"executor_report"`
	Tree           struct {
		ChangedPaths []string `json:"changed_paths"`
	} `json:"tree"`
}

// replayEvidenceInput reconstructs the bounded claim_evidence input of a
// recorded attempt from its journal report and run log. The plan file is not
// part of the journal: the criteria come from the recorded proof signals, and
// the progress events carry no timestamps.
func replayEvidenceInput(ctx context.Context, workspace string, attempt replayAttempt, title, runLog string, changedPaths []string) loop.ClaimEvidenceInput {
	tail, progress := loop.ParseRunLog(runLog)
	return loop.ClaimEvidenceInput{
		Workspace:    workspace,
		Task:         routing.PlanTask{TaskArtifact: routing.TaskArtifact{ID: attempt.taskID, Title: title}},
		Criteria:     loop.CriteriaFromProofs(attempt.report.Proofs),
		Report:       attempt.report,
		OutputTail:   tail,
		Progress:     progress,
		ChangedPaths: changedPaths,
		TreeChanged:  attempt.treeChanged,
		Diff:         replayAttemptDiff(ctx, workspace, attempt),
	}
}

// replayAttemptDiff resolves the unified diff of a recorded attempt in the
// same order resolveReplayChangedPaths walks for its paths: the candidate's
// recorded base and commit first, then the attempt base and the worktree
// snapshot. Without either pair there is no diff.
func replayAttemptDiff(ctx context.Context, workspace string, attempt replayAttempt) string {
	if diff, ok := replayGitDiff(ctx, workspace, attempt.candidateBase, attempt.candidateCommit); ok {
		return diff
	}
	if diff, ok := replayGitDiff(ctx, workspace, attempt.baseSHA, attempt.snapshotSHA); ok {
		return diff
	}
	return ""
}

func replayGitDiff(ctx context.Context, workspace, base, commit string) (string, bool) {
	if base == "" || commit == "" {
		return "", false
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return "", false
	}
	result, err := publication.ExecRunner{}.Run(ctx, publication.Command{
		Executable: git,
		Args:       []string{"diff", "--no-color", base, commit},
		Directory:  workspace,
	})
	if err != nil || result.ExitCode != 0 || result.StdoutTruncated {
		return "", false
	}
	return string(result.Stdout), true
}

func resolveReplayChangedPaths(ctx context.Context, workspace string, attempt replayAttempt) ([]string, bool) {
	if len(attempt.report.Scope.Paths) > 0 {
		return attempt.report.Scope.Paths, true
	}
	if paths, ok := replayGitNameOnly(ctx, workspace, attempt.candidateBase, attempt.candidateCommit); ok {
		return paths, true
	}
	if paths, ok := replayGitNameOnly(ctx, workspace, attempt.baseSHA, attempt.snapshotSHA); ok {
		return paths, true
	}
	if !attempt.treeChanged {
		return []string{}, true
	}
	return nil, false
}

func replayGitNameOnly(ctx context.Context, workspace, base, commit string) ([]string, bool) {
	if base == "" || commit == "" {
		return nil, false
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, false
	}
	result, err := publication.ExecRunner{}.Run(ctx, publication.Command{
		Executable: git,
		Args:       []string{"diff", "--name-only", base, commit},
		Directory:  workspace,
	})
	if err != nil {
		return nil, false
	}
	return parseGitNameOnly(result.Stdout), true
}

func parseGitNameOnly(stdout []byte) []string {
	var paths []string
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		paths = append(paths, filepath.ToSlash(line))
	}
	return paths
}

func replayChangedPathsCount(paths []string, known bool) string {
	if !known {
		return "unknown"
	}
	return strconv.Itoa(len(paths))
}

const (
	replayDefaultThreshold   = 0.9
	replayChoiceSupported    = "supported"
	replayChoiceContradicted = "contradicted"
	replayChoiceUnverifiable = "unverifiable"
	replayUncertainLow       = 0.30
	replayUncertainHigh      = 0.70
)

// replayClaim is one settled claim as replay reports it: who settled it
// (code or judge), the choice, the confidence and the material probability.
type replayClaim struct {
	Kind       string  `json:"kind"`
	Text       string  `json:"text"`
	Line       string  `json:"line,omitempty"`
	Source     string  `json:"source"`
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
	Material   float64 `json:"material"`
}

// replayUncertain is one judge answer routed to the uncertain bucket because
// its confidence is below the threshold or its contradicted probability lies
// in the 0.30–0.70 band.
type replayUncertain struct {
	Key          string  `json:"key"`
	Choice       string  `json:"choice"`
	Confidence   float64 `json:"confidence"`
	Contradicted float64 `json:"contradicted"`
}

// replayJudgment is the aggregated view of one attempt's claims: the flag,
// the per-claim records, the uncertain bucket and the breakdown counts the
// replay line reports.
type replayJudgment struct {
	Flagged           bool
	Claims            []replayClaim
	Uncertain         []replayUncertain
	CodeContradicted  int
	JudgeContradicted int
	MaxContradicted   float64
	MaterialMax       float64
}

// aggregateReplayClaims applies the loop's aggregation to the settled claims
// and the judge's answers — the same rules live and replay report: a
// code-settled contradiction flags on its own; a judge contradiction flags
// only when confidence and material are both at or above the threshold;
// answers below the threshold or whose contradicted probability lies in
// 0.30–0.70 land in the uncertain bucket; MaxContradicted is the highest
// contradicted probability among the judge's answers and MaterialMax is
// the highest material probability among the judged claims.
func aggregateReplayClaims(claims []loop.Claim, answers map[string]judge.Answer, threshold float64) replayJudgment {
	judgment := replayJudgment{Claims: make([]replayClaim, 0, len(claims)), Uncertain: []replayUncertain{}}
	for index, claim := range claims {
		key := fmt.Sprintf("c%d_relation", index+1)
		materialKey := fmt.Sprintf("c%d_material", index+1)
		record := replayClaim{Kind: string(claim.Kind), Text: claim.Text, Line: claim.Line}
		answer, asked := answers[key]
		material, askedMaterial := answers[materialKey]
		if asked && claim.Status == loop.ClaimStatusUnsettled {
			record.Source = string(loop.ClaimSourceJudge)
			record.Choice = replayMappedChoice(answer.Choice)
			record.Confidence = answer.Confidence
			if askedMaterial {
				record.Material = material.Noul
			}
			if replayAnswerUncertain(answer, threshold) {
				judgment.Uncertain = append(judgment.Uncertain, replayUncertain{
					Key:          key,
					Choice:       answer.Choice,
					Confidence:   answer.Confidence,
					Contradicted: replayContradictedProbability(answer),
				})
			} else if replayDefectChoice(answer.Choice) && answer.Confidence >= threshold && askedMaterial && material.Noul >= threshold {
				judgment.JudgeContradicted++
				judgment.Flagged = true
			}
		} else {
			record.Source = string(claim.Source)
			record.Choice = string(claim.Status)
			if claim.Source == loop.ClaimSourceCode {
				record.Confidence = 1
				if claim.Status == loop.ClaimStatusContradicted {
					judgment.CodeContradicted++
					judgment.Flagged = true
				}
			}
		}
		judgment.Claims = append(judgment.Claims, record)
		if record.Material > judgment.MaterialMax {
			judgment.MaterialMax = record.Material
		}
	}
	for _, answer := range answers {
		if probability := replayContradictedProbability(answer); probability > judgment.MaxContradicted {
			judgment.MaxContradicted = probability
		}
	}
	return judgment
}

func replayMappedChoice(choice string) string {
	switch choice {
	case replayChoiceSupported, replayChoiceUnverifiable:
		return choice
	}
	if replayDefectChoice(choice) {
		return replayChoiceContradicted
	}
	return choice
}

func replayDefectChoice(choice string) bool {
	switch choice {
	case replayChoiceContradicted, "path_not_changed", "proof_failed", "verifier_incomplete", "tests_gate_failed", "count_mismatch",
		"fabricated_reference", "wrong_count", "behaviour_absent":
		return true
	}
	return false
}

func replayContradictedProbability(answer judge.Answer) float64 {
	var sum float64
	for option, probability := range answer.Probabilities {
		if replayDefectChoice(option) {
			sum += probability
		}
	}
	return sum
}

// replayAnswerUncertain mirrors the loop's uncertain rule: an answer is
// uncertain when its confidence is below the threshold or its contradicted
// probability lies in the 0.30–0.70 band.
func replayAnswerUncertain(answer judge.Answer, threshold float64) bool {
	if answer.Confidence < threshold {
		return true
	}
	probability := replayContradictedProbability(answer)
	return probability >= replayUncertainLow && probability <= replayUncertainHigh
}

// replayJSONRecord is one attempt of a --json replay: the per-claim list and
// the uncertain bucket next to the fields the text line reports. A skipped
// attempt carries only the identifier and the expected log path.
type replayJSONRecord struct {
	TaskID          string            `json:"task_id"`
	Execution       int               `json:"execution"`
	Outcome         string            `json:"outcome,omitempty"`
	Skipped         string            `json:"skipped,omitempty"`
	Unavailable     string            `json:"unavailable,omitempty"`
	Asked           bool              `json:"asked"`
	Flagged         bool              `json:"flagged"`
	MaxContradicted float64           `json:"max_contradicted"`
	MaterialMax     float64           `json:"material_max"`
	Provider        string            `json:"provider,omitempty"`
	Model           string            `json:"model,omitempty"`
	InputTokens     int               `json:"input_tokens,omitempty"`
	OutputTokens    int               `json:"output_tokens,omitempty"`
	LatencyMS       *int64            `json:"latency_ms,omitempty"`
	Claims          []replayClaim     `json:"claims"`
	Uncertain       []replayUncertain `json:"uncertain"`
}

// judgeProviderName reports the provider that answered the last ask; a
// single-provider judge falls back to the configured provider.
func judgeProviderName(j judge.Judge, config judge.Config) string {
	if named, ok := j.(interface {
		LastProvider() judge.Provider
	}); ok {
		if name := named.LastProvider(); name != "" {
			return string(name)
		}
	}
	return string(config.Provider)
}

// judgeReplayReason keeps the typed unavailability reason when there is one.
func judgeReplayReason(err error) string {
	var unavailable *judge.UnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.Reason
	}
	return err.Error()
}

func (a replayAttempt) runLogPath(workspace, runs, delivery, slug string) string {
	if runs == "" {
		runs = filepath.Join(workspace, ".batuta", "runs")
	} else if !filepath.IsAbs(runs) {
		runs = filepath.Join(workspace, runs)
	}
	return filepath.Join(runs, loop.ReplayRunLogName(delivery, slug, a.taskID, a.at, a.execution))
}

func judgeFailure(stderr io.Writer, err error) error {
	var unavailable *judge.UnavailableError
	if !errors.As(err, &unavailable) {
		return err
	}
	fmt.Fprintln(stderr, "judge:", err)
	return &ExitError{Code: 2, State: "unavailable"}
}

func readJSONFile(path string) (json.RawMessage, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("%s is not valid JSON", path)
	}
	return json.RawMessage(payload), nil
}
