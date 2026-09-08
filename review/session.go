package review

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

var reviewerExecutor = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ReviewerRuntimeFromTable uses the optional review role, then the high lane
// for the requested domain. An override is executor/model (model may contain /).
func ReviewerRuntimeFromTable(table routing.RoutingTable, domain routing.Domain, override string) (routing.RuntimeValue, error) {
	var runtime routing.RuntimeValue
	switch {
	case override != "":
		var found bool
		runtime.Provider, runtime.Model, found = strings.Cut(override, "/")
		if !found {
			return runtime, fmt.Errorf("review: reviewer must be executor/model")
		}
	case table.Review != nil:
		runtime.Provider, runtime.Model = string(table.Review.Executor), table.Review.Model
	default:
		row, ok := table.Row(routing.ComplexityHigh, domain)
		if !ok {
			return runtime, fmt.Errorf("review: no review role or high lane for %q", domain)
		}
		runtime.Provider, runtime.Model = string(row.Executor), row.Model
	}
	runtime.Reasoning = "high"
	if err := validateReviewer(runtime); err != nil {
		return routing.RuntimeValue{}, err
	}
	return runtime, nil
}

func validateReviewer(runtime routing.RuntimeValue) error {
	if !reviewerExecutor.MatchString(runtime.Provider) || runtime.Provider == string(routing.ExecutorSelf) {
		return fmt.Errorf("review: reviewer requires a CLI executor")
	}
	model := runtime.Model
	if strings.TrimSpace(model) != model || model == "" || model == "—" || model == "-" || strings.ContainsAny(model, "\x00\r\n") || strings.HasPrefix(model, "<") || strings.EqualFold(model, "default") || strings.EqualFold(model, "default model") {
		return fmt.Errorf("review: reviewer requires an exact model")
	}
	return nil
}

type SessionOptions struct {
	Root       string
	Skills     string               // Installed batuta skill directory; empty uses normal discovery.
	Parallel   int                  // Zero defaults to one.
	Timeout    time.Duration        // Per attempt; zero defaults to ten minutes.
	Subprocess *executor.Subprocess // Nil uses the production executor runner; custom runners must support concurrent calls.
}

type SessionAttempt struct {
	Result executor.Result `json:"result"`
	Error  string          `json:"error,omitempty"`
}

type CohortResult struct {
	Cohort   int              `json:"cohort"` // Zero-based manifest index; results retain manifest order.
	Files    []string         `json:"files"`
	Covered  bool             `json:"covered"`
	Reason   string           `json:"reason,omitempty"`
	Findings []Finding        `json:"findings"`
	Rejected []RejectedLine   `json:"rejected,omitempty"`
	Attempts []SessionAttempt `json:"attempts"`
}

// RunCohorts invokes only adapter read-only commands in the checkout. Each
// attempt and the entire batch must preserve the initial gate-1 signature.
// A shared-tree change invalidates all cohorts because attribution is unsafe.
// Setup errors return an error; session failures remain explicit uncovered results.
func RunCohorts(ctx context.Context, manifest Manifest, runtime routing.RuntimeValue, opts SessionOptions) ([]CohortResult, error) {
	if err := validateReviewer(runtime); err != nil {
		return nil, err
	}
	if opts.Parallel < 0 || opts.Timeout < 0 {
		return nil, fmt.Errorf("review: parallelism and timeout must not be negative")
	}
	if opts.Parallel == 0 {
		opts.Parallel = 1
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Minute
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}
	opts.Root = root
	profile, err := loop.LoadProfile(root)
	if err != nil {
		return nil, err
	}
	if opts.Skills == "" {
		opts.Skills, err = loop.FindSkills(root, "")
		if err != nil {
			return nil, err
		}
	}
	conventions, missing := loop.Conventions(opts.Skills, profile.Template)
	if len(missing) > 0 {
		return nil, fmt.Errorf("review: missing rubric templates: %s", strings.Join(missing, ", "))
	}
	adapter, err := executor.LoadAdapter(opts.Skills, runtime.Provider)
	if err != nil {
		return nil, err
	}
	subprocess := executor.NewSubprocess()
	if opts.Subprocess != nil {
		subprocess = *opts.Subprocess
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	git := publication.GitClient{Executable: gitPath, Runner: publication.ExecRunner{}}
	baseline, err := git.WorktreeState(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("review: initial tree signature: %w", err)
	}
	invocations := make([]executor.Invocation, len(manifest.Cohorts))
	results := make([]CohortResult, len(manifest.Cohorts))
	for i, cohort := range manifest.Cohorts {
		prompt, err := BuildCohortPrompt(root, manifest, cohort, profile.Raw, conventions)
		if err != nil {
			return nil, err
		}
		invocation, err := adapter.ReadonlyCommand(executor.Request{Prompt: prompt, Cwd: root, Model: runtime.Model, Effort: runtime.Reasoning})
		if err != nil {
			return nil, err
		}
		invocations[i] = invocation
		results[i] = CohortResult{Cohort: i, Files: append([]string(nil), cohort.Files...), Reason: "session did not run"}
	}
	var wg sync.WaitGroup
	jobs := make(chan int)
	for worker := 0; worker < min(opts.Parallel, len(results)); worker++ {
		wg.Go(func() {
			for i := range jobs {
				runCohort(ctx, manifest, adapter, subprocess, git, baseline, invocations[i], opts.Timeout, &results[i])
			}
		})
	}
	for i := range results {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	after, err := git.WorktreeState(ctx, root)
	if err != nil || after != baseline {
		reason := "review tree changed; all cohort sessions rejected"
		if err != nil {
			reason = "review tree signature could not be verified: " + err.Error()
		}
		for i := range results {
			results[i].Covered = false
			results[i].Findings = nil
			results[i].Reason = reason
		}
	}
	return results, nil
}

func runCohort(ctx context.Context, manifest Manifest, adapter executor.Adapter, subprocess executor.Subprocess, git publication.GitClient, baseline publication.WorktreeState, invocation executor.Invocation, timeout time.Duration, result *CohortResult) {
	for attempt := 0; attempt < 2; attempt++ {
		before, err := git.WorktreeState(ctx, invocation.Dir)
		if err != nil || before != baseline {
			result.Reason = "review tree changed or its signature is unavailable"
			return
		}
		outcome, runErr := subprocess.Execute(ctx, adapter, invocation, timeout)
		record := SessionAttempt{Result: outcome}
		if runErr != nil {
			record.Error = runErr.Error()
		}
		result.Attempts = append(result.Attempts, record)
		after, err := git.WorktreeState(ctx, invocation.Dir)
		if err != nil || after != baseline {
			result.Reason = "review tree changed or its signature is unavailable"
			return
		}
		finished := gates.Finished(outcome.Finished, outcome.TimedOut, outcome.RateLimited, outcome.ExitCode, executor.Tail(outcome.Stderr, 10))
		if runErr != nil || !finished.Pass {
			result.Reason = finished.Signal
			if runErr != nil {
				result.Reason = runErr.Error()
			}
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if outcome.Truncated || outcome.Question != "" {
			result.Reason = "reviewer output was truncated or requested human input"
			return
		}
		result.Findings, result.Rejected = ParseFindings(string(outcome.Stdout))
		accepted := result.Findings[:0]
		for _, finding := range result.Findings {
			if !findingInCohort(finding, manifest, result.Files) {
				result.Rejected = append(result.Rejected, RejectedLine{Reason: fmt.Sprintf("finding %s:%d-%d is outside cohort hunks", finding.File, finding.Line, rangeEnd(finding.Line, finding.EndLine))})
			} else {
				accepted = append(accepted, finding)
			}
		}
		result.Findings = accepted
		if len(result.Rejected) > 0 {
			result.Reason = "reviewer output contains rejected findings or invalid framing"
			return
		}
		result.Covered = true
		result.Reason = ""
		return
	}
}

func findingInCohort(finding Finding, manifest Manifest, files []string) bool {
	name := path.Clean(finding.File)
	for _, candidate := range files {
		if name != candidate {
			continue
		}
		for _, file := range manifest.Files {
			if file.Path != name {
				continue
			}
			for _, hunk := range file.Hunks {
				if hunk.Count > 0 && finding.Line >= hunk.Start && rangeEnd(finding.Line, finding.EndLine) < hunk.Start+hunk.Count {
					return true
				}
			}
		}
	}
	return false
}
