package loop

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func renderFixture(state string) PanelView {
	m := PanelView{
		Header:    PanelHeader{Delivery: "roadmap-20260906-215846", Project: "core", Branch: "feat/roadmap", Head: "4c1e9f2", Phase: 2, State: "running", Elapsed: 14*time.Minute + 32*time.Second},
		Attention: PanelAttention{Kind: "none"},
		Context:   PanelContext{Executor: "codex", Model: "gpt-5.6-sol", Reasoning: "medium", TestCommand: "go test ./...", Sandbox: "workspace-write", Sessions: 2, PID: 99573},
		Progress:  PanelProgress{WavesDone: 1, WavesTotal: 4, TasksDone: 1, TasksTotal: 5, WavesShown: 1, TasksShown: 1},
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
				assertRenderGolden(t, state, Style{Width: width, Lang: "en", Glyphs: "unicode", Frame: -1}, "")
			})
		}
	}
}

func TestRenderSpinnerFrames(t *testing.T) {
	for _, test := range []struct {
		name, glyphs, frames string
	}{
		{name: "unicode", glyphs: "unicode", frames: "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"},
		{name: "ascii", glyphs: "ascii", frames: "|/-\\"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := renderFixture("calm")
			model.Attention = PanelAttention{Kind: "running", Task: "task_2", Text: "working"}
			for frame, want := range []rune(test.frames) {
				got := Render(model, Style{Width: 120, Lang: "en", Glyphs: test.glyphs, Frame: frame})
				glyph := string(want)
				for _, line := range []string{strings.SplitN(got, "\n", 2)[0], strings.Split(got, "\n")[1]} {
					if !strings.Contains(line, glyph) {
						t.Fatalf("frame %d missing %q in %q", frame, glyph, line)
					}
				}
				if strings.Count(got, glyph) < 4 {
					t.Fatalf("frame %d rendered %d running glyphs, want header, attention, wave, and task row:\n%s", frame, strings.Count(got, glyph), got)
				}
			}
			static := Render(model, Style{Width: 120, Lang: "en", Glyphs: test.glyphs, Frame: -1})
			if strings.Count(static, ">") < 4 {
				t.Fatalf("static frame did not retain running marker:\n%s", static)
			}
		})
	}
}

func TestRenderUsesShownProgress(t *testing.T) {
	model := renderFixture("calm")
	model.Progress.WavesShown = 1.5
	got := Render(model, Style{Width: 120, Lang: "en", Glyphs: "ascii", Frame: -1})
	if !strings.Contains(got, "Waves  1/4  [#########...............]  38%") {
		t.Fatalf("renderer did not use eased progress:\n%s", got)
	}
}
func TestRenderPortugueseLabels(t *testing.T) {
	assertRenderGolden(t, "calm", Style{Width: 120, Lang: "pt", Glyphs: "unicode", Frame: -1}, "-pt-unicode")
	focused := Render(renderFixture("calm"), Style{Width: 120, Lang: "pt", Glyphs: "unicode", Focus: string(focusLog)})
	if !strings.Contains(focused, "l log") || !strings.Contains(focused, "Log recente ·") || !strings.Contains(panelLegend(Style{Width: 120, Lang: "pt", Glyphs: "unicode"}), "· foco") {
		t.Fatal("Portuguese focus labels are incomplete")
	}
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

func TestRenderKeyLine(t *testing.T) {
	for _, width := range []int{120, 80} {
		got := Render(renderFixture("calm"), Style{Width: width, Lang: "en", Glyphs: "unicode", Focus: string(focusTable)})
		if !strings.Contains(got, "l log") {
			t.Fatalf("width %d key line omitted l log:\n%s", width, got)
		}
	}
	if got := panelLegend(Style{Width: 120, Lang: "en", Glyphs: "unicode"}); !strings.Contains(got, "· focused") {
		t.Fatalf("legend omitted focus marker:\n%s", got)
	}
}

func TestRenderLogOffset(t *testing.T) {
	model := renderFixture("calm")
	model.LogLines = []string{"one", "two", "three", "four", "five", "six", "seven"}
	got := Render(model, Style{Width: 120, Lang: "en", Glyphs: "unicode", LogOffset: 1})
	if !strings.Contains(got, "one") || strings.Contains(got, "seven") {
		t.Fatalf("offset did not move the six-line window:\n%s", got)
	}
}
func TestRenderASCIIFallback(t *testing.T) {
	assertRenderGolden(t, "calm", Style{Width: 120, Lang: "en", Glyphs: "ascii", Frame: -1}, "-en-ascii")
	t.Setenv("BATUTA_LANG", "en")
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "en_US.UTF-8")
	if got := StyleForWriter(&strings.Builder{}); got.Glyphs != "ascii" {
		t.Fatalf("style: %+v", got)
	}
}
func TestRenderColours(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sgr := regexp.MustCompile("\x1b\\[[0-9;]*m")
	for _, state := range []string{"calm", "blocked"} {
		style := Style{Width: 120, Lang: "en", Glyphs: "unicode", Colour: true}
		got := Render(renderFixture(state), style)
		if !strings.Contains(got, "\x1b[") {
			t.Fatal("Render did not honor explicit Colour")
		}
		for _, code := range sgr.FindAllString(got, -1) {
			for _, parameter := range strings.Split(code[2:len(code)-1], ";") {
				n, err := strconv.Atoi(parameter)
				if err != nil || !(n == 0 || n == 1 || n == 2 || n == 7 || n >= 30 && n <= 37 || n >= 90 && n <= 97) {
					t.Fatalf("unexpected SGR %q", code)
				}
			}
		}
		style.Colour = false
		plain := strings.ReplaceAll(sgr.ReplaceAllString(got, ""), "│▶", "│ ")
		if plain != Render(renderFixture(state), style) {
			t.Fatal("colour changed layout beyond the selection marker")
		}
	}
	if StyleForWriter(&strings.Builder{}).Colour {
		t.Fatal("non-TTY colour enabled")
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

func TestPanelWidthIgnoresSGR(t *testing.T) {
	for _, text := range []string{"hello", "宽字", "▶task_2"} {
		for _, codes := range []string{"7", "2;7", "1;31", "94"} {
			if got, want := panelWidth("\x1b["+codes+"m"+text+"\x1b[0m"), panelWidth(text); got != want {
				t.Errorf("%q: width=%d, want %d", text, got, want)
			}
		}
	}
}

type paintedCell struct {
	char               rune
	colour             int
	bold, dim, reverse bool
}

func paintedCells(t *testing.T, line string) []paintedCell {
	t.Helper()
	var cells []paintedCell
	var state paintedCell
	for len(line) > 0 {
		if strings.HasPrefix(line, "\x1b[") {
			end := strings.IndexByte(line, 'm')
			if end < 0 {
				t.Fatal("unterminated SGR")
			}
			for _, parameter := range strings.Split(line[2:end], ";") {
				code, err := strconv.Atoi(parameter)
				if err != nil {
					t.Fatal(err)
				}
				switch code {
				case 0:
					state = paintedCell{}
				case 1:
					state.bold = true
				case 2:
					state.dim = true
				case 7:
					state.reverse = true
				default:
					state.colour = code
				}
			}
			line = line[end+1:]
			continue
		}
		for _, c := range line {
			state.char = c
			cells = append(cells, state)
			line = line[len(string(c)):]
			break
		}
	}
	if state.colour != 0 || state.bold || state.dim || state.reverse {
		t.Fatal("paint leaked past end of line")
	}
	return cells
}

func paintLineContaining(t *testing.T, output, text string) string {
	t.Helper()
	sgr := regexp.MustCompile("\x1b\\[[0-9;]*m")
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(sgr.ReplaceAllString(line, ""), text) {
			return line
		}
	}
	t.Fatalf("missing line %q", text)
	return ""
}

func TestPaintSelectedRow(t *testing.T)         { testPaintSelection(t, "table", false) }
func TestPaintSelectedRowLogFocus(t *testing.T) { testPaintSelection(t, "log", true) }
func testPaintSelection(t *testing.T, focus string, dim bool) {
	t.Helper()
	for _, glyphs := range []string{"unicode", "ascii"} {
		for _, width := range []int{60, 80, 120} {
			for _, selected := range []string{"task_1", "task_2"} {
				model := renderFixture("calm")
				model.Detail.Task = selected
				got := Render(model, Style{Width: width, Glyphs: glyphs, Colour: true, Focus: focus, Frame: -1})
				marker, border := "▶", "│"
				if glyphs == "ascii" {
					marker, border = ">", "|"
				}
				line := paintLineContaining(t, got, border+marker+selected)
				cells := paintedCells(t, line)
				if len(cells) != width {
					t.Fatalf("row width=%d, want %d", len(cells), width)
				}
				for i, cell := range cells {
					if !cell.reverse || dim && !cell.dim {
						t.Fatalf("selection lost at column %d: %+v", i, cell)
					}
				}
				for _, other := range []string{"task_1", "task_2", "task_3"} {
					if other == selected {
						continue
					}
					for _, cell := range paintedCells(t, paintLineContaining(t, got, border+" "+other)) {
						if cell.reverse {
							t.Fatalf("unselected row %s reversed", other)
						}
					}
				}
			}
		}
	}
}

func TestPaintAttentionBanner(t *testing.T) {
	for _, tc := range []struct {
		state   string
		colour  int
		reverse bool
	}{
		{"calm", 0, false}, {"question", 33, true}, {"limit", 33, true}, {"blocked", 31, true}, {"conflict", 31, true},
	} {
		for _, width := range []int{30, 60, 120} {
			got := Render(renderFixture(tc.state), Style{Width: width, Colour: true, Frame: -1})
			cells := paintedCells(t, strings.Split(got, "\n")[1])
			if len(cells) != width {
				t.Fatalf("banner width=%d", len(cells))
			}
			for _, cell := range cells {
				if cell.colour != tc.colour || cell.reverse != tc.reverse || tc.state == "calm" && !cell.dim {
					t.Fatalf("%s banner: %+v", tc.state, cell)
				}
			}
		}
	}
}

func TestPaintStates(t *testing.T) {
	for _, tc := range []struct {
		state     string
		colour    int
		bold, dim bool
	}{
		{"integrated", 32, false, false}, {"running", 34, false, false}, {"executor", 34, false, false},
		{"preparing", 34, false, false}, {"candidate", 34, false, false}, {"blocked", 31, true, false},
		{"failed", 31, true, false}, {"conflict", 31, true, false}, {"waiting_input", 33, true, false},
		{"pending", 0, false, true},
	} {
		for _, lang := range []string{"en", "pt"} {
			model := renderFixture("calm")
			model.Header.State = tc.state
			model.Attention = PanelAttention{Kind: tc.state, Task: "task_2", Text: "attention"}
			model.Waves[1].State = tc.state
			model.Waves[1].Rows[0].State = tc.state
			got := Render(model, Style{Width: 120, Lang: lang, Colour: true, Frame: -1})
			r := panelRenderer{style: Style{Frame: -1}, g: glyphsFor(Style{}), labels: panelLabels[lang]}
			visibleStatus := r.status(tc.state, true)
			if lang == "pt" && tc.state == "waiting_input" {
				visibleStatus = "? aguarda respos…"
			}
			for _, location := range []struct{ line, token string }{
				{strings.Split(got, "\n")[0], r.status(tc.state, false)},
				{strings.Split(got, "\n")[1], r.stateGlyph(tc.state)},
				{paintLineContaining(t, got, "│▶task_2"), visibleStatus},
				{paintLineContaining(t, got, "│ W2"), r.stateGlyph(tc.state) + " 0/1"},
			} {
				cells := paintedCells(t, location.line)
				var plain strings.Builder
				for _, cell := range cells {
					plain.WriteRune(cell.char)
				}
				start := strings.Index(plain.String(), location.token)
				if start < 0 {
					t.Fatalf("missing %q", location.token)
				}
				start = len([]rune(plain.String()[:start]))
				for _, cell := range cells[start : start+len([]rune(location.token))] {
					if cell.colour != tc.colour || tc.bold && !cell.bold || tc.dim && !cell.dim {
						t.Fatalf("%s/%s %q: %+v", tc.state, lang, location.token, cell)
					}
				}
			}
		}
	}
}

func TestPaintWaves(t *testing.T) {
	model := renderFixture("calm")
	model.Waves = append(model.Waves, PanelWave{State: "before_run", Done: 1, Total: 1, Rows: []PanelRow{{Task: "task_0", Title: "previous work", State: "integrated"}}}, PanelWave{State: "pending", Dependencies: []string{"task_4"}})
	got := Render(model, Style{Width: 120, Colour: true, Frame: -1})
	for _, tc := range []struct {
		text      string
		bold, dim bool
		colour    int
	}{
		{"│ W1", true, false, 0}, {"│ W2", true, false, 34}, {"│ W3", true, false, 0},
		{"before run", false, true, 0}, {"│ task_0", false, true, 0}, {"│ pending", false, true, 0},
	} {
		cells := paintedCells(t, paintLineContaining(t, got, tc.text))
		for _, cell := range cells {
			if tc.bold && !cell.bold || tc.dim && !cell.dim {
				t.Fatalf("%s: %+v", tc.text, cell)
			}
		}
		if tc.colour != 0 && cells[2].colour != tc.colour {
			t.Fatalf("active wave not blue: %+v", cells[2])
		}
	}
}

func TestPaintGates(t *testing.T) {
	for _, glyphs := range []string{"unicode", "ascii"} {
		model := renderFixture("calm")
		model.Waves[1].Rows[0].Gates = [4]string{"pass", "fail", "pending", "silent"}
		got := Render(model, Style{Width: 120, Glyphs: glyphs, Colour: true, Frame: -1})
		marker, gates := "│▶task_2", "✓✗·✓"
		if glyphs == "ascii" {
			marker, gates = "|>task_2", "+x.+"
		}
		line := paintLineContaining(t, got, marker)
		cells := paintedCells(t, line)
		var plain strings.Builder
		for _, cell := range cells {
			plain.WriteRune(cell.char)
		}
		start := strings.Index(plain.String(), gates)
		if start < 0 {
			t.Fatalf("missing gates %q", gates)
		}
		start = len([]rune(plain.String()[:start]))
		for i, colour := range []int{32, 31, 0, 32} {
			cell := cells[start+i]
			if cell.colour != colour || i == 1 && !cell.bold || i == 2 && !cell.dim || !cell.reverse {
				t.Fatalf("gate %d: %+v", i, cell)
			}
		}
	}
}
