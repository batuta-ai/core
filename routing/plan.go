package routing

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// TaskSource is where a delivery takes its tasks from. The Compozy daemon
// reads `.compozy/tasks/<slug>/` through ArtifactLoader; CLI hosts read the
// plan the batuta-plan skill wrote through PlanLoader. Both return the same
// TaskSet, so the delivery graph does not know which one it came from.
type TaskSource interface {
	Load(slug string) (TaskSet, error)
}

var (
	_ TaskSource = (*ArtifactLoader)(nil)
	_ TaskSource = (*PlanLoader)(nil)
)

const maxPlanDirectoryEntries = 512

// PlanStatus is the `**Status:**` value in a plan header.
type PlanStatus string

const (
	PlanProposed   PlanStatus = "proposed"
	PlanApproved   PlanStatus = "approved"
	PlanInProgress PlanStatus = "in progress"
	PlanDone       PlanStatus = "done"
)

// PlanTask is one `- [ ] N.` entry of a plan: the TaskArtifact the graph
// consumes plus the hints the loop hands to the executor.
type PlanTask struct {
	TaskArtifact
	Number   int
	Executor string
	Model    string
	Scope    []string
	Accept   []string
	Line     int
}

// Plan is a parsed `.batuta/plan-<slug>.md`.
type Plan struct {
	Slug    string
	Title   string
	Goal    string
	Created string
	Status  PlanStatus
	Tasks   []PlanTask
	Set     TaskSet
}

// PlanLoader reads plans from `.batuta/` under a trusted workspace root.
type PlanLoader struct {
	root string
}

func NewPlanLoader(workspaceRoot string) (*PlanLoader, error) {
	loader, err := NewArtifactLoader(workspaceRoot)
	if err != nil {
		return nil, err
	}
	return &PlanLoader{root: loader.root}, nil
}

// PlanPath is the plan file for a slug, relative to the workspace root.
func PlanPath(slug string) string {
	return filepath.Join(".batuta", "plan-"+slug+".md")
}

func (l *PlanLoader) Load(slug string) (TaskSet, error) {
	plan, err := l.LoadPlan(slug)
	if err != nil {
		return TaskSet{}, err
	}
	return plan.Set, nil
}

func (l *PlanLoader) LoadPlan(slug string) (Plan, error) {
	if !canonicalSlug.MatchString(slug) {
		return Plan{}, ErrInvalidSlug
	}
	path, err := (&ArtifactLoader{root: l.root}).resolveContained(PlanPath(slug))
	if err != nil {
		return Plan{}, err
	}
	payload, err := readBoundedFile(path, maxTaskArtifactBytes)
	if err != nil {
		return Plan{}, err
	}
	return ParsePlan(slug, payload)
}

// ListPlans returns the slugs of every plan under `.batuta/`, sorted.
func (l *PlanLoader) ListPlans() ([]string, error) {
	directory, err := (&ArtifactLoader{root: l.root}).resolveContained(".batuta")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, errors.New("routing: plan directory is unavailable")
	}
	if len(entries) > maxPlanDirectoryEntries {
		return nil, fmt.Errorf("%w: .batuta holds more than %d entries", ErrReauthoringRequired, maxPlanDirectoryEntries)
	}
	slugs := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "plan-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(name, "plan-"), ".md")
		if canonicalSlug.MatchString(slug) {
			slugs = append(slugs, slug)
		}
	}
	return slugs, nil
}

var (
	planTitleLine = regexp.MustCompile(`^#\s+Plan\s*[—:-]\s*(.+?)\s*$`)
	// Metadata lives on lines that start with a bold field; a status
	// mentioned in prose, a comment or a code fence is not a declaration.
	planMetaLine   = regexp.MustCompile(`^\*\*(Created|Status):\*\*`)
	planStatusLine = regexp.MustCompile(`\*\*Status:\*\*\s*(proposed|approved|in progress|done)\s*(?:·|$)`)
	planCreated    = regexp.MustCompile(`\*\*Created:\*\*\s*([^·*]+?)\s*(?:·|$)`)
	planGoalLine   = regexp.MustCompile(`^\*\*Goal:\*\*\s*(.*)$`)
	// - [ ] 1. Title — domain/complexity → executor/model
	planTaskLine = regexp.MustCompile(`^- \[([ xX])\]\s+([0-9]+)\.\s+(.+?)\s+[—-]\s+([a-z]+)/([a-z]+)(?:\s+(?:→|->)\s+(\S+))?\s*$`)
	planTaskBare = regexp.MustCompile(`^- \[[ xX]\]\s+[0-9]+\.`)
	planFieldRe  = regexp.MustCompile(`^\s+(Scope|Accept|Depends on):\s*(.*)$`)
)

// ParsePlan parses the machine contract of a plan (batuta-plan/SKILL.md):
// a task is a `- [ ] N. <title> — <domain>/<complexity> → <executor/model>`
// line; indented `Accept:` is mandatory; `Depends on:` lists task numbers;
// `Scope:` is a comma-separated list. Everything else is prose.
func ParsePlan(slug string, payload []byte) (Plan, error) {
	if !canonicalSlug.MatchString(slug) {
		return Plan{}, ErrInvalidSlug
	}
	if len(payload) > maxTaskArtifactBytes {
		return Plan{}, errors.New("routing: plan byte budget exceeded")
	}
	plan := Plan{Slug: slug}
	scanner := bufio.NewScanner(strings.NewReader(string(payload)))
	scanner.Buffer(make([]byte, 1024), maxTaskArtifactBytes)
	var (
		current   *PlanTask
		block     strings.Builder
		numbers   = map[int]int{}
		lineNo    int
		inGoal    bool
		inFence   bool
		statusAt  int
		pending   = map[int][]pendingDependency{}
		flushTask = func() error {
			if current == nil {
				return nil
			}
			if len(current.Accept) == 0 {
				return fmt.Errorf("%w: line %d: task %d has no Accept: line", ErrReauthoringRequired, current.Line, current.Number)
			}
			content := block.String()
			digest := sha256.Sum256([]byte(content))
			current.Content = []byte(content)
			current.Digest = hex.EncodeToString(digest[:])
			plan.Tasks = append(plan.Tasks, *current)
			current = nil
			block.Reset()
			return nil
		}
	)
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if lineNo == 1 {
			if m := planTitleLine.FindStringSubmatch(line); m != nil {
				plan.Title = m[1]
				continue
			}
			return Plan{}, fmt.Errorf("%w: line 1: expected `# Plan — <title>`", ErrReauthoringRequired)
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(strings.TrimSpace(line), "<!--") {
			continue
		}
		if planMetaLine.MatchString(line) {
			if strings.Contains(line, "**Status:**") {
				m := planStatusLine.FindStringSubmatch(line)
				if m == nil {
					return Plan{}, fmt.Errorf("%w: line %d: Status must be exactly proposed | approved | in progress | done", ErrReauthoringRequired, lineNo)
				}
				if plan.Status != "" {
					return Plan{}, fmt.Errorf("%w: line %d: Status already declared at line %d", ErrReauthoringRequired, lineNo, statusAt)
				}
				plan.Status, statusAt = PlanStatus(m[1]), lineNo
			}
			if m := planCreated.FindStringSubmatch(line); m != nil && plan.Created == "" {
				plan.Created = strings.TrimSpace(m[1])
			}
			continue
		}
		if m := planGoalLine.FindStringSubmatch(line); m != nil {
			plan.Goal = m[1]
			inGoal = true
			continue
		}
		if inGoal {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "**") || strings.HasPrefix(line, "#") {
				inGoal = false
			} else {
				plan.Goal += " " + strings.TrimSpace(line)
				continue
			}
		}
		if m := planTaskLine.FindStringSubmatch(line); m != nil {
			if err := flushTask(); err != nil {
				return Plan{}, err
			}
			number, _ := strconv.Atoi(m[2])
			if number == 0 {
				return Plan{}, fmt.Errorf("%w: line %d: task numbers start at 1", ErrReauthoringRequired, lineNo)
			}
			if prior, duplicate := numbers[number]; duplicate {
				return Plan{}, fmt.Errorf("%w: line %d: task %d already defined at line %d", ErrReauthoringRequired, lineNo, number, prior)
			}
			numbers[number] = lineNo
			domain, complexity := Domain(m[4]), Complexity(m[5])
			if !domain.Valid() || !complexity.Valid() {
				return Plan{}, fmt.Errorf("%w: line %d: unknown lane %s/%s", ErrReauthoringRequired, lineNo, m[4], m[5])
			}
			status := "pending"
			if m[1] != " " {
				status = "completed"
			}
			task := PlanTask{
				TaskArtifact: TaskArtifact{ID: "task_" + m[2], Title: strings.TrimSpace(m[3]), Status: status, Domain: domain, Complexity: complexity},
				Number:       number, Line: lineNo,
			}
			if m[6] != "" {
				task.Executor, task.Model, _ = strings.Cut(m[6], "/")
			}
			current = &task
			block.WriteString(line)
			block.WriteString("\n")
			continue
		}
		if planTaskBare.MatchString(line) {
			return Plan{}, fmt.Errorf("%w: line %d: task line must read `- [ ] N. <title> — <domain>/<complexity> → <executor/model>`", ErrReauthoringRequired, lineNo)
		}
		if current == nil {
			continue
		}
		if strings.TrimSpace(line) == "" || !strings.HasPrefix(line, " ") {
			if err := flushTask(); err != nil {
				return Plan{}, err
			}
			continue
		}
		block.WriteString(line)
		block.WriteString("\n")
		m := planFieldRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch m[1] {
		case "Scope":
			current.Scope = append(current.Scope, splitList(m[2], ",")...)
		case "Accept":
			current.Accept = append(current.Accept, splitList(m[2], ";")...)
		case "Depends on":
			for _, item := range splitList(m[2], ",") {
				dependency, err := strconv.Atoi(item)
				if err != nil || dependency < 1 || dependency == current.Number {
					return Plan{}, fmt.Errorf("%w: line %d: Depends on lists other tasks' numbers", ErrReauthoringRequired, lineNo)
				}
				pending[current.Number] = append(pending[current.Number], pendingDependency{number: dependency, line: lineNo})
				current.Dependencies = append(current.Dependencies, "task_"+strconv.Itoa(dependency))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Plan{}, errors.New("routing: plan is unreadable")
	}
	if err := flushTask(); err != nil {
		return Plan{}, err
	}
	if plan.Status == "" {
		return Plan{}, fmt.Errorf("%w: header has no `**Status:**` (proposed | approved | in progress | done)", ErrReauthoringRequired)
	}
	if len(plan.Tasks) == 0 {
		return Plan{}, fmt.Errorf("%w: plan has no tasks", ErrReauthoringRequired)
	}
	for number, dependencies := range pending {
		for _, dependency := range dependencies {
			if _, defined := numbers[dependency.number]; !defined {
				return Plan{}, fmt.Errorf("%w: line %d: task %d depends on task %d, which does not exist", ErrReauthoringRequired, dependency.line, number, dependency.number)
			}
		}
	}
	ordered, err := topologicalPlanOrder(plan.Tasks)
	if err != nil {
		return Plan{}, err
	}
	set := TaskSet{Slug: slug, Tasks: make([]TaskArtifact, 0, len(ordered))}
	for _, task := range ordered {
		set.Tasks = append(set.Tasks, task.TaskArtifact)
	}
	snapshot, err := set.DeliverySnapshot()
	if err != nil {
		return Plan{}, err
	}
	set.Digest = snapshot.Digest
	plan.Set = set
	return plan, nil
}

type pendingDependency struct {
	number int
	line   int
}

// topologicalPlanOrder returns the tasks with every dependency before its
// dependents, keeping the file order among tasks that are otherwise free
// (Kahn's algorithm, lowest task number first). A cycle is a reauthoring
// error naming the tasks involved.
func topologicalPlanOrder(tasks []PlanTask) ([]PlanTask, error) {
	byID := make(map[string]PlanTask, len(tasks))
	indegree := make(map[string]int, len(tasks))
	dependents := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
		indegree[task.ID] += 0
		for _, dependency := range task.Dependencies {
			indegree[task.ID]++
			dependents[dependency] = append(dependents[dependency], task.ID)
		}
	}
	ready := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if indegree[task.ID] == 0 {
			ready = append(ready, task.ID)
		}
	}
	ordered := make([]PlanTask, 0, len(tasks))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				slices.SortFunc(ready, func(a, b string) int { return byID[a].Number - byID[b].Number })
			}
		}
	}
	if len(ordered) != len(tasks) {
		stuck := make([]string, 0)
		for _, task := range tasks {
			if indegree[task.ID] > 0 {
				stuck = append(stuck, strconv.Itoa(task.Number))
			}
		}
		return nil, fmt.Errorf("%w: tasks %s depend on each other in a cycle", ErrReauthoringRequired, strings.Join(stuck, ", "))
	}
	return ordered, nil
}

func splitList(value, separator string) []string {
	parts := strings.Split(value, separator)
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			items = append(items, part)
		}
	}
	return items
}
