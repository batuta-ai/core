package loop

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/term"
)

// Style fixes the layout independently of the process environment. Use
// StyleForWriter to choose terminal capabilities; NO_COLOR always takes priority.
type Style struct {
	Width        int
	Lang, Glyphs string
	Colour       bool
}

func StyleForWriter(w io.Writer) Style {
	locale := os.Getenv("LC_ALL")
	if locale == "" {
		locale = os.Getenv("LANG")
	}
	lang := os.Getenv("BATUTA_LANG")
	if lang == "" {
		lang = locale
	}
	style := Style{Width: 120, Lang: "en", Glyphs: "ascii"}
	if strings.HasPrefix(strings.ToLower(lang), "pt") {
		style.Lang = "pt"
	}
	if strings.Contains(strings.ToLower(locale), "utf-8") || strings.Contains(strings.ToLower(locale), "utf8") {
		style.Glyphs = "unicode"
	}
	if file, ok := w.(interface{ Fd() uintptr }); ok {
		style.Width, _ = TerminalSize(file.Fd())
		style.Colour = isTerminal(file.Fd()) && os.Getenv("NO_COLOR") == ""
	}
	return style
}

// TerminalSize reports the visible terminal size, falling back to 120 by 40.
func TerminalSize(fd uintptr) (width, height int) {
	width, height, err := term.GetSize(fd)
	if err != nil || width <= 0 || height <= 0 {
		return 120, 40
	}
	return width, height
}

type panelGlyphs struct{ h, v, tl, tr, bl, br, ok, fail, pend, full, empty, ell, arrow string }

func glyphsFor(style Style) panelGlyphs {
	if style.Glyphs == "ascii" {
		return panelGlyphs{"-", "|", "+", "+", "+", "+", "+", "x", ".", "#", ".", "~", "->"}
	}
	return panelGlyphs{"─", "│", "┌", "┐", "└", "┘", "✓", "✗", "·", "█", "░", "…", "→"}
}

type panelRenderer struct {
	style  Style
	g      panelGlyphs
	labels map[string]string
}

// Render draws only the supplied snapshot; it never reads journals or run logs.
func Render(model PanelView, style Style) string {
	if style.Width <= 0 {
		style.Width = 120
	}
	if model.message != "" {
		return model.message + "\n"
	}
	labels := panelLabels[style.Lang]
	if labels == nil {
		labels = panelLabels["en"]
	}
	r := panelRenderer{style, glyphsFor(style), labels}
	width := style.Width
	state := model.Header.State
	if model.Attention.Kind == "conflict" || model.Attention.Kind == "escalated" {
		state = model.Attention.Kind
	}
	status := r.status(state, false) + " · " + renderElapsed(model.Header.Elapsed)
	left := " batuta watch"
	if width >= 76 {
		left += " · " + model.Header.Project
	}
	if width >= 100 {
		left = " batuta watch · " + model.Header.Delivery + " · " + model.Header.Project
	}
	if width >= 76 && (model.Header.Roadmap != "" || model.Header.Phase > 0) {
		left += fmt.Sprintf(" · %s %d", labels["phase"], model.Header.Phase)
	}
	right := status + " "
	if width >= 76 {
		right = model.Header.Branch + " @ " + panelCommit(model.Header.Head) + " · " + right
	}
	if panelWidth(right) > width {
		right = r.fit(right, width)
	}
	out := []string{r.fit(left, width-panelWidth(right)) + right}
	pieces := strings.Split(r.attention(model.Attention), " · ")
	for len(pieces) > 1 && panelWidth(" "+strings.Join(pieces, " · ")) > width {
		pieces = pieces[:len(pieces)-1]
	}
	out = append(out, r.fit(" "+strings.Join(pieces, " · "), width))
	c := model.Context
	pid := "–"
	if style.Glyphs == "ascii" {
		pid = "-"
	}
	if c.PID != 0 {
		pid = fmt.Sprint(c.PID)
	}
	ctx := []string{strings.TrimSpace(c.Executor+" "+c.Model) + " · " + c.Reasoning + " · " + c.TestCommand + " · " + c.Sandbox, fmt.Sprintf("%d %s · %d %s · %d %s · PID %s", c.Sessions, labels["sessions"], c.Retries, labels["retries"], c.Escalations, labels["escalations"], pid)}
	p := model.Progress
	prog := []string{r.progress(labels["waves"], p.WavesDone, p.WavesTotal), r.progress(labels["tasks"], p.TasksDone, p.TasksTotal)}
	detail := r.detail(model.Detail)
	var cols []int
	var headings []string
	switch {
	case width >= 100:
		lw := width/2 - 1
		a, b := r.box(labels["context"], ctx, lw), r.box(labels["progress"], prog, width-lw-1)
		for i := range a {
			out = append(out, a[i]+" "+b[i])
		}
		out = append(out, r.box(labels["detail"], detail, width)...)
		cols = []int{7, width - 7 - 17 - 7 - 6 - 8 - 14, 17, 7, 6, 8}
		headings = []string{labels["id"], labels["task"], labels["status"], labels["attempt"], labels["gates"], labels["commit"]}
	case width >= 76:
		out = append(out, r.box(labels["context"], append(ctx, prog...), width)...)
		out = append(out, r.box(labels["detail"], detail[:3], width)...)
		cols = []int{7, width - 7 - 15 - 5 - 6 - 10, 15, 5, 6}
		headings = []string{labels["id"], labels["task"], labels["status"], labels["tr"], labels["gates"]}
	default:
		out = append(out, r.box(labels["progress"], []string{prog[0], detail[0]}, width)...)
		cols = []int{7, max(0, width-7-13-6-8), 13, 6}
		headings = []string{labels["id"], labels["task"], labels["status"], labels["gates"]}
	}
	table := []string{r.row(headings, cols)}
	for _, wave := range model.Waves {
		title := ""
		if wave.Base != "" {
			title = labels["base"] + " " + panelCommit(wave.Base)
		}
		if wave.Integrated != "" {
			title += " " + r.g.arrow + " " + labels["integrated"] + " " + panelCommit(wave.Integrated)
		}
		if len(wave.Dependencies) > 0 {
			title = labels["after"] + " " + strings.Join(wave.Dependencies, ", ")
		}
		id := fmt.Sprintf("W%d", wave.Number)
		if wave.Number == 0 {
			id = labels["pending"]
			if wave.State == "before_run" {
				id, title = "", labels["before_run"]
			}
		}
		table = append(table, r.tableRow([]string{id, title, fmt.Sprintf("%s %d/%d", r.stateGlyph(wave.State), wave.Done, wave.Total), "", "", ""}, cols))
		for _, task := range wave.Rows {
			state := task.State
			if model.Attention.Task == task.Task && model.Attention.Kind != "none" && model.Attention.Kind != "" {
				state = model.Attention.Kind
			}
			gates := ""
			for _, gate := range task.Gates {
				switch gate {
				case "pass", "silent":
					gates += r.g.ok
				case "fail":
					gates += r.g.fail
				default:
					gates += r.g.pend
				}
			}
			commit := ""
			if task.Commit != "" {
				commit = panelCommit(task.Commit)
			}
			table = append(table, r.tableRow([]string{task.Task, "  " + task.Title, r.status(state, true), renderAttempt(task.Attempt, task.AttemptLimit), gates, commit}, cols))
		}
	}
	out = append(out, r.box(labels["table"], table, width)...)
	keys := labels["keys"]
	if width >= 100 {
		out = append(out, r.fit(fmt.Sprintf(" ^ %d %s · v %d %s", model.RowsAbove, labels["above"], model.RowsBelow, labels["below"]), width-panelWidth(keys)-1)+keys+" ")
	} else {
		out = append(out, r.fit(fmt.Sprintf(" ^ %d · v %d · ", model.RowsAbove, model.RowsBelow)+strings.Split(keys, " · ")[0]+" · ? · q", width))
	}
	if width >= 76 {
		count := 3
		if width >= 100 {
			count = 6
		}
		title := labels["logs"]
		if model.LogTitle != "" {
			title += " · " + model.LogTitle
		}
		out = append(out, r.box(title, model.LogLines[max(0, len(model.LogLines)-count):], width)...)
	}
	for i, line := range out {
		line = r.fit(expandPanelTabs(line), width)
		if style.Glyphs == "ascii" {
			line = strings.ReplaceAll(line, "·", "-")
		}
		if style.Colour && os.Getenv("NO_COLOR") == "" {
			line = r.colour(line)
		}
		out[i] = line
	}
	return strings.Join(out, "\n") + "\n"
}

func (r panelRenderer) attention(a PanelAttention) string {
	if a.Kind == "" || a.Kind == "none" {
		return r.labels["needs_none"]
	}
	text := r.stateGlyph(a.Kind) + " "
	switch a.Kind {
	case "question":
		text += a.Task + " " + r.labels["question"] + " · \"" + a.Text + "\""
	case "blocked":
		text += a.Task + " " + r.labels["blocked"]
		if a.Text != "" {
			text += " · " + a.Text
		}
	default:
		if a.Task != "" {
			text += a.Task + " "
		}
		text += a.Text
	}
	if a.Hint != "" {
		hint := a.Hint
		switch hint {
		case "r answers":
			hint = r.labels["answer"]
		case "o opens the log":
			hint = r.labels["openlog"]
		}
		text += " · " + hint
	}
	return text
}
func (r panelRenderer) detail(d PanelDetail) []string {
	first := d.Task
	if attempt := renderAttempt(d.Attempt, d.AttemptLimit); attempt != "" {
		first += " · " + attempt
	}
	if d.Title != "" {
		first += " · " + d.Title
	}
	criterion := fmt.Sprint(d.Criterion)
	if d.CriterionTotal > 0 {
		criterion += fmt.Sprintf("/%d", d.CriterionTotal)
	}
	second := r.labels["criterion"] + "  " + criterion
	if d.CriterionTitle != "" {
		second += " · " + d.CriterionTitle
	}
	if d.Question != "" {
		second = r.labels["question"] + "  " + d.Question
	} else if d.Reason != "" {
		second = r.labels["reason"] + "  " + d.Reason
	}
	return []string{first, second, r.labels["last"] + "  " + d.LastRecord + " · " + renderAge(d.LastAge), r.labels["worktree"] + "  " + d.Worktree, r.labels["log"] + "  " + d.LogPath}
}
func renderAttempt(n, total int) string {
	if n <= 0 {
		return ""
	}
	if total <= 0 {
		return fmt.Sprintf("e%d", n)
	}
	return fmt.Sprintf("e%d/%d", n, total)
}
func renderElapsed(d time.Duration) string {
	s := max(0, int(d/time.Second))
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}
func renderAge(d time.Duration) string {
	s := max(0, int(d/time.Second))
	if s >= 3600 {
		return fmt.Sprintf("%dh", s/3600)
	}
	if s >= 60 {
		return fmt.Sprintf("%dm", s/60)
	}
	return fmt.Sprintf("%ds", s)
}
func (r panelRenderer) stateGlyph(state string) string {
	switch state {
	case "integrated", "done", "pass", "before_run":
		return r.g.ok
	case "blocked", "fail", "failed":
		return r.g.fail
	case "waiting_input", "question":
		return "?"
	case "limit_wait", "limit":
		return "~"
	case "conflict":
		return "<"
	case "escalated":
		return "^"
	case "pending", "":
		return r.g.pend
	default:
		return ">"
	}
}
func (r panelRenderer) status(state string, task bool) string {
	key := state
	switch state {
	case "waiting_input":
		key = "question"
	case "limit_wait":
		key = "limit"
	case "done":
		key = "integrated"
	case "running", "preparing":
		key = "running"
		if task {
			key = "executor"
		}
	}
	label := r.labels[key]
	if label == "" {
		label = state
	}
	return r.stateGlyph(state) + " " + label
}
func (r panelRenderer) progress(label string, done, total int) string {
	if total <= 0 {
		return fmt.Sprintf("%s  %d", label, done)
	}
	fraction := float64(max(0, min(done, total))) / float64(total)
	full := int(math.RoundToEven(24 * fraction))
	return fmt.Sprintf("%s  %d/%d  [%s%s]  %d%%", label, done, total, strings.Repeat(r.g.full, full), strings.Repeat(r.g.empty, 24-full), int(math.RoundToEven(100*fraction)))
}
func (r panelRenderer) tableRow(values []string, cols []int) string {
	if len(cols) == 5 {
		values = values[:5]
	} else if len(cols) == 4 {
		values = []string{values[0], values[1], values[2], values[4]}
	}
	return r.row(values, cols)
}
func (r panelRenderer) row(values []string, cols []int) string {
	out := make([]string, len(cols))
	for i, n := range cols {
		out[i] = r.fit(values[i], n)
	}
	return strings.Join(out, " ")
}
func (r panelRenderer) box(title string, rows []string, width int) []string {
	inner := max(0, width-2)
	title = strings.TrimRight(r.fit(title, max(0, inner-3)), " ")
	out := []string{r.g.tl + r.g.h + " " + title + " " + strings.Repeat(r.g.h, max(0, inner-panelWidth(title)-3)) + r.g.tr}
	for _, row := range rows {
		out = append(out, r.g.v+" "+r.fit(row, max(0, inner-2))+" "+r.g.v)
	}
	return append(out, r.g.bl+strings.Repeat(r.g.h, inner)+r.g.br)
}
func (r panelRenderer) fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	// Journal values are text, never terminal control sequences. Tabs retain the
	// generator's expansion order: after boxing and before the final width fit.
	s = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && c != '\t' {
			return ' '
		}
		return c
	}, s)
	if panelWidth(s) > n {
		var b strings.Builder
		used := 0
		for _, c := range s {
			w := panelRuneWidth(c)
			if used+w > n-1 {
				break
			}
			b.WriteRune(c)
			used += w
		}
		s = b.String() + r.g.ell
	}
	return s + strings.Repeat(" ", max(0, n-panelWidth(s)))
}
func panelWidth(s string) int {
	n := 0
	for _, c := range s {
		n += panelRuneWidth(c)
	}
	return n
}
func panelRuneWidth(c rune) int {
	// Fullwidth forms, CJK, Hangul and wide emoji occupy two terminal cells.
	if c >= 0x1100 && (c <= 0x115f || c == 0x2329 || c == 0x232a || c >= 0x2e80 && c <= 0xa4cf && c != 0x303f || c >= 0xac00 && c <= 0xd7a3 || c >= 0xf900 && c <= 0xfaff || c >= 0xfe10 && c <= 0xfe19 || c >= 0xfe30 && c <= 0xfe6f || c >= 0xff01 && c <= 0xff60 || c >= 0xffe0 && c <= 0xffe6 || c >= 0x1f300 && c <= 0x1faff || c >= 0x20000 && c <= 0x3fffd) {
		return 2
	}
	return 1
}
func expandPanelTabs(s string) string {
	var b strings.Builder
	column := 0
	for _, c := range s {
		if c == '\t' {
			n := 4 - column%4
			b.WriteString(strings.Repeat(" ", n))
			column += n
		} else {
			b.WriteRune(c)
			column++
		}
	}
	return b.String()
}
func (r panelRenderer) colour(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		end := i + 1
		for end < len(runes) && !unicode.IsSpace(runes[end]) {
			end++
		}
		token := runes[i:end]
		gates := len(token) == 4
		for _, c := range token {
			if string(c) != r.g.ok && string(c) != r.g.fail && string(c) != r.g.pend {
				gates = false
			}
		}
		for _, c := range token {
			code := ""
			if len(token) == 1 || gates {
				switch string(c) {
				case r.g.ok:
					code = "32"
				case r.g.fail:
					code = "31"
				case ">", "?", "~", "<", "^":
					code = "34"
				}
			}
			if code != "" {
				b.WriteString("\x1b[" + code + "m")
				b.WriteRune(c)
				b.WriteString("\x1b[0m")
			} else {
				b.WriteRune(c)
			}
		}
		i = end
	}
	return b.String()
}
