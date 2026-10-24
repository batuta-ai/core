package loop

import (
	"strings"
	"testing"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/routing"
)

func TestBriefCarriesTheProgressProtocol(t *testing.T) {
	t.Parallel()
	brief := Brief(BriefInput{Criteria: []gates.Criterion{
		{Text: "first criterion", Proof: "test -f first"},
		{Text: "second criterion", Proof: "test -f second"},
	}})
	_, section, found := strings.Cut(brief, "## Progress protocol\n\n")
	if !found {
		t.Fatalf("brief has no progress protocol section:\n%s", brief)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, want := range []string{
		"BATUTA-PROGRESS <n> START", "before the first edit toward it",
		"BATUTA-PROGRESS <n> DONE", "when its proof passes locally",
		"plain text on stdout", "nothing else on that line", "no tool required",
		"1-based positions", "## Acceptance criteria", "BATUTA-QUESTION:",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("progress protocol lacks %q:\n%s", want, section)
		}
	}
}

func TestBriefCarriesOnlySharedAndOwnDecisions(t *testing.T) {
	t.Parallel()
	plan := routing.Plan{
		Context: "Shared decision.\n\n**Task 1.** Decision for one.\n\n**Tasks 2–3.** Decision for two and three.",
		Tasks: []routing.PlanTask{
			{Number: 1},
			{Number: 2},
			{Number: 3},
		},
	}
	for _, tc := range []struct {
		task  routing.PlanTask
		own   string
		other string
	}{
		{task: routing.PlanTask{Number: 1}, own: "Decision for one.", other: "Decision for two and three."},
		{task: routing.PlanTask{Number: 2}, own: "Decision for two and three.", other: "Decision for one."},
	} {
		brief := Brief(BriefInput{Plan: plan, Task: tc.task})
		if !strings.Contains(brief, "Shared decision.") || !strings.Contains(brief, tc.own) || strings.Contains(brief, tc.other) {
			t.Errorf("brief for task %d has wrong context:\n%s", tc.task.Number, brief)
		}
	}
}

func conventionsSectionOf(t *testing.T, brief string) string {
	t.Helper()
	_, section, found := strings.Cut(brief, "## Conventions\n\n")
	if !found {
		t.Fatalf("brief has no conventions section:\n%s", brief)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

func TestBriefCarriesProfileConventions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		raw         string
		conventions []string
		missing     []string
	}{
		{
			name:        "with template sections",
			raw:         "Stack: Go\n\n## Conventions\n\nWrap errors with %w.\nNo globals.\n\n## Other\n\nunrelated\n",
			conventions: []string{"### Go\n\nTemplate go body.", "### Testing\n\nTemplate testing body."},
		},
		{
			name: "without template sections",
			raw:  "Stack: Go\n\n## Conventions\n\nWrap errors with %w.\nNo globals.\n",
		},
		{
			name:        "with missing templates note",
			raw:         "Stack: Go\n\n## Conventions\n\nWrap errors with %w.\nNo globals.\n",
			conventions: []string{"### Go\n\nTemplate go body."},
			missing:     []string{"nope"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			brief := Brief(BriefInput{Profile: ParseProfile(tc.raw), Conventions: tc.conventions, Missing: tc.missing})
			section := conventionsSectionOf(t, brief)
			heading := "### From .batuta/profile.md\n\nWrap errors with %w.\nNo globals."
			stack := strings.Index(section, "Stack: Go")
			leave := strings.Index(section, "Leave no TODO")
			own := strings.Index(section, heading)
			if stack < 0 || leave < 0 || own < 0 {
				t.Fatalf("missing parts (stack %d, leave %d, own %d):\n%s", stack, leave, own, section)
			}
			if !(stack < leave && leave < own) {
				t.Errorf("profile section out of order:\n%s", section)
			}
			if strings.Contains(section, "Unknown — no stack template") {
				t.Errorf("Unknown line present although the profile carries conventions:\n%s", section)
			}
			if len(tc.conventions) > 0 {
				first := strings.Index(section, tc.conventions[0])
				if first < own {
					t.Errorf("template section before the profile section:\n%s", section)
				}
			}
			if len(tc.missing) > 0 {
				note := strings.Index(section, "(templates not installed")
				if note < strings.LastIndex(section, tc.conventions[len(tc.conventions)-1]) {
					t.Errorf("missing note before the template sections:\n%s", section)
				}
			}
		})
	}
}

func TestBriefWithoutProfileConventions(t *testing.T) {
	t.Parallel()
	const leave = "Leave no TODO, placeholder or skipped test behind: the task is done when its criteria hold on the real code.\n\n"
	const unknown = "Unknown — no stack template was found; follow the existing code style of the files you touch and change only what this brief asks.\n\n"
	for _, tc := range []struct {
		name        string
		raw         string
		conventions []string
		missing     []string
		want        string
	}{
		{
			name:        "no profile section with templates",
			raw:         "Stack: Go\n",
			conventions: []string{"### Go\n\nTemplate go body."},
			missing:     []string{"nope"},
			want:        "Stack: Go\n" + leave + "### Go\n\nTemplate go body.\n\n(templates not installed on this machine: nope)\n\n",
		},
		{
			name: "no profile section without templates",
			raw:  "Stack: Go\n\n## Conventions for briefs\n\nnot the own section\n",
			want: "Stack: Go\n" + leave + unknown,
		},
		{
			name: "profile section without templates",
			raw:  "Stack: Go\n\n## Conventions\n\nBe kind.\n",
			want: "Stack: Go\n" + leave + "### From .batuta/profile.md\n\nBe kind.\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			brief := Brief(BriefInput{Profile: ParseProfile(tc.raw), Conventions: tc.conventions, Missing: tc.missing})
			got := conventionsSectionOf(t, brief)
			// the section is cut before "\n## ", so its trailing blank line is gone
			if want := strings.TrimSuffix(tc.want, "\n"); got != want {
				t.Errorf("conventions section\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func grantedBrief() string {
	return Brief(BriefInput{
		Task:     routing.PlanTask{TaskArtifact: routing.TaskArtifact{Title: "Grant task"}, Scope: []string{"out/1.txt"}},
		Worktree: "/tmp/worktree",
		Grant:    routing.TaskGrant{Paths: []string{"docs/**", "scripts/run.sh"}, Operations: []string{"git-commit", "test"}, Until: routing.GrantUntilTask},
	})
}

func briefSection(t *testing.T, brief, heading string) string {
	t.Helper()
	_, section, found := strings.Cut(brief, heading+"\n\n")
	if !found {
		t.Fatalf("brief has no %q section:\n%s", heading, brief)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

func TestBriefRendersStandingGrant(t *testing.T) {
	t.Parallel()
	section := briefSection(t, grantedBrief(), "## Standing grant")
	for _, want := range []string{"`docs/**`", "`scripts/run.sh`", "`git-commit`", "`test`", "until the task ends", "never lifts the `## Boundaries` rules"} {
		if !strings.Contains(section, want) {
			t.Errorf("standing grant section lacks %q:\n%s", want, section)
		}
	}
	if strings.Contains(Brief(BriefInput{Task: routing.PlanTask{Scope: []string{"out/1.txt"}}}), "## Standing grant") {
		t.Error("brief without a grant renders a standing grant section")
	}
}

func TestBriefEffectiveScope(t *testing.T) {
	t.Parallel()
	section := briefSection(t, grantedBrief(), "## Scope")
	plan := strings.Index(section, "- `out/1.txt`\n")
	granted := strings.Index(section, "- `docs/**` (standing grant)\n")
	other := strings.Index(section, "- `scripts/run.sh` (standing grant)\n")
	if plan < 0 || granted < plan || other < granted {
		t.Fatalf("scope does not list the plan Scope then the granted paths:\n%s", section)
	}
	if !strings.Contains(section, "does not lift the `## Boundaries` rules") {
		t.Errorf("scope does not say the grant keeps the Boundaries:\n%s", section)
	}
}

func TestBriefGrantProtocolLine(t *testing.T) {
	t.Parallel()
	section := briefSection(t, grantedBrief(), "## Stop conditions")
	for _, want := range []string{"BATUTA-QUESTION:", "`Beyond grant: <what you need that is not granted>`", "must end with"} {
		if !strings.Contains(section, want) {
			t.Errorf("stop conditions lack %q:\n%s", want, section)
		}
	}
	if strings.Contains(briefSection(t, Brief(BriefInput{}), "## Stop conditions"), "Beyond grant") {
		t.Error("brief without a grant mentions the Beyond grant clause")
	}
}

func TestBriefGrantReply(t *testing.T) {
	t.Parallel()
	brief := Brief(BriefInput{
		Task:     routing.PlanTask{TaskArtifact: routing.TaskArtifact{Title: "Grant task"}},
		Worktree: "/tmp/worktree",
		Question: "Which file holds the brief?",
		Answer:   "loop/brief.go",
		GrantReply: &GrantReply{
			Question:   "May I edit docs?",
			Paths:      []string{"docs/**", "scripts/run.sh"},
			Operations: []string{"git-commit", "test"},
		},
	})
	human := "You stopped earlier with this question: Which file holds the brief?\nThe answer: loop/brief.go\nContinue from the state of this worktree.\n\n"
	reply := "You stopped earlier with this question: May I edit docs?\nAlready granted: paths docs/**, scripts/run.sh; operations git-commit, test. Continue. Ask again only with a Beyond grant: clause that names what is not granted; use that clause for scope questions.\n\n"
	humanAt, replyAt := strings.Index(brief, human), strings.Index(brief, reply)
	if humanAt < 0 || replyAt < humanAt+len(human) {
		t.Fatalf("context lacks the human Q/A followed by the grant reply:\n%s", brief)
	}
	if strings.Index(brief, "## Conventions") < replyAt {
		t.Errorf("grant reply renders outside ## Context:\n%s", brief)
	}
}

func TestBriefHumanAnswerUnchanged(t *testing.T) {
	t.Parallel()
	brief := Brief(BriefInput{
		Task:     routing.PlanTask{TaskArtifact: routing.TaskArtifact{Title: "Human task"}},
		Worktree: "/tmp/worktree",
		Question: "Which file holds the brief?",
		Answer:   "loop/brief.go",
	})
	want := "You stopped earlier with this question: Which file holds the brief?\nThe answer: loop/brief.go\nContinue from the state of this worktree.\n\n"
	if !strings.Contains(brief, want) {
		t.Errorf("human answer block changed:\n%s", brief)
	}
	if strings.Contains(brief, "Already granted") {
		t.Error("brief without a grant reply renders Already granted")
	}
}
