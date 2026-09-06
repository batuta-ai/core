package loop

import (
	"fmt"
	"strings"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/routing"
)

// BriefInput is everything a brief is built from. The executor sees the
// brief and nothing else (references/brief.md), so it must stand alone.
type BriefInput struct {
	Plan        routing.Plan
	Task        routing.PlanTask
	Profile     Profile
	Conventions []string
	Missing     []string // templates that were not found
	Worktree    string
	Base        string
	Criteria    []gates.Criterion
	Feedback    []string // failures of the previous attempt, verbatim
	Question    string   // the question the executor asked, when continuing
	Answer      string   // the human's answer
	Lane        string   // domain/complexity for the sweep rule
}

const methodLine = "Work test-first from the acceptance criteria. Investigate root cause before fixing a bug; never silence a signal (cast, suppression, empty catch, sleep) instead of fixing its source — if you must, mark `// WORKAROUND: <reason>` and say so in your report."

const testLaws = "1. Test the behavior, never the mock.\n2. A failing test means fix the code, not the test.\n3. No test-only flags or branches in production code."

// Brief renders the eight sections of references/brief.md for one task.
func Brief(input BriefInput) string {
	var b strings.Builder
	task := input.Task
	fmt.Fprintf(&b, "# Brief — %s\n\n", task.Title)
	fmt.Fprintf(&b, "Work only inside `%s`. It is a git worktree on its own branch; commit there as you like, your history never reaches the base branch.\n\n", input.Worktree)

	b.WriteString("## Goal\n\n")
	b.WriteString(task.Title + ".")
	if prose := taskProse(task); prose != "" {
		b.WriteString(" " + prose)
	}
	if input.Plan.Goal != "" {
		fmt.Fprintf(&b, "\n\nThis task is part of the plan \"%s\": %s", input.Plan.Title, input.Plan.Goal)
	}
	b.WriteString("\n\n")

	b.WriteString("## Context\n\n")
	if strings.TrimSpace(input.Plan.Context) != "" {
		b.WriteString(strings.TrimSpace(input.Plan.Context) + "\n\n")
	} else {
		b.WriteString("Unknown — the plan carries no decisions and context section; discover what you need inside the Scope.\n\n")
	}
	if len(task.Dependencies) > 0 {
		fmt.Fprintf(&b, "Tasks %s of the plan are already integrated on the base commit `%s`; build on them.\n\n", strings.Join(task.Dependencies, ", "), short(input.Base))
	}
	if input.Question != "" {
		fmt.Fprintf(&b, "You stopped earlier with this question: %s\nThe answer: %s\nContinue from the state of this worktree.\n\n", input.Question, input.Answer)
	}
	if len(input.Feedback) > 0 {
		b.WriteString("### Feedback from the previous attempt\n\nA previous session worked on this task in this worktree and did not pass verification. You are a new session with no memory of it: read the current code before changing anything. Fix only what is missing or wrong — do not reimplement what is already correct and tested. The real cause:\n\n")
		for _, line := range input.Feedback {
			b.WriteString("- " + strings.ReplaceAll(line, "\n", "\n  ") + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Conventions\n\n")
	if input.Profile.Stack != "" {
		fmt.Fprintf(&b, "Stack: %s\n", input.Profile.Stack)
	}
	if input.Profile.Methodology != "" {
		fmt.Fprintf(&b, "Methodology: %s\n", input.Profile.Methodology)
	}
	if input.Profile.Test != "" {
		fmt.Fprintf(&b, "Test command: `%s` — run the suite with exactly this command; verification uses it and no other runner.\n", input.Profile.Test)
	}
	b.WriteString("Leave no TODO, placeholder or skipped test behind: the task is done when its criteria hold on the real code.\n\n")
	if len(input.Conventions) == 0 {
		b.WriteString("Unknown — no stack template was found; follow the existing code style of the files you touch and change only what this brief asks.\n\n")
	}
	for _, section := range input.Conventions {
		b.WriteString(section + "\n\n")
	}
	if len(input.Missing) > 0 {
		fmt.Fprintf(&b, "(templates not installed on this machine: %s)\n\n", strings.Join(input.Missing, ", "))
	}

	b.WriteString("## Acceptance criteria\n\n")
	for index, criterion := range input.Criteria {
		fmt.Fprintf(&b, "%d. %s", index+1, criterion.Text)
		if criterion.Proof != "" {
			fmt.Fprintf(&b, " — proof: `%s` exits 0", criterion.Proof)
		}
		b.WriteString("\n")
	}
	if len(input.Criteria) == 0 {
		b.WriteString("Unknown — the plan lists no Accept line for this task.\n")
	}
	b.WriteString("\n")

	b.WriteString("## Boundaries\n\n")
	b.WriteString("Do not touch CI configuration, licenses, lockfiles (except through an allowed dependency), `WORK.md` or anything under `.batuta/`. Do not push, do not merge, do not change branches.\n\n")

	b.WriteString("## Scope\n\n")
	if len(task.Scope) > 0 {
		for _, entry := range task.Scope {
			b.WriteString("- `" + strings.TrimSpace(entry) + "`\n")
		}
		b.WriteString("\nDo not change anything outside this list; if the task requires it, stop and report.\n\n")
	} else {
		b.WriteString("Unknown — the plan declares no Scope; keep changes to the files the criteria name and report every path you touch.\n\n")
	}

	b.WriteString("## Expected evidence\n\n")
	b.WriteString("In your final message: the paths you touched, each command you ran with its real output, and anything you did not verify, declared as such. Never claim a result you did not observe.\n\n")

	b.WriteString("## Stop conditions\n\n")
	b.WriteString("Stop and report instead of improvising when: (1) the code's shape contradicts this brief; (2) the same command fails twice; (3) the fix needs edits beyond Scope or Boundaries. ")
	fmt.Fprintf(&b, "To ask the human a question, print exactly one final line `%s <your question>` and exit 0; the run pauses and resumes in this worktree with the answer.\n\n", executor.QuestionPrefix)

	if needsTestLaws(input.Criteria, input.Profile) {
		b.WriteString("## Test laws\n\n" + testLaws + "\n\n")
	}
	b.WriteString("## Method\n\n" + methodLine + "\n")
	return b.String()
}

// taskProse is the prose under a task line, minus the machine fields.
func taskProse(task routing.PlanTask) string {
	var lines []string
	for index, line := range strings.Split(strings.TrimRight(string(task.Content), "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if index == 0 || trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Scope:") || strings.HasPrefix(trimmed, "Accept:") || strings.HasPrefix(trimmed, "Depends on:") {
			continue
		}
		lines = append(lines, trimmed)
	}
	return strings.Join(lines, " ")
}

func needsTestLaws(criteria []gates.Criterion, profile Profile) bool {
	if profile.Test != "" {
		return true
	}
	for _, criterion := range criteria {
		lower := strings.ToLower(criterion.Text + " " + criterion.Proof)
		if strings.Contains(lower, "test") || strings.Contains(lower, "spec") {
			return true
		}
	}
	return false
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
