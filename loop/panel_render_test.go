package loop

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func renderFixture(state string) PanelView {
	m := PanelView{
		Header:    PanelHeader{Delivery: "roadmap-20260906-215846", Project: "core", Branch: "feat/roadmap", Head: "4c1e9f2", Phase: 2, State: "running", Elapsed: 14*time.Minute + 32*time.Second},
		Attention: PanelAttention{Kind: "none"},
		Context:   PanelContext{Executor: "codex", Model: "gpt-5.6-sol", Reasoning: "medium", TestCommand: "go test ./...", Sandbox: "workspace-write", Sessions: 2, PID: 99573},
		Progress:  PanelProgress{WavesDone: 1, WavesTotal: 4, TasksDone: 1, TasksTotal: 5},
		Detail:    PanelDetail{Task: "task_2", Attempt: 1, AttemptLimit: 4, Title: "Archiving a plan ticks its phase in the roadmap", Criterion: 2, CriterionTotal: 3, CriterionTitle: "TickPhase rewrites only the line", LastRecord: "task_progress 2 START", LastAge: 12 * time.Second, Worktree: ".batuta/worktrees/roadmap-task-2-e1", LogPath: ".batuta/runs/2026-09-06-roadmap-task-2-e1.out.log"},
		LogTitle:  "task-2-e1",
		LogLines:  []string{"BATUTA-PROGRESS 1 DONE", "BATUTA-PROGRESS 2 START", "exec: go test ./routing -run TestTickPhaseRewritesOnlyTheLine -count=1", "--- FAIL: TestTickPhaseRewritesOnlyTheLine (0.00s)", "    roadmap_test.go:88: TickPhase() rewrote 2 lines, want 1", "codex: the rewrite must keep every other byte; switching to a line-indexed replace"},
		Waves: []PanelWave{
			{Number: 1, Base: "838d06a", Integrated: "4c1e9f2", Done: 1, Total: 1, State: "integrated", Rows: []PanelRow{{Task: "task_1", Title: "Parse .batuta/roadmap.md into phases with an optional plan slug", State: "integrated", Attempt: 1, AttemptLimit: 4, Gates: [4]string{"pass", "pass", "pass", "pass"}, Commit: "4c1e9f2"}}},
			{Number: 2, Base: "4c1e9f2", Total: 1, State: "running", Rows: []PanelRow{{Task: "task_2", Title: "Archiving a plan ticks its phase in the roadmap", State: "running", Attempt: 1, AttemptLimit: 4}}},
			{Number: 3, Dependencies: []string{"task_1"}, Total: 1, State: "pending", Rows: []PanelRow{{Task: "task_3", Title: "The opened record carries roadmap and phase", State: "pending"}}},
			{Number: 4, Dependencies: []string{"task_2", "task_3"}, Total: 2, State: "pending", Rows: []PanelRow{{Task: "task_4", Title: "batuta loop --roadmap runs the phases in order, one delivery per approved plan", State: "pending"}, {Task: "task_5", Title: "capabilities, usage and docs describe the roadmap", State: "pending"}}},
		},
	}
	row := &m.Waves[1].Rows[0]
	switch state {
	case "question":
		m.Header.State = "waiting_input"
		row.State = "waiting_input"
		m.Detail.Question = "Should TickPhase also archive the phase when every task is ticked?"
		m.Attention = PanelAttention{Kind: "question", Task: "task_2", Text: m.Detail.Question, Hint: "r answers"}
		m.Detail.LastRecord = "question"
		m.Detail.LastAge = 3 * time.Minute
		m.LogLines = append(m.LogLines[:2], "BATUTA-QUESTION: "+m.Detail.Question)
	case "limit":
		m.Header.State = "limit_wait"
		row.State = "limit_wait"
		m.Attention = PanelAttention{Kind: "limit", Text: "codex usage limit · resumes 23:40 (41 min) · wait 1/20 · same attempt, no retry spent"}
		m.Detail.LastRecord = "limit_wait"
		m.Detail.LastAge = 4 * time.Minute
		m.LogLines = append(m.LogLines[:2], "Rate limit reached for gpt-5.6-sol, resets 11:40pm")
	case "blocked":
		m.Header.State = "blocked"
		m.Header.Elapsed = 41*time.Minute + 10*time.Second
		row.State = "blocked"
		row.Attempt = 3
		row.Gates = [4]string{"pass", "pass", "fail", "pending"}
		m.Waves[1].State = "blocked"
		m.Waves[3].State = "blocked"
		m.Attention = PanelAttention{Kind: "blocked", Task: "task_2", Text: "G2 Tests failed 3/3 · escalated gpt-5.6-sol → gpt-6-astra", Hint: "o opens the log"}
		m.Detail.Reason = "G2 tests: TestTickPhaseRewritesOnlyTheLine fails after retry and escalation"
		m.Detail.LastRecord = "task_blocked"
		m.Detail.LastAge = time.Minute
		m.LogLines = []string{"--- FAIL: TestTickPhaseRewritesOnlyTheLine (0.00s)", "    roadmap_test.go:88: TickPhase() rewrote 2 lines, want 1", "FAIL", "FAIL\tgithub.com/batuta-ai/core/routing\t0.412s", "gate 2 tests: fail (exit 1)", "attempt e3/4 blocked: tests_failed"}
	case "conflict":
		m.Header.State = "conflict"
		m.Header.Elapsed = 22*time.Minute + 5*time.Second
		row.State = "conflict"
		row.Attempt = 2
		row.Gates = [4]string{"pass", "pass", "pass", "pass"}
		m.Attention = PanelAttention{Kind: "conflict", Task: "task_2", Text: "conflict when integrating after task_3 · re-executing on 9b2c4e1 with the same executor · e2/4"}
		m.Detail.Attempt = 2
		m.Detail.Reason = "candidate 7fa1e02 conflicted with loop/report.go on 9b2c4e1"
		m.Detail.LastRecord = "settled reexecute_conflict"
		m.Detail.LastAge = 20 * time.Second
	}
	return m
}

func assertRenderGolden(t *testing.T, state string, style Style, suffix string) {
	t.Helper()
	path := fmt.Sprintf("testdata/mock-%s-%d%s.txt", state, style.Width, suffix)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := Render(renderFixture(state), style)
	if got != string(want) {
		lines := strings.Split(got, "\n")
		expected := strings.Split(string(want), "\n")
		for i := 0; i < min(len(lines), len(expected)); i++ {
			if lines[i] != expected[i] {
				t.Fatalf("%s line %d\ngot  %q\nwant %q", path, i+1, lines[i], expected[i])
			}
		}
		t.Fatalf("%s: got %d lines, want %d", path, len(lines), len(expected))
	}
}

func TestRenderMatchesGoldens(t *testing.T) {
	for _, state := range []string{"calm", "question", "limit", "blocked", "conflict"} {
		for _, width := range []int{120, 80, 60} {
			t.Run(fmt.Sprintf("%s/%d", state, width), func(t *testing.T) {
				assertRenderGolden(t, state, Style{Width: width, Lang: "en", Glyphs: "unicode"}, "")
			})
		}
	}
}
func TestRenderPortugueseLabels(t *testing.T) {
	assertRenderGolden(t, "calm", Style{Width: 120, Lang: "pt", Glyphs: "unicode"}, "-pt-unicode")
	for _, key := range []string{"LANG", "LC_ALL", "BATUTA_LANG"} {
		t.Run(key, func(t *testing.T) {
			for _, k := range []string{"LANG", "LC_ALL", "BATUTA_LANG"} {
				t.Setenv(k, "")
			}
			t.Setenv(key, "pt_BR.UTF-8")
			if got := StyleForWriter(&strings.Builder{}); got.Lang != "pt" {
				t.Fatalf("style: %+v", got)
			}
		})
	}
}
func TestRenderASCIIFallback(t *testing.T) {
	assertRenderGolden(t, "calm", Style{Width: 120, Lang: "en", Glyphs: "ascii"}, "-en-ascii")
	t.Setenv("BATUTA_LANG", "en")
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "en_US.UTF-8")
	if got := StyleForWriter(&strings.Builder{}); got.Glyphs != "ascii" {
		t.Fatalf("style: %+v", got)
	}
}
func TestRenderColours(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	sgr := regexp.MustCompile("\x1b\\[[0-9;]*m")
	for _, state := range []string{"calm", "blocked"} {
		style := Style{Width: 120, Lang: "en", Glyphs: "unicode", Colour: true}
		got := Render(renderFixture(state), style)
		for _, code := range sgr.FindAllString(got, -1) {
			if code != "\x1b[0m" && code != "\x1b[31m" && code != "\x1b[32m" && code != "\x1b[34m" {
				t.Fatalf("unexpected SGR %q", code)
			}
		}
		colour := "\x1b[34m"
		if state == "blocked" {
			colour = "\x1b[31m"
		}
		if !strings.Contains(got, colour) || !strings.Contains(got, "\x1b[32m") {
			t.Fatal("missing semantic colours")
		}
		style.Colour = false
		if sgr.ReplaceAllString(got, "") != Render(renderFixture(state), style) {
			t.Fatal("colour changed layout")
		}
	}
	ascii := Render(renderFixture("calm"), Style{Width: 120, Lang: "en", Glyphs: "ascii", Colour: true})
	if !strings.Contains(ascii, "codex gpt-5.6-sol") || !strings.Contains(ascii, "+- Context") {
		t.Fatal("coloured ordinary ASCII text or borders")
	}
	if StyleForWriter(&strings.Builder{}).Colour {
		t.Fatal("non-TTY colour enabled")
	}
	t.Setenv("NO_COLOR", "1")
	if strings.Contains(Render(renderFixture("calm"), Style{Width: 120, Colour: true}), "\x1b[") {
		t.Fatal("NO_COLOR ignored")
	}
}
func TestTerminalSizeFallback(t *testing.T) {
	width, height := TerminalSize(^uintptr(0))
	if width != 120 || height != 40 {
		t.Fatalf("size=%dx%d", width, height)
	}
	if got := StyleForWriter(&strings.Builder{}); got.Width != 120 {
		t.Fatalf("width=%d", got.Width)
	}
}

func TestRenderUnknownContextAndBeforeRun(t *testing.T) {
	for _, tc := range []struct {
		lang, glyphs, pid, before, check string
	}{
		{"en", "unicode", "PID –", "before run", "✓ 1/1"},
		{"pt", "unicode", "PID –", "antes do run", "✓ 1/1"},
		{"en", "ascii", "PID -", "before run", "+ 1/1"},
	} {
		for _, width := range []int{80, 120} {
			model := renderFixture("calm")
			model.Header.Roadmap = ""
			model.Header.Phase = 0
			model.Context.PID = 0
			model.Waves = []PanelWave{{State: "before_run", Done: 1, Total: 1}}
			got := Render(model, Style{Width: width, Lang: tc.lang, Glyphs: tc.glyphs})
			for _, want := range []string{tc.pid, tc.before, tc.check} {
				if !strings.Contains(got, want) {
					t.Errorf("%s/%s/%d missing %q:\n%s", tc.lang, tc.glyphs, width, want, got)
				}
			}
			if header := strings.SplitN(got, "\n", 2)[0]; strings.Contains(header, "phase") || strings.Contains(header, "fase") || strings.Contains(got, "PID 0") {
				t.Errorf("unknown context rendered as zero:\n%s", got)
			}
		}
	}
}

func TestRenderUsesSuppliedValues(t *testing.T) {
	model := renderFixture("calm")
	model.Header.Delivery = "another-delivery"
	model.Detail.Task = "task_9"
	model.Detail.Attempt = 7
	model.Detail.AttemptLimit = 0
	model.Detail.CriterionTotal = 0
	model.Detail.CriterionTitle = "new criterion"
	model.LogTitle = "another-run"
	model.LogLines = []string{"custom log value"}
	model.Waves[2].Dependencies = []string{"task_9"}
	model.Progress.WavesTotal = 0
	got := Render(model, Style{Width: 120, Lang: "en", Glyphs: "unicode"})
	for _, want := range []string{"another-delivery", "task_9 · e7 ·", "Criterion  2 · new criterion", "Recent log · another-run", "custom log value", "after task_9"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing model value %q", want)
		}
	}
	if strings.Contains(got, "e7/4") || strings.Contains(got, "[██████") {
		t.Fatal("invented denominator")
	}
	for _, width := range []int{1, 30, 60, 80, 120} {
		model.Detail.Title = "宽标题 with an escaped control \x1b[31m"
		text := Render(model, Style{Width: width})
		for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			if panelWidth(line) != width || strings.ContainsRune(line, '\x1b') {
				t.Fatalf("width %d: %q", width, line)
			}
		}
	}
}

func TestStyleForWriterTerminalColour(t *testing.T) {
	previous := isTerminal
	t.Cleanup(func() { isTerminal = previous })
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, terminal := range []bool{false, true} {
		isTerminal = func(fd uintptr) bool { return terminal && fd == file.Fd() }
		for _, noColour := range []string{"", "1"} {
			t.Setenv("NO_COLOR", noColour)
			if got := StyleForWriter(file).Colour; got != (terminal && noColour == "") {
				t.Fatalf("terminal=%v NO_COLOR=%q: colour=%v", terminal, noColour, got)
			}
			if StyleForWriter(&strings.Builder{}).Colour {
				t.Fatal("writer without a descriptor gained colour")
			}
		}
	}
}
