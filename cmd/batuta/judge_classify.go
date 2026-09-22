package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/batuta-ai/core/classify"
	"github.com/batuta-ai/core/judge"
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
// unavailable task names no lane.
type classifyRecord struct {
	Task                 string  `json:"task"`
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
}

func (r classifyRecord) text() string {
	line := fmt.Sprintf("%s plan=%s/%s", r.Task, r.PlanDomain, r.PlanComplexity)
	if r.Unavailable != "" {
		return line + " unavailable=" + r.Unavailable
	}
	tokens := "unknown"
	if r.InputTokens != nil {
		tokens = strconv.Itoa(*r.InputTokens)
	}
	return fmt.Sprintf("%s judge=%s/%s complexity=%.2f domain=%.2f fallback=%t input_tokens=%s",
		line, r.Domain, r.Complexity, r.ComplexityConfidence, r.DomainConfidence, r.Fallback, tokens)
}

func runJudgeClassify(args []string, stdout, stderr io.Writer) error {
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
	threshold := config.Decision(classifyDecisionName).Threshold
	if threshold <= 0 {
		threshold = classifyDefaultThreshold
	}
	if !*asJSON {
		fmt.Fprintf(stdout, "threshold=%s\n", corpusThresholdLabel(threshold))
	}
	return classifyPlanTasks(context.Background(), stdout, j, buildReason, plan, threshold, *asJSON)
}

// classifyPlanTasks asks one classify request per task, sequential, and
// prints one record per task. An unavailable judge — never built, or failed
// on that task — reports unavailable and no lane.
func classifyPlanTasks(ctx context.Context, stdout io.Writer, j judge.Judge, buildReason string, plan routing.Plan, threshold float64, asJSON bool) error {
	encoder := json.NewEncoder(stdout)
	for _, task := range plan.Tasks {
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
			response, err := j.Ask(ctx, classify.BuildRequest(task, plan.ContextFor(task.Number)))
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
