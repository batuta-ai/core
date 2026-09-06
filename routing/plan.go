package routing

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// Plan is a parsed plan with its workspace-relative source path.
type Plan struct {
	Path    string
	Slug    string
	Title   string
	Goal    string
	Created string
	Status  PlanStatus
	Tasks   []PlanTask
	Set     TaskSet
	// Inputs is the stamp comment on line 2 (`profile.md@sha256:… routing.md@sha256:…`),
	// empty when the plan carries none. Context is the prose under
	// `## Decisions and context`: what a fresh session — or an executor's
	// brief — needs to know. Neither is part of the task set digest.
	Inputs  string
	Context string
}

// PlanLoader reads active and legacy plans under a trusted workspace root.
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
	return filepath.Join(".batuta", "plans", slug+".md")
}

// PlanLocations lists active and legacy paths in lookup order.
func PlanLocations(slug string) []string {
	return []string{PlanPath(slug), filepath.Join(".batuta", "plan-"+slug+".md")}
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
	for _, relative := range PlanLocations(slug) {
		if _, err := os.Lstat(filepath.Join(l.root, relative)); errors.Is(err, os.ErrNotExist) {
			continue
		}
		path, err := (&ArtifactLoader{root: l.root}).resolveContained(relative)
		if err != nil {
			return Plan{}, err
		}
		payload, err := readBoundedFile(path, maxTaskArtifactBytes)
		if err != nil {
			return Plan{}, err
		}
		plan, err := ParsePlan(slug, payload)
		if err != nil {
			return Plan{}, err
		}
		plan.Path = relative
		return plan, nil
	}
	return Plan{}, errors.New("routing: plan is unavailable")
}

// ListPlans returns sorted, distinct active and legacy slugs, excluding done/.
func (l *PlanLoader) ListPlans() ([]string, error) {
	slugs := make([]string, 0)
	for _, location := range []struct{ directory, prefix string }{
		{filepath.Join(".batuta", "plans"), ""},
		{".batuta", "plan-"},
	} {
		if _, err := os.Lstat(filepath.Join(l.root, location.directory)); errors.Is(err, os.ErrNotExist) {
			continue
		}
		directory, err := (&ArtifactLoader{root: l.root}).resolveContained(location.directory)
		if err != nil {
			return nil, err
		}
		entries, err := readPlanDirectory(directory)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasPrefix(name, location.prefix) || !strings.HasSuffix(name, ".md") {
				continue
			}
			slug := strings.TrimSuffix(strings.TrimPrefix(name, location.prefix), ".md")
			if canonicalSlug.MatchString(slug) {
				slugs = append(slugs, slug)
			}
		}
	}
	slices.Sort(slugs)
	return slices.Compact(slugs), nil
}

func readPlanDirectory(directory string) ([]os.DirEntry, error) {
	handle, err := os.Open(directory)
	if err != nil {
		return nil, errors.New("routing: plan directory is unavailable")
	}
	defer handle.Close()
	entries, err := handle.ReadDir(maxPlanDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("routing: plan directory is unavailable")
	}
	if len(entries) > maxPlanDirectoryEntries {
		return nil, fmt.Errorf("%w: plan directory holds more than %d entries", ErrReauthoringRequired, maxPlanDirectoryEntries)
	}
	return entries, nil
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
	planTaskLine        = regexp.MustCompile(`^- \[([ xX])\]\s+([0-9]+)\.\s+(.+?)\s+[—-]\s+([a-z]+)/([a-z]+)(?:\s+(?:→|->)\s+(\S+))?\s*$`)
	planTaskBare        = regexp.MustCompile(`^- \[[ xX]\]\s+[0-9]+\.`)
	planFieldRe         = regexp.MustCompile(`^\s+(Scope|Accept|Depends on):\s*(.*)$`)
	planContextTaskLine = regexp.MustCompile(`^(?:\*\*Task ([0-9]+)\.\*\*|\*\*Tasks ([0-9]+(?:\s*[–-]\s*[0-9]+)?(?:\s*,\s*[0-9]+(?:\s*[–-]\s*[0-9]+)?)*)\.\*\*)`)
	planContextTaskItem = regexp.MustCompile(`^([0-9]+)(?:\s*[–-]\s*([0-9]+))?$`)
)

type contextTaskRange struct {
	first int
	last  int
}

type contextParagraph struct {
	text        string
	line        int
	labelled    bool
	invalidTask string
	tasks       []contextTaskRange
}

// ContextFor returns the shared decisions and the decisions addressed to task.
func (p Plan) ContextFor(task int) string {
	context := strings.TrimSpace(p.Context)
	if context == "" {
		return ""
	}
	paragraphs := splitContextParagraphs(context, 1)
	hasLabels := false
	for _, paragraph := range paragraphs {
		if paragraph.labelled {
			hasLabels = true
			break
		}
	}
	if !hasLabels {
		return context
	}
	selected := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if !paragraph.labelled || contextTargetsTask(paragraph.tasks, task) {
			selected = append(selected, paragraph.text)
		}
	}
	return strings.Join(selected, "\n\n")
}

func splitContextParagraphs(context string, firstLine int) []contextParagraph {
	lines := strings.Split(context, "\n")
	paragraphs := make([]contextParagraph, 0)
	current := make([]string, 0)
	line := firstLine
	fence := ""
	flush := func() {
		if len(current) == 0 {
			return
		}
		text := strings.Join(current, "\n")
		tasks, labelled, invalidTask := contextTasks(strings.SplitN(text, "\n", 2)[0])
		paragraphs = append(paragraphs, contextParagraph{
			text:        text,
			line:        line,
			labelled:    labelled,
			invalidTask: invalidTask,
			tasks:       tasks,
		})
		current = current[:0]
	}
	for index, value := range lines {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" && fence == "" {
			flush()
			continue
		}
		if len(current) == 0 {
			line = firstLine + index
		}
		current = append(current, value)
		switch {
		case fence == "" && strings.HasPrefix(trimmed, "```"):
			fence = "```"
		case fence == "" && strings.HasPrefix(trimmed, "~~~"):
			fence = "~~~"
		case fence != "" && strings.HasPrefix(trimmed, fence):
			fence = ""
		}
	}
	flush()
	return paragraphs
}

func contextTasks(line string) ([]contextTaskRange, bool, string) {
	match := planContextTaskLine.FindStringSubmatch(line)
	if match == nil {
		return nil, false, ""
	}
	items := match[1]
	if items == "" {
		items = match[2]
	}
	ranges := make([]contextTaskRange, 0)
	for _, item := range strings.Split(items, ",") {
		parts := planContextTaskItem.FindStringSubmatch(strings.TrimSpace(item))
		if parts == nil {
			return nil, true, strings.TrimSpace(item)
		}
		first, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, true, parts[1]
		}
		last := first
		if parts[2] != "" {
			last, err = strconv.Atoi(parts[2])
			if err != nil {
				return nil, true, parts[2]
			}
		}
		ranges = append(ranges, contextTaskRange{first: first, last: last})
	}
	return ranges, true, ""
}

func contextTargetsTask(ranges []contextTaskRange, task int) bool {
	for _, item := range ranges {
		if task >= item.first && task <= item.last {
			return true
		}
	}
	return false
}

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
		current      *PlanTask
		block        strings.Builder
		numbers      = map[int]int{}
		lineNo       int
		inGoal       bool
		inFence      bool
		inComment    bool
		inContext    bool
		context      strings.Builder
		contextAt    int
		contextFence string
		statusAt     int
		pending      = map[int][]pendingDependency{}
		flushTask    = func() error {
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
		trimmed := strings.TrimSpace(line)
		if inContext && inFence {
			context.WriteString(line)
			context.WriteString("\n")
			if strings.HasPrefix(trimmed, contextFence) {
				inFence = false
				contextFence = ""
			}
			continue
		}
		if inComment {
			if strings.Contains(trimmed, "-->") {
				inComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") {
			inComment = !strings.Contains(trimmed[4:], "-->")
			if inner, found := strings.CutPrefix(strings.TrimSuffix(strings.TrimSpace(trimmed[4:]), "-->"), "inputs:"); found && plan.Inputs == "" {
				plan.Inputs = strings.TrimSpace(inner)
			}
			continue
		}
		if inContext {
			if strings.HasPrefix(line, "## ") {
				inContext = false
			} else {
				context.WriteString(line)
				context.WriteString("\n")
				if strings.HasPrefix(trimmed, "```") {
					inFence = true
					contextFence = "```"
				} else if strings.HasPrefix(trimmed, "~~~") {
					inFence = true
					contextFence = "~~~"
				}
				continue
			}
		}
		if strings.HasPrefix(line, "## ") {
			inContext = strings.EqualFold(strings.TrimSpace(line[3:]), "Decisions and context")
			if inContext {
				if err := flushTask(); err != nil {
					return Plan{}, err
				}
				contextAt = lineNo + 1
				continue
			}
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if planMetaLine.MatchString(line) {
			if strings.Contains(line, "**Status:**") {
				m := planStatusLine.FindStringSubmatch(line)
				if m == nil || strings.Count(line, "**Status:**") > 1 {
					return Plan{}, fmt.Errorf("%w: line %d: Status must be exactly proposed | approved | in progress | done, once", ErrReauthoringRequired, lineNo)
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
	plan.Context = strings.TrimSpace(context.String())
	if plan.Status == "" {
		return Plan{}, fmt.Errorf("%w: header has no `**Status:**` (proposed | approved | in progress | done)", ErrReauthoringRequired)
	}
	if len(plan.Tasks) == 0 {
		return Plan{}, fmt.Errorf("%w: plan has no tasks", ErrReauthoringRequired)
	}
	for _, paragraph := range splitContextParagraphs(context.String(), contextAt) {
		if paragraph.invalidTask != "" {
			return Plan{}, fmt.Errorf("%w: line %d: context label names task %s, which does not exist", ErrReauthoringRequired, paragraph.line, paragraph.invalidTask)
		}
		for _, taskRange := range paragraph.tasks {
			if taskRange.first > taskRange.last {
				return Plan{}, fmt.Errorf("%w: line %d: context task range %d–%d must be ascending", ErrReauthoringRequired, paragraph.line, taskRange.first, taskRange.last)
			}
			if missing, found := firstMissingContextTask(taskRange, numbers); found {
				return Plan{}, fmt.Errorf("%w: line %d: context label names task %d, which does not exist", ErrReauthoringRequired, paragraph.line, missing)
			}
		}
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

func firstMissingContextTask(taskRange contextTaskRange, numbers map[int]int) (int, bool) {
	defined := make([]int, 0, len(numbers))
	for number := range numbers {
		if number >= taskRange.first && number <= taskRange.last {
			defined = append(defined, number)
		}
	}
	slices.Sort(defined)
	expected := taskRange.first
	for _, number := range defined {
		if number > expected {
			return expected, true
		}
		if number == taskRange.last {
			return 0, false
		}
		expected = number + 1
	}
	return expected, true
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
