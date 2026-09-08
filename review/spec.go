package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

var specSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type SpecRule struct {
	ID    string `json:"id"`
	Task  string `json:"task"`
	Text  string `json:"text"`
	Proof string `json:"proof,omitempty"`
}

type CriterionStatus string

const (
	CriterionSatisfied     CriterionStatus = "satisfied"
	CriterionViolated      CriterionStatus = "violated"
	CriterionNotApplicable CriterionStatus = "not-applicable"
)

type SpecResult struct {
	ID     string          `json:"id"`
	Status CriterionStatus `json:"status"`
	Path   string          `json:"path"`
}

func (r SpecResult) Violated() bool { return r.Status == CriterionViolated }

type SpecSweep struct {
	Covered  bool             `json:"covered"`
	Reason   string           `json:"reason,omitempty"`
	Results  []SpecResult     `json:"results"`
	Attempts []SessionAttempt `json:"attempts"`
}

func (s SpecSweep) VerdictCriteria() []Criterion {
	criteria := make([]Criterion, 0, len(s.Results))
	for _, result := range s.Results {
		criteria = append(criteria, Criterion{ID: result.ID, Violated: result.Violated()})
	}
	return criteria
}

// SpecCriteria turns every nonempty Accept entry into a stable, numbered rule.
func SpecCriteria(plan routing.Plan) []SpecRule {
	var rules []SpecRule
	for taskIndex, task := range plan.Tasks {
		number := task.Number
		if number < 1 {
			number = taskIndex + 1
		}
		for criterionIndex, criterion := range gates.ParseCriteria(task.Accept) {
			rules = append(rules, SpecRule{
				ID:    fmt.Sprintf("task-%d.%d", number, criterionIndex+1),
				Task:  task.Title,
				Text:  criterion.Text,
				Proof: criterion.Proof,
			})
		}
	}
	return rules
}

// LoadSpecCriteria reads explicit paths directly; slugs prefer active, archived,
// then legacy plans. Parsing always uses the selected file's contents.
func LoadSpecCriteria(root, spec string) ([]SpecRule, error) {
	paths := []string{spec}
	slug := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(spec), filepath.Ext(spec)), "plan-")
	if !filepath.IsAbs(spec) && !strings.ContainsAny(spec, "/\\") && filepath.Ext(spec) == "" {
		if !specSlug.MatchString(spec) {
			return nil, routing.ErrInvalidSlug
		}
		slug = spec
		paths = []string{routing.PlanPath(spec), filepath.Join(".batuta", "plans", "done", spec+".md"), filepath.Join(".batuta", "plan-"+spec+".md")}
	}
	for _, filename := range paths {
		if !filepath.IsAbs(filename) {
			filename = filepath.Join(root, filename)
		}
		file, err := os.Open(filename)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		payload, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		plan, err := routing.ParsePlan(slug, payload)
		if err != nil {
			return nil, err
		}
		return SpecCriteria(plan), nil
	}
	return nil, fmt.Errorf("review: plan %q is unavailable", spec)
}

// RunSpecSweep runs one read-only session for the complete spec. Invalid or
// incomplete output leaves the sweep explicitly uncovered.
func RunSpecSweep(ctx context.Context, manifest Manifest, rules []SpecRule, runtime routing.RuntimeValue, opts SessionOptions) (SpecSweep, error) {
	var sweep SpecSweep
	if err := validateReviewer(runtime); err != nil {
		return sweep, err
	}
	if len(rules) == 0 {
		return SpecSweep{Covered: true}, nil
	}
	if opts.Timeout < 0 {
		return sweep, fmt.Errorf("review: timeout must not be negative")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Minute
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return sweep, err
	}
	profile, err := loop.LoadProfile(root)
	if err != nil {
		return sweep, err
	}
	if opts.Skills == "" {
		opts.Skills, err = loop.FindSkills(root, "")
		if err != nil {
			return sweep, err
		}
	}
	conventions, missing := loop.Conventions(opts.Skills, profile.Template)
	if len(missing) > 0 {
		return sweep, fmt.Errorf("review: missing rubric templates: %s", strings.Join(missing, ", "))
	}
	prompt := BuildSpecPrompt(manifest, rules)
	prompt += "\nProject rubric (.batuta/profile.md):\n" + profile.Raw + "\n"
	for _, section := range conventions {
		prompt += "\n" + section + "\n"
	}
	adapter, err := executor.LoadAdapter(opts.Skills, runtime.Provider)
	if err != nil {
		return sweep, err
	}
	invocation, err := adapter.ReadonlyCommand(executor.Request{Prompt: prompt, Cwd: root, Model: runtime.Model, Effort: runtime.Reasoning})
	if err != nil {
		return sweep, err
	}
	subprocess := executor.NewSubprocess()
	if opts.Subprocess != nil {
		subprocess = *opts.Subprocess
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return sweep, err
	}
	git := publication.GitClient{Executable: gitPath, Runner: publication.ExecRunner{}}
	baseline, err := git.WorktreeState(ctx, root)
	if err != nil {
		return sweep, fmt.Errorf("review: initial tree signature: %w", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		outcome, runErr := subprocess.Execute(ctx, adapter, invocation, opts.Timeout)
		record := SessionAttempt{Result: outcome}
		if runErr != nil {
			record.Error = runErr.Error()
		}
		sweep.Attempts = append(sweep.Attempts, record)
		after, stateErr := git.WorktreeState(ctx, root)
		if stateErr != nil || after != baseline {
			sweep.Reason = "review tree changed or its signature is unavailable"
			return sweep, nil
		}
		finished := gates.Finished(outcome.Finished, outcome.TimedOut, outcome.RateLimited, outcome.ExitCode, executor.Tail(outcome.Stderr, 10))
		if runErr != nil || !finished.Pass {
			sweep.Reason = finished.Signal
			if runErr != nil {
				sweep.Reason = runErr.Error()
			}
			if ctx.Err() != nil {
				return sweep, nil
			}
			continue
		}
		if outcome.Truncated || outcome.Question != "" {
			sweep.Reason = "spec sweep output was truncated or requested human input"
			return sweep, nil
		}
		results, parseErr := parseSpecResults(string(outcome.Stdout), rules)
		if parseErr != nil {
			sweep.Reason = parseErr.Error()
			return sweep, nil
		}
		sweep.Covered = true
		sweep.Reason = ""
		sweep.Results = results
		return sweep, nil
	}
	return sweep, nil
}

func parseSpecResults(output string, rules []SpecRule) ([]SpecResult, error) {
	lines := strings.Split(output, "\n")
	start, end := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case "<<<CRITERIA":
			if start >= 0 {
				return nil, fmt.Errorf("review: duplicate CRITERIA block")
			}
			start = i
		case "CRITERIA>>>":
			if start < 0 || end >= 0 {
				return nil, fmt.Errorf("review: invalid CRITERIA block")
			}
			end = i
		}
	}
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("review: missing or unterminated CRITERIA block")
	}
	results := make([]SpecResult, 0, len(rules))
	for _, line := range lines[start+1 : end] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var result SpecResult
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			return nil, fmt.Errorf("review: invalid criterion result: %w", err)
		}
		if result.Status != CriterionSatisfied && result.Status != CriterionViolated && result.Status != CriterionNotApplicable {
			return nil, fmt.Errorf("review: criterion %q has invalid status", result.ID)
		}
		if strings.TrimSpace(result.Path) == "" {
			return nil, fmt.Errorf("review: criterion %q has no path", result.ID)
		}
		results = append(results, result)
	}
	if len(results) != len(rules) {
		return nil, fmt.Errorf("review: spec sweep answered %d of %d criteria", len(results), len(rules))
	}
	for i, rule := range rules {
		if results[i].ID != rule.ID {
			return nil, fmt.Errorf("review: expected criterion %q at position %d", rule.ID, i+1)
		}
	}
	return results, nil
}
