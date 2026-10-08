package council

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

type RunOptions struct {
	Root       string
	Skills     string
	Parallel   int
	Timeout    time.Duration
	Subprocess *executor.Subprocess
}

type CouncilFailure struct {
	Label    string `json:"label"`
	Stage    string `json:"stage"`
	Reason   string `json:"reason"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Tail     string `json:"tail,omitempty"`
}

type RunResult struct {
	Critiques []Critique                      `json:"critiques"`
	Reviews   []CrossReview                   `json:"reviews"`
	Aggregate AggregateResult                 `json:"aggregate"`
	Synthesis string                          `json:"synthesis"`
	Failures  []CouncilFailure                `json:"failures"`
	Labels    map[string]routing.RuntimeValue `json:"labels"`
}

type councilSession struct {
	label   string
	runtime routing.RuntimeValue
	adapter executor.Adapter
	prompt  string
}

type councilSessionResult struct {
	output   string
	tail     string
	exitCode int
	failure  *CouncilFailure
	treeErr  error
}

// Run collects independent critiques, anonymous cross-reviews, and a chairman
// synthesis. It returns evidence for the caller to write, never plan approval.
func Run(ctx context.Context, plan string, conventions []string, table routing.RoutingTable, opts RunOptions) (RunResult, error) {
	if strings.TrimSpace(plan) == "" || opts.Parallel < 0 || opts.Timeout < 0 {
		return RunResult{}, fmt.Errorf("council: a plan and non-negative parallelism and timeout are required")
	}
	if opts.Parallel == 0 {
		opts.Parallel = 1
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Minute
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return RunResult{}, err
	}
	if opts.Skills == "" {
		opts.Skills, err = loop.FindSkills(root, "")
		if err != nil {
			return RunResult{}, err
		}
	}
	rows := table.CouncilRows()
	if len(rows) < 2 {
		return RunResult{}, fmt.Errorf("council: at least two counsellors are required")
	}
	chairman, ok := table.ChairmanRole()
	if !ok {
		return RunResult{}, fmt.Errorf("council: no chairman route")
	}
	subprocess := executor.NewSubprocess()
	if opts.Subprocess != nil {
		subprocess = *opts.Subprocess
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return RunResult{}, err
	}
	git := publication.GitClient{Executable: gitPath, Runner: publication.ExecRunner{}}
	baseline, err := git.WorktreeState(ctx, root)
	if err != nil {
		return RunResult{}, fmt.Errorf("council: initial tree signature: %w", err)
	}
	result := RunResult{Labels: make(map[string]routing.RuntimeValue)}
	sessions := make([]councilSession, len(rows))
	for i, row := range rows {
		adapter, err := executor.LoadAdapter(opts.Skills, string(row.Executor))
		if err != nil {
			return RunResult{}, err
		}
		label := councilLabel(i)
		effort := string(row.Lane)
		if effort == "" {
			effort = "high"
		} else if row.Lane == routing.ComplexityCritical {
			effort = "xhigh"
		}
		runtime := routing.RuntimeValue{Provider: string(row.Executor), Model: row.Model, Reasoning: effort}
		result.Labels[label] = runtime
		sessions[i] = councilSession{label: label, runtime: runtime, adapter: adapter, prompt: BuildCritiquePrompt(plan, conventions)}
	}
	chairAdapter, err := executor.LoadAdapter(opts.Skills, string(chairman.Executor))
	if err != nil {
		return RunResult{}, err
	}
	chair := councilSession{label: "chairman", runtime: routing.RuntimeValue{Provider: string(chairman.Executor), Model: chairman.Model, Reasoning: "high"}, adapter: chairAdapter}
	result.Labels[chair.label] = chair.runtime
	first, err := runCouncilStage(ctx, "critique", sessions, opts, subprocess, git, baseline, root)
	if err != nil {
		return RunResult{}, err
	}
	parsed := make([]councilSession, 0, len(sessions))
	for i, session := range sessions {
		if first[i].failure != nil {
			result.Failures = append(result.Failures, *first[i].failure)
			continue
		}
		critique, err := ParseCritique(first[i].output, session.label)
		if err != nil {
			result.Failures = append(result.Failures, *councilFailure(session.label, "critique", "unparsable answer", &first[i].exitCode, first[i].tail))
			continue
		}
		result.Critiques = append(result.Critiques, critique)
		parsed = append(parsed, session)
	}
	if len(result.Critiques) < 2 {
		return result, fmt.Errorf("council: fewer than two critiques parsed")
	}
	for i := range parsed {
		parsed[i].prompt = BuildCrossReviewPrompt(result.Critiques, parsed[i].label)
	}
	second, err := runCouncilStage(ctx, "cross-review", parsed, opts, subprocess, git, baseline, root)
	if err != nil {
		return RunResult{}, err
	}
	for i, session := range parsed {
		if second[i].failure != nil {
			result.Failures = append(result.Failures, *second[i].failure)
			continue
		}
		review, err := ParseCrossReview(second[i].output, result.Critiques, session.label)
		if err != nil {
			result.Failures = append(result.Failures, *councilFailure(session.label, "cross-review", "unparsable answer", &second[i].exitCode, second[i].tail))
			continue
		}
		result.Reviews = append(result.Reviews, review)
	}
	result.Aggregate = Aggregate(result.Critiques, result.Reviews)
	chair.prompt = BuildChairmanPrompt(result.Aggregate, result.Critiques)
	final, err := runCouncilStage(ctx, "chairman", []councilSession{chair}, opts, subprocess, git, baseline, root)
	if err != nil {
		return RunResult{}, err
	}
	if final[0].failure != nil {
		result.Failures = append(result.Failures, *final[0].failure)
	} else {
		result.Synthesis = final[0].output
	}
	return result, nil
}

func runCouncilStage(ctx context.Context, stage string, sessions []councilSession, opts RunOptions, subprocess executor.Subprocess, git publication.GitClient, baseline publication.WorktreeState, root string) ([]councilSessionResult, error) {
	results := make([]councilSessionResult, len(sessions))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(opts.Parallel, len(sessions)) {
		workers.Go(func() {
			for i := range jobs {
				results[i] = runCouncilSession(ctx, stage, sessions[i], subprocess, git, baseline, root, opts.Timeout)
			}
		})
	}
	for i := range sessions {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	for _, outcome := range results {
		if outcome.treeErr != nil {
			return nil, outcome.treeErr
		}
	}
	if err := checkCouncilTree(ctx, git, root, baseline); err != nil {
		return nil, err
	}
	return results, nil
}

func runCouncilSession(ctx context.Context, stage string, session councilSession, subprocess executor.Subprocess, git publication.GitClient, baseline publication.WorktreeState, root string, timeout time.Duration) councilSessionResult {
	if err := checkCouncilTree(ctx, git, root, baseline); err != nil {
		return councilSessionResult{treeErr: err}
	}
	invocation, err := session.adapter.ReadonlyCommand(executor.Request{Prompt: session.prompt, Cwd: root, Model: session.runtime.Model, Effort: session.runtime.Reasoning})
	if err != nil {
		return councilSessionResult{failure: councilFailure(session.label, stage, "read-only invocation failed", nil, "")}
	}
	outcome, runErr := subprocess.Execute(ctx, session.adapter, invocation, timeout)
	if err := checkCouncilTree(ctx, git, root, baseline); err != nil {
		return councilSessionResult{treeErr: err}
	}
	output := string(outcome.Stdout)
	if output == "" && len(outcome.RawStdout) > 0 {
		output = string(outcome.RawStdout)
	}
	if runErr != nil {
		return councilSessionResult{failure: councilFailure(session.label, stage, "session failed to start", &outcome.ExitCode, output+"\n"+string(outcome.Stderr))}
	}
	if outcome.TimedOut {
		return councilSessionResult{failure: councilFailure(session.label, stage, "session timed out", &outcome.ExitCode, output+"\n"+string(outcome.Stderr))}
	}
	if outcome.RateLimited {
		return councilSessionResult{failure: councilFailure(session.label, stage, "session hit a usage or rate limit", &outcome.ExitCode, output+"\n"+string(outcome.Stderr))}
	}
	if !outcome.Finished {
		return councilSessionResult{failure: councilFailure(session.label, stage, fmt.Sprintf("session exited with code %d", outcome.ExitCode), &outcome.ExitCode, output+"\n"+string(outcome.Stderr))}
	}
	if outcome.Truncated || outcome.Question != "" {
		return councilSessionResult{failure: councilFailure(session.label, stage, "session output was truncated or requested input", &outcome.ExitCode, output+"\n"+string(outcome.Stderr))}
	}
	return councilSessionResult{output: output, tail: output + "\n" + string(outcome.Stderr), exitCode: outcome.ExitCode}
}

func checkCouncilTree(ctx context.Context, git publication.GitClient, root string, baseline publication.WorktreeState) error {
	current, err := git.WorktreeState(ctx, root)
	if err != nil {
		return fmt.Errorf("council: tree signature unavailable: %w", err)
	}
	if current != baseline {
		return fmt.Errorf("council: read-only session changed the repository")
	}
	return nil
}

func councilFailure(label, stage, reason string, code *int, output string) *CouncilFailure {
	tail := executor.DropSecretBearingLines(executor.Tail([]byte(output), 40))
	if len(tail) > 4096 {
		tail = tail[len(tail)-4096:]
	}
	return &CouncilFailure{Label: label, Stage: stage, Reason: reason, ExitCode: code, Tail: tail}
}

func councilLabel(index int) string {
	label := ""
	for index >= 0 {
		label = string(rune('A'+index%26)) + label
		index = index/26 - 1
	}
	return label
}
