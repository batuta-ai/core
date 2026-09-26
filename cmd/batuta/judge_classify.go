package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/batuta-ai/core/classify"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

// classifyDecisionName is the decision point whose threshold the classify
// run reads; classifyDefaultThreshold holds when the config names none.
const (
	classifyDecisionName     = "classify"
	classifyDefaultThreshold = 0.7
)

// classifyRecord is one task of a classify run: the plan's lane, the judge's
// lane with both confidences and the fallback flag, the per-task input
// tokens and the unavailability reason when the judge never answered. An
// unavailable task names no lane. The bench form adds the plan slug and, per
// task, the recorded first-attempt outcome and how the judge lane relates to
// the executed one; the plain classify form leaves them unset.
type classifyRecord struct {
	Task                 string  `json:"task"`
	PlanSlug             string  `json:"plan_slug,omitempty"`
	PlanComplexity       string  `json:"plan_complexity"`
	PlanDomain           string  `json:"plan_domain"`
	Complexity           string  `json:"complexity,omitempty"`
	Domain               string  `json:"domain,omitempty"`
	ComplexityConfidence float64 `json:"complexity_confidence,omitempty"`
	DomainConfidence     float64 `json:"domain_confidence,omitempty"`
	Fallback             bool    `json:"fallback"`
	Threshold            float64 `json:"threshold"`
	InputTokens          *int    `json:"input_tokens,omitempty"`
	Unavailable          string  `json:"unavailable,omitempty"`
	Outcome              string  `json:"outcome,omitempty"`
	Relation             string  `json:"relation,omitempty"`
}

func (r classifyRecord) text() string {
	prefix := ""
	if r.PlanSlug != "" {
		prefix = r.PlanSlug + " "
	}
	line := fmt.Sprintf("%s%s plan=%s/%s", prefix, r.Task, r.PlanDomain, r.PlanComplexity)
	if r.Unavailable != "" {
		line += " unavailable=" + r.Unavailable
	} else {
		tokens := "unknown"
		if r.InputTokens != nil {
			tokens = strconv.Itoa(*r.InputTokens)
		}
		line = fmt.Sprintf("%s judge=%s/%s complexity=%.2f domain=%.2f fallback=%t input_tokens=%s",
			line, r.Domain, r.Complexity, r.ComplexityConfidence, r.DomainConfidence, r.Fallback, tokens)
	}
	if r.Outcome != "" {
		line += fmt.Sprintf(" outcome=%s relation=%s", r.Outcome, r.Relation)
	}
	return line
}

func runJudgeClassify(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "bench" {
		return runJudgeClassifyBench(args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("judge classify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	planPath := flags.String("plan", "", "plan file whose tasks are classified")
	configPath := flags.String("config", "", "judge config path (default: .batuta/judge.json under --workspace)")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	baseURL := flags.String("base-url", "", "override the configured provider base URL")
	asJSON := flags.Bool("json", false, "print one JSON object per task")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *planPath == "" {
		return errors.New("usage: batuta judge classify --plan <file> [--json] [--config <path>] [--workspace <dir>] [--base-url <url>]")
	}

	payload, err := os.ReadFile(*planPath)
	if err != nil {
		return fmt.Errorf("judge classify: %s: %w", *planPath, err)
	}
	slug := strings.TrimSuffix(filepath.Base(*planPath), ".md")
	plan, err := routing.ParsePlan(slug, payload)
	if err != nil {
		return fmt.Errorf("judge classify: %s: %w", *planPath, err)
	}
	j, buildReason, threshold, err := classifyJudge(*configPath, *workspace, *baseURL)
	if err != nil {
		return err
	}
	if !*asJSON {
		fmt.Fprintf(stdout, "threshold=%s\n", corpusThresholdLabel(threshold))
	}
	return classifyPlanTasks(context.Background(), stdout, j, buildReason, plan, threshold, *asJSON)
}

// classifyJudge builds the classify judge and its threshold: the classify
// decision's threshold from the config, classifyDefaultThreshold when unset.
// The judge stays nil with a typed reason when it is unavailable.
func classifyJudge(configPath, workspace, baseURL string) (judge.Judge, string, float64, error) {
	config, err := loadJudgeConfig(configPath, workspace)
	if err != nil {
		return nil, "", 0, err
	}
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	j, buildReason, err := corpusRunJudge(config)
	if err != nil {
		return nil, "", 0, err
	}
	threshold := config.Decision(classifyDecisionName).Threshold
	if threshold <= 0 {
		threshold = classifyDefaultThreshold
	}
	return j, buildReason, threshold, nil
}

// classifyPlanTasks asks one classify request per task, sequential, and
// prints one record per task. An unavailable judge — never built, or failed
// on that task — reports unavailable and no lane.
func classifyPlanTasks(ctx context.Context, stdout io.Writer, j judge.Judge, buildReason string, plan routing.Plan, threshold float64, asJSON bool) error {
	encoder := json.NewEncoder(stdout)
	for _, task := range plan.Tasks {
		record := classifyRecordFor(ctx, j, buildReason, task, plan.ContextFor(task.Number), threshold)
		if asJSON {
			if err := encoder.Encode(record); err != nil {
				return err
			}
			continue
		}
		fmt.Fprintln(stdout, record.text())
	}
	return nil
}

// classifyRecordFor asks one classify request for a task and maps the
// answers onto a record. An unavailable judge — never built, or failed on
// that task — reports unavailable and no lane.
func classifyRecordFor(ctx context.Context, j judge.Judge, buildReason string, task routing.PlanTask, context string, threshold float64) classifyRecord {
	record := classifyRecord{
		Task:           task.ID,
		PlanComplexity: string(task.Complexity),
		PlanDomain:     string(task.Domain),
		Threshold:      threshold,
	}
	switch {
	case j == nil:
		record.Unavailable = buildReason
	default:
		response, err := j.Ask(ctx, classify.BuildRequest(task, context))
		if err != nil {
			record.Unavailable = judgeReplayReason(err)
		} else {
			decision := classify.Decide(response.Answers, threshold)
			record.Complexity = string(decision.Complexity)
			record.Domain = string(decision.Domain)
			record.ComplexityConfidence = decision.ComplexityConfidence
			record.DomainConfidence = decision.DomainConfidence
			record.Fallback = decision.Fallback
			if response.Usage.InputTokens != 0 {
				tokens := response.Usage.InputTokens
				record.InputTokens = &tokens
			}
		}
	}
	return record
}

// Bench — `batuta judge classify bench` scores the classify decision
// against the lanes of every task of every plan and, where a delivery
// journal exists, beside what the lane actually did. The judge only
// proposes; the host's labels stay the gate.

const (
	benchOutcomeCandidate = "candidate"
	benchOutcomeRetried   = "retried"
	benchOutcomeEscalated = "escalated"
	benchOutcomeFailed    = "failed"
	benchOutcomeUnknown   = "unknown"

	benchRelationLower   = "lower"
	benchRelationEqual   = "equal"
	benchRelationHigher  = "higher"
	benchRelationUnknown = "unknown"
)

var (
	benchOutcomeLabels    = []string{benchOutcomeCandidate, benchOutcomeRetried, benchOutcomeEscalated, benchOutcomeFailed, benchOutcomeUnknown}
	benchComplexityLabels = []string{
		string(routing.ComplexityLow), string(routing.ComplexityMedium),
		string(routing.ComplexityHigh), string(routing.ComplexityCritical),
	}
	benchComplexityRank = map[routing.Complexity]int{
		routing.ComplexityLow:      0,
		routing.ComplexityMedium:   1,
		routing.ComplexityHigh:     2,
		routing.ComplexityCritical: 3,
	}
)

// multiFlag collects a flag given more than once.
type multiFlag []string

func (f *multiFlag) String() string { return strings.Join(*f, ",") }

func (f *multiFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func runJudgeClassifyBench(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge classify bench", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var planPaths, journalDirs multiFlag
	flags.Var(&planPaths, "plan", "plan file whose tasks are classified (repeatable)")
	flags.Var(&journalDirs, "journals", "directory whose delivery journals (*.jsonl, found recursively) are scored (repeatable)")
	configPath := flags.String("config", "", "judge config path (default: .batuta/judge.json under --workspace)")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	baseURL := flags.String("base-url", "", "override the configured provider base URL")
	rubric := flags.String("rubric", "v1", "classification rubric (v1 or v2)")
	asJSON := flags.Bool("json", false, "print one JSON object per task plus a summary object")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || len(planPaths) == 0 || (*rubric != "v1" && *rubric != "v2") {
		return errors.New("usage: batuta judge classify bench --plan <file> [--plan <file>...] [--journals <dir>...] [--rubric v1|v2] [--json] [--config <path>] [--workspace <dir>] [--base-url <url>]")
	}
	plans := make([]routing.Plan, 0, len(planPaths))
	for _, path := range planPaths {
		payload, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("judge classify bench: %s: %w", path, err)
		}
		plan, err := routing.ParsePlan(strings.TrimSuffix(filepath.Base(path), ".md"), payload)
		if err != nil {
			return fmt.Errorf("judge classify bench: %s: %w", path, err)
		}
		plans = append(plans, plan)
	}
	deliveries, err := readBenchJournals(journalDirs)
	if err != nil {
		return err
	}
	j, buildReason, threshold, err := classifyJudge(*configPath, *workspace, *baseURL)
	if err != nil {
		return err
	}
	if *rubric == "v2" {
		return classifyBenchTasksV2(context.Background(), stdout, j, buildReason, plans, deliveries, *asJSON)
	}
	if !*asJSON {
		fmt.Fprintf(stdout, "threshold=%s\n", corpusThresholdLabel(threshold))
	}
	return classifyBenchTasks(context.Background(), stdout, j, buildReason, plans, deliveries, threshold, *asJSON)
}

// benchDelivery is one delivery journal read for the bench: its name decides
// which plan it belongs to, its records carry the attempts.
type benchDelivery struct {
	name    string
	records []journal.Record
}

// readBenchJournals reads every delivery journal under the given
// directories, walking each one so both a bare journal directory and a
// workspace root (`<root>/.batuta/journal/`) are covered, lexical order.
func readBenchJournals(dirs []string) ([]benchDelivery, error) {
	var deliveries []benchDelivery
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			records, err := readJournal(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			deliveries = append(deliveries, benchDelivery{name: strings.TrimSuffix(entry.Name(), ".jsonl"), records: records})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("judge classify bench: %s: %w", dir, err)
		}
	}
	return deliveries, nil
}

// benchDeliveryFor finds the first delivery whose name starts with the plan
// slug followed by "-" and whose records carry the task id.
func benchDeliveryFor(deliveries []benchDelivery, slug, taskID string) *benchDelivery {
	for i := range deliveries {
		delivery := &deliveries[i]
		if !strings.HasPrefix(delivery.name, slug+"-") {
			continue
		}
		for _, record := range delivery.records {
			if record.TaskID == taskID {
				return delivery
			}
		}
	}
	return nil
}

type benchAttemptDetail struct {
	Execution int    `json:"execution"`
	Executor  string `json:"executor"`
}

// benchOutcome derives the recorded outcome of a task's first finished
// attempt: candidate when that attempt was recorded as one, escalated when a
// later attempt ran on a different executor, retried when it ran on the same
// one, failed when no later attempt ran, unknown when the delivery holds no
// finished attempt for the task.
func benchOutcome(records []journal.Record, taskID string) string {
	executors := map[int]string{}
	candidates := map[int]bool{}
	finished := []int{}
	for _, record := range records {
		if record.TaskID != taskID {
			continue
		}
		var detail benchAttemptDetail
		switch record.Kind {
		case loop.KindStarted:
			if json.Unmarshal(record.Detail, &detail) == nil {
				executors[detail.Execution] = detail.Executor
			}
		case loop.KindFinished:
			if json.Unmarshal(record.Detail, &detail) == nil {
				finished = append(finished, detail.Execution)
			}
		case loop.KindCandidate:
			if json.Unmarshal(record.Detail, &detail) == nil {
				candidates[detail.Execution] = true
			}
		}
	}
	if len(finished) == 0 {
		return benchOutcomeUnknown
	}
	first := finished[0]
	if candidates[first] {
		return benchOutcomeCandidate
	}
	if len(finished) == 1 {
		return benchOutcomeFailed
	}
	for _, attempt := range finished[1:] {
		if executor := executors[attempt]; executor != "" && executor != executors[first] {
			return benchOutcomeEscalated
		}
	}
	return benchOutcomeRetried
}

// classifyBenchTasks classifies every task of every plan — one classify
// request per task, sequential — prints one record per task and folds them
// into the summary. A task's outcome comes from the first matching delivery
// journal; without one the outcome stays unknown and the task is never
// dropped.
func classifyBenchTasks(ctx context.Context, stdout io.Writer, j judge.Judge, buildReason string, plans []routing.Plan, deliveries []benchDelivery, threshold float64, asJSON bool) error {
	encoder := json.NewEncoder(stdout)
	counts := newBenchCounts()
	for _, plan := range plans {
		for _, task := range plan.Tasks {
			record := classifyRecordFor(ctx, j, buildReason, task, plan.ContextFor(task.Number), threshold)
			record.PlanSlug = plan.Slug
			record.Outcome = benchOutcomeUnknown
			if delivery := benchDeliveryFor(deliveries, plan.Slug, task.ID); delivery != nil {
				record.Outcome = benchOutcome(delivery.records, task.ID)
			}
			switch {
			case record.Unavailable != "":
				record.Relation = benchRelationUnknown
			case record.Complexity == string(task.Complexity):
				record.Relation = benchRelationEqual
			case benchComplexityRank[routing.Complexity(record.Complexity)] < benchComplexityRank[task.Complexity]:
				record.Relation = benchRelationLower
			default:
				record.Relation = benchRelationHigher
			}
			counts.add(record)
			if asJSON {
				if err := encoder.Encode(record); err != nil {
					return err
				}
				continue
			}
			fmt.Fprintln(stdout, record.text())
		}
	}
	if asJSON {
		return printBenchSummaryJSON(encoder, threshold, counts)
	}
	return printBenchSummaryText(stdout, counts)
}

// benchCounts folds the per-task bench records into the summary: agreement
// with the plan labels, the complexity confusion matrix, the outcome counts
// per relation and the constant-answer baseline.
type benchCounts struct {
	tasks           int
	complexityExact int
	domainExact     int
	under           int
	over            int
	fallbacks       int
	unavailable     int
	labels          map[string]int
	matrix          map[string]map[string]int
	outcomes        map[string]map[string]int
}

func newBenchCounts() *benchCounts {
	counts := &benchCounts{
		labels:   map[string]int{},
		matrix:   map[string]map[string]int{},
		outcomes: map[string]map[string]int{},
	}
	for _, label := range benchComplexityLabels {
		counts.matrix[label] = map[string]int{}
		for _, judge := range benchComplexityLabels {
			counts.matrix[label][judge] = 0
		}
	}
	for _, relation := range []string{benchRelationLower, benchRelationEqual, benchRelationHigher} {
		counts.outcomes[relation] = map[string]int{}
		for _, outcome := range benchOutcomeLabels {
			counts.outcomes[relation][outcome] = 0
		}
	}
	return counts
}

func (c *benchCounts) add(record classifyRecord) {
	c.tasks++
	c.labels[record.PlanComplexity]++
	if record.Unavailable != "" {
		c.unavailable++
		return
	}
	if record.Fallback {
		c.fallbacks++
	}
	if record.Complexity == record.PlanComplexity {
		c.complexityExact++
	}
	if record.Domain == record.PlanDomain {
		c.domainExact++
	}
	switch record.Relation {
	case benchRelationLower:
		c.under++
	case benchRelationHigher:
		c.over++
	}
	c.matrix[record.PlanComplexity][record.Complexity]++
	c.outcomes[record.Relation][record.Outcome]++
}

// baseline is the constant-answer score: the share of the most common plan
// label, first in lane order on a tie.
func (c *benchCounts) baseline() (string, int, int) {
	label, count := "", 0
	for _, candidate := range benchComplexityLabels {
		if c.labels[candidate] > count {
			label, count = candidate, c.labels[candidate]
		}
	}
	return label, count, c.tasks
}

func printBenchSummaryText(stdout io.Writer, counts *benchCounts) error {
	_, count, total := counts.baseline()
	if _, err := fmt.Fprintf(stdout, "bench tasks=%d complexity_exact=%d/%d domain_exact=%d/%d under=%d over=%d fallbacks=%d unavailable=%d baseline=%d/%d\n",
		counts.tasks, counts.complexityExact, counts.tasks, counts.domainExact, counts.tasks,
		counts.under, counts.over, counts.fallbacks, counts.unavailable, count, total); err != nil {
		return err
	}
	for _, plan := range benchComplexityLabels {
		cells := make([]string, 0, len(benchComplexityLabels))
		for _, judge := range benchComplexityLabels {
			cells = append(cells, fmt.Sprintf("%s=%d", judge, counts.matrix[plan][judge]))
		}
		if _, err := fmt.Fprintf(stdout, "matrix plan=%s judge: %s\n", plan, strings.Join(cells, " ")); err != nil {
			return err
		}
	}
	for _, relation := range []string{benchRelationLower, benchRelationEqual, benchRelationHigher} {
		cells := make([]string, 0, len(benchOutcomeLabels))
		for _, outcome := range benchOutcomeLabels {
			cells = append(cells, fmt.Sprintf("%s=%d", outcome, counts.outcomes[relation][outcome]))
		}
		if _, err := fmt.Fprintf(stdout, "outcomes relation=%s: %s\n", relation, strings.Join(cells, " ")); err != nil {
			return err
		}
	}
	return nil
}

type benchSummaryJSON struct {
	Threshold       float64                   `json:"threshold"`
	Tasks           int                       `json:"tasks"`
	ComplexityExact int                       `json:"complexity_exact"`
	DomainExact     int                       `json:"domain_exact"`
	Under           int                       `json:"under"`
	Over            int                       `json:"over"`
	Fallbacks       int                       `json:"fallbacks"`
	Unavailable     int                       `json:"unavailable"`
	Baseline        benchBaselineJSON         `json:"baseline"`
	Matrix          map[string]map[string]int `json:"matrix"`
	Outcomes        map[string]map[string]int `json:"outcomes"`
}

type benchBaselineJSON struct {
	Label string  `json:"label"`
	Count int     `json:"count"`
	Share float64 `json:"share"`
}

func printBenchSummaryJSON(encoder *json.Encoder, threshold float64, counts *benchCounts) error {
	label, count, total := counts.baseline()
	summary := benchSummaryJSON{
		Threshold: threshold, Tasks: counts.tasks,
		ComplexityExact: counts.complexityExact, DomainExact: counts.domainExact,
		Under: counts.under, Over: counts.over, Fallbacks: counts.fallbacks,
		Unavailable: counts.unavailable, Matrix: counts.matrix, Outcomes: counts.outcomes,
	}
	if total > 0 {
		summary.Baseline = benchBaselineJSON{Label: label, Count: count, Share: float64(count) / float64(total)}
	}
	return encoder.Encode(map[string]benchSummaryJSON{"summary": summary})
}

var benchV2Questions = []string{"contract", "lifecycle", "security", "open_decision", "mechanical"}

type benchV2Answer struct {
	Answer      bool    `json:"answer"`
	Confidence  float64 `json:"confidence"`
	Defaulted   bool    `json:"defaulted"`
	Unavailable bool    `json:"unavailable"`
}

type benchV2Record struct {
	PlanSlug    string                   `json:"plan_slug"`
	Task        string                   `json:"task"`
	PlanLane    string                   `json:"plan_lane"`
	JudgeLane   string                   `json:"judge_lane,omitempty"`
	Scope       classify.ScopeFeatures   `json:"scope"`
	Answers     map[string]benchV2Answer `json:"answers,omitempty"`
	Outcome     string                   `json:"outcome"`
	Relation    string                   `json:"relation"`
	InputTokens *int                     `json:"input_tokens,omitempty"`
	Unavailable string                   `json:"unavailable,omitempty"`
}

func (r benchV2Record) text() string {
	line := fmt.Sprintf("%s %s plan=%s", r.PlanSlug, r.Task, r.PlanLane)
	if r.Unavailable != "" {
		line += " unavailable=" + r.Unavailable
	} else {
		line += " judge=" + r.JudgeLane
	}
	line += fmt.Sprintf(" scope=files:%d,dirs:%d,test_only:%t,docs_only:%t", r.Scope.Files, r.Scope.Directories, r.Scope.TestOnly, r.Scope.DocsOnly)
	for _, key := range benchV2Questions {
		answer, ok := r.Answers[key]
		if !ok {
			continue
		}
		value := "no"
		if answer.Answer {
			value = "yes"
		}
		line += fmt.Sprintf(" %s=%s confidence=%.2f defaulted=%t", key, value, answer.Confidence, answer.Defaulted)
	}
	return fmt.Sprintf("%s outcome=%s relation=%s", line, r.Outcome, r.Relation)
}

func classifyBenchTasksV2(ctx context.Context, stdout io.Writer, j judge.Judge, buildReason string, plans []routing.Plan, deliveries []benchDelivery, asJSON bool) error {
	encoder := json.NewEncoder(stdout)
	counts := newBenchV2Counts()
	for _, plan := range plans {
		for _, task := range plan.Tasks {
			record := benchV2RecordFor(ctx, j, buildReason, plan, task, deliveries)
			counts.add(record)
			if asJSON {
				if err := encoder.Encode(record); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(stdout, record.text()); err != nil {
				return err
			}
		}
	}
	if asJSON {
		return encoder.Encode(map[string]benchV2Summary{"summary": counts.summary()})
	}
	return counts.printSummary(stdout)
}

func benchV2RecordFor(ctx context.Context, j judge.Judge, buildReason string, plan routing.Plan, task routing.PlanTask, deliveries []benchDelivery) benchV2Record {
	record := benchV2Record{
		PlanSlug: plan.Slug, Task: task.ID, PlanLane: string(task.Complexity),
		Scope: classify.Features(task), Outcome: benchOutcomeUnknown, Relation: benchRelationUnknown,
	}
	if delivery := benchDeliveryFor(deliveries, plan.Slug, task.ID); delivery != nil {
		record.Outcome = benchOutcome(delivery.records, task.ID)
	}
	if j == nil {
		record.Unavailable = buildReason
		return record
	}
	response, err := j.Ask(ctx, classify.BuildRequestV2(task, plan.ContextFor(task.Number)))
	if err != nil {
		record.Unavailable = judgeReplayReason(err)
		return record
	}
	decision := classify.DecideV2(record.Scope, response.Answers)
	record.JudgeLane = string(decision.Complexity)
	record.Answers = make(map[string]benchV2Answer, len(benchV2Questions))
	for _, key := range benchV2Questions {
		answer := response.Answers[key]
		unavailable := answer.Type != judge.QuestionNoul || math.IsNaN(answer.Noul) || answer.Noul < 0 || answer.Noul > 1
		confidence := 0.0
		if !unavailable {
			confidence = math.Max(answer.Noul, 1-answer.Noul)
		}
		defaulted := false
		for _, name := range decision.Defaulted {
			if name == key {
				defaulted = true
				break
			}
		}
		record.Answers[key] = benchV2Answer{Answer: decision.Answers[key], Confidence: confidence, Defaulted: defaulted, Unavailable: unavailable}
	}
	if response.Usage.InputTokens != 0 {
		tokens := response.Usage.InputTokens
		record.InputTokens = &tokens
	}
	switch {
	case decision.Complexity == task.Complexity:
		record.Relation = benchRelationEqual
	case benchComplexityRank[decision.Complexity] < benchComplexityRank[task.Complexity]:
		record.Relation = benchRelationLower
	default:
		record.Relation = benchRelationHigher
	}
	return record
}

type benchV2Measure struct {
	Count        int     `json:"count"`
	Total        int     `json:"total"`
	Share        float64 `json:"share"`
	Pass         bool    `json:"pass"`
	ReportedOnly bool    `json:"reported_only,omitempty"`
}

type benchV2Discrimination struct {
	Distribution map[string]int `json:"distribution"`
	LargestLane  string         `json:"largest_lane"`
	LargestShare float64        `json:"largest_share"`
	Lanes        int            `json:"lanes"`
	Pass         bool           `json:"pass"`
}

type benchV2Summary struct {
	Tasks              int                       `json:"tasks"`
	Sufficient         int                       `json:"sufficient"`
	Insufficient       int                       `json:"insufficient"`
	Unknown            int                       `json:"unknown"`
	Unavailable        int                       `json:"unavailable"`
	UnavailableAnswers int                       `json:"unavailable_answers"`
	DefaultedAnswers   int                       `json:"defaulted_answers"`
	AnswerDistribution map[string]map[string]int `json:"answer_distribution"`
	Economy            benchV2Measure            `json:"economy"`
	Safety             benchV2Measure            `json:"safety"`
	Discrimination     benchV2Discrimination     `json:"discrimination"`
	Balance            benchV2Measure            `json:"balance"`
	Agreement          benchV2Measure            `json:"agreement"`
	Pass               bool                      `json:"pass"`
}

type benchV2Counts struct {
	summaryData          benchV2Summary
	economy              int
	safety               int
	agreement            int
	agreedTotal          int
	measured             int
	sufficientMeasured   int
	insufficientMeasured int
}

func newBenchV2Counts() *benchV2Counts {
	c := &benchV2Counts{}
	c.summaryData.AnswerDistribution = map[string]map[string]int{}
	c.summaryData.Discrimination.Distribution = map[string]int{}
	for _, key := range benchV2Questions {
		c.summaryData.AnswerDistribution[key] = map[string]int{"yes": 0, "no": 0}
	}
	for _, lane := range benchComplexityLabels {
		c.summaryData.Discrimination.Distribution[lane] = 0
	}
	return c
}

func (c *benchV2Counts) add(record benchV2Record) {
	s := &c.summaryData
	s.Tasks++
	switch record.Outcome {
	case benchOutcomeCandidate, benchOutcomeRetried:
		s.Sufficient++
	case benchOutcomeEscalated, benchOutcomeFailed:
		s.Insufficient++
	default:
		s.Unknown++
	}
	if record.Unavailable != "" {
		s.Unavailable++
		s.UnavailableAnswers += len(benchV2Questions)
		return
	}
	for _, key := range benchV2Questions {
		answer := record.Answers[key]
		if answer.Unavailable {
			s.UnavailableAnswers++
		}
		if answer.Defaulted {
			s.DefaultedAnswers++
		}
		value := "no"
		if answer.Answer {
			value = "yes"
		}
		s.AnswerDistribution[key][value]++
	}
	c.agreedTotal++
	if record.Relation == benchRelationEqual {
		c.agreement++
	}
	if record.Outcome == benchOutcomeUnknown {
		return
	}
	c.measured++
	s.Discrimination.Distribution[record.JudgeLane]++
	if record.Outcome == benchOutcomeCandidate || record.Outcome == benchOutcomeRetried {
		c.sufficientMeasured++
		if record.Relation != benchRelationHigher {
			c.economy++
		}
	} else {
		c.insufficientMeasured++
		if record.Relation == benchRelationHigher {
			c.safety++
		}
	}
}

func benchV2Share(count, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(count) / float64(total)
}

func (c *benchV2Counts) summary() benchV2Summary {
	s := c.summaryData
	s.Economy = benchV2Measure{Count: c.economy, Total: c.sufficientMeasured, Share: benchV2Share(c.economy, c.sufficientMeasured)}
	s.Economy.Pass = s.Economy.Total > 0 && s.Economy.Share >= 0.75
	s.Safety = benchV2Measure{Count: c.safety, Total: c.insufficientMeasured, Share: benchV2Share(c.safety, c.insufficientMeasured), ReportedOnly: s.Insufficient < 10}
	s.Safety.Pass = s.Safety.Total > 0 && s.Safety.Share >= 0.5
	s.Agreement = benchV2Measure{Count: c.agreement, Total: c.agreedTotal, Share: benchV2Share(c.agreement, c.agreedTotal)}
	for _, lane := range benchComplexityLabels {
		count := s.Discrimination.Distribution[lane]
		if count > 0 {
			s.Discrimination.Lanes++
		}
		if count > s.Discrimination.Distribution[s.Discrimination.LargestLane] {
			s.Discrimination.LargestLane = lane
		}
	}
	s.Discrimination.LargestShare = benchV2Share(s.Discrimination.Distribution[s.Discrimination.LargestLane], c.measured)
	s.Discrimination.Pass = c.measured > 0 && s.Discrimination.LargestShare <= 0.7 && s.Discrimination.Lanes >= 3
	s.Balance.Share = (s.Economy.Share + s.Safety.Share) / 2
	s.Balance.Pass = s.Economy.Total > 0 && s.Safety.Total > 0 && s.Balance.Share >= 0.65
	s.Pass = s.Economy.Pass && (s.Safety.Pass || s.Safety.ReportedOnly) && s.Discrimination.Pass && s.Balance.Pass
	return s
}

func (c *benchV2Counts) printSummary(stdout io.Writer) error {
	s := c.summary()
	if _, err := fmt.Fprintf(stdout, "bench v2 tasks=%d sufficient=%d insufficient=%d unknown=%d unavailable=%d\n", s.Tasks, s.Sufficient, s.Insufficient, s.Unknown, s.Unavailable); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "economy=%d/%d (%.2f) %s\n", s.Economy.Count, s.Economy.Total, s.Economy.Share, benchV2Verdict(s.Economy.Pass)); err != nil {
		return err
	}
	status := ""
	if s.Safety.ReportedOnly {
		status = " reported only"
	}
	if _, err := fmt.Fprintf(stdout, "safety=%d/%d (%.2f) %s%s\n", s.Safety.Count, s.Safety.Total, s.Safety.Share, benchV2Verdict(s.Safety.Pass), status); err != nil {
		return err
	}
	parts := make([]string, 0, len(benchComplexityLabels))
	for _, lane := range benchComplexityLabels {
		parts = append(parts, fmt.Sprintf("%s=%d", lane, s.Discrimination.Distribution[lane]))
	}
	if _, err := fmt.Fprintf(stdout, "discrimination=%s largest=%s %.2f lanes=%d %s\n", strings.Join(parts, " "), s.Discrimination.LargestLane, s.Discrimination.LargestShare, s.Discrimination.Lanes, benchV2Verdict(s.Discrimination.Pass)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "balance=%.2f %s\nagreement=%d/%d (%.2f)\nunavailable_answers=%d/%d defaulted_answers=%d\n", s.Balance.Share, benchV2Verdict(s.Balance.Pass), s.Agreement.Count, s.Agreement.Total, s.Agreement.Share, s.UnavailableAnswers, s.Tasks*len(benchV2Questions), s.DefaultedAnswers); err != nil {
		return err
	}
	for _, key := range benchV2Questions {
		if _, err := fmt.Fprintf(stdout, "answers %s yes=%d no=%d\n", key, s.AnswerDistribution[key]["yes"], s.AnswerDistribution[key]["no"]); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "bench v2 %s\n", benchV2Verdict(s.Pass))
	return err
}

func benchV2Verdict(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}
