package loop

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
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
	Frame        int
	Focus        string
	LogOffset    int
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
	style := Style{Width: 120, Lang: "en", Glyphs: "ascii", Frame: -1}
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

type paintKind uint8

type lineKind paintKind

const (
	paintPlain paintKind = iota
	paintSelected
	paintSelectedUnfocused
	paintStateIntegrated
	paintStateRunning
	paintStateBlocked
	paintStateWaiting
	paintStatePending
	paintWaveActive
	paintWave
	paintDim
	paintAttentionWaiting
	paintAttentionBlocked
	paintBold
	paintChip
	paintLogProgress
	paintLogError
	paintLogPrompt
	paintBarDone
	paintBarRunning
	paintBarEasing
	paintBorder
	paintFocusBorder
)

var paintTable = map[paintKind]string{
	paintSelected:          "7",
	paintSelectedUnfocused: "2;7",
	paintStateIntegrated:   "32",
	paintStateRunning:      "34",
	paintStateBlocked:      "1;31",
	paintStateWaiting:      "1;33",
	paintStatePending:      "2",
	paintWaveActive:        "1;34",
	paintWave:              "1",
	paintDim:               "2",
	paintAttentionWaiting:  "1;33;7",
	paintAttentionBlocked:  "1;31;7",
	paintBold:              "1",
	paintChip:              "7",
	paintLogProgress:       "1;36",
	paintLogError:          "31",
	paintLogPrompt:         "1",
	paintBarDone:           "32",
	paintBarRunning:        "34",
	paintBarEasing:         "94",
	paintBorder:            "2",
	paintFocusBorder:       "34",
}

type segment struct {
	text string
	kind paintKind
}

type panelLine struct {
	segments []segment
	kind     lineKind
}

func textLine(text string) panelLine {
	return panelLine{segments: []segment{{text: text}}}
}

func textLines(texts []string) []panelLine {
	lines := make([]panelLine, len(texts))
	for i, text := range texts {
		lines[i] = textLine(text)
	}
	return lines
}

func (line panelLine) text() string {
	var b strings.Builder
	for _, part := range line.segments {
		b.WriteString(part.text)
	}
	return b.String()
}

func stateLine(text, state string) panelLine {
	return panelLine{segments: []segment{{text, statePaint(state)}}}
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
	left := textLine(" batuta watch")
	if width >= 76 {
		left.segments = append(left.segments, segment{text: " · " + model.Header.Project})
	}
	if width >= 100 {
		left.segments = []segment{{text: " batuta watch · "}, {model.Header.Delivery, paintBold}, {text: " · " + model.Header.Project}}
	}
	if width >= 76 && (model.Header.Roadmap != "" || model.Header.Phase > 0) {
		left.segments = append(left.segments, segment{text: fmt.Sprintf(" · %s %d", labels["phase"], model.Header.Phase)})
	}
	right := stateLine(r.status(state, false), state)
	right.segments = append(right.segments, segment{" · " + renderElapsed(model.Header.Elapsed) + " ", paintDim})
	if width >= 76 {
		right.segments = append([]segment{{model.Header.Branch + " @ " + panelCommit(model.Header.Head) + " · ", paintDim}}, right.segments...)
	}
	if panelWidth(right.text()) > width {
		right = r.fitLine(right, width)
	}
	header := r.fitLine(left, width-panelWidth(right.text()))
	header.segments = append(header.segments, right.segments...)
	out := []panelLine{header}
	attention := r.attention(model.Attention)
	pieces := strings.Split(attention.text(), " · ")
	for len(pieces) > 1 && panelWidth(" "+strings.Join(pieces, " · ")) > width {
		pieces = pieces[:len(pieces)-1]
	}
	attention.segments = []segment{{text: " " + strings.Join(pieces, " · ")}}
	out = append(out, r.fitLine(attention, width))
	c := model.Context
	pid := "–"
	if style.Glyphs == "ascii" {
		pid = "-"
	}
	if c.PID != 0 {
		pid = fmt.Sprint(c.PID)
	}
	ctx := textLines([]string{strings.TrimSpace(c.Executor+" "+c.Model) + " · " + c.Reasoning + " · " + c.TestCommand + " · " + c.Sandbox, fmt.Sprintf("%d %s · %d %s · %d %s · PID %s", c.Sessions, labels["sessions"], c.Retries, labels["retries"], c.Escalations, labels["escalations"], pid)})
	p := model.Progress
	prog := []panelLine{r.progress(labels["waves"], p.WavesShown, p.WavesDone, p.WavesTotal), r.progress(labels["tasks"], p.TasksShown, p.TasksDone, p.TasksTotal)}
	detail := r.detail(model.Detail)
	var cols []int
	var headings []string
	switch {
	case width >= 100:
		lw := width/2 - 1
		a, b := r.boxLines(labels["context"], ctx, lw), r.boxLines(labels["progress"], prog, width-lw-1)
		for i := range a {
			joined := a[i]
			joined.segments = append(joined.segments, segment{text: " "})
			joined.segments = append(joined.segments, b[i].segments...)
			out = append(out, joined)
		}
		out = append(out, r.boxLines(labels["detail"], detail, width)...)
		cols = []int{7, width - 7 - 17 - 7 - 6 - 8 - 14, 17, 7, 6, 8}
		headings = []string{labels["id"], labels["task"], labels["status"], labels["attempt"], labels["gates"], labels["commit"]}
	case width >= 76:
		out = append(out, r.boxLines(labels["context"], append(ctx, prog...), width)...)
		out = append(out, r.boxLines(labels["detail"], detail[:3], width)...)
		cols = []int{7, width - 7 - 15 - 5 - 6 - 10, 15, 5, 6}
		headings = []string{labels["id"], labels["task"], labels["status"], labels["tr"], labels["gates"]}
	default:
		out = append(out, r.boxLines(labels["progress"], []panelLine{prog[0], detail[0]}, width)...)
		cols = []int{7, max(0, width-7-13-6-8), 13, 6}
		headings = []string{labels["id"], labels["task"], labels["status"], labels["gates"]}
	}
	table := []panelLine{r.row(textLines(headings), cols)}
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
		waveLine := r.tableRow([]panelLine{textLine(id), textLine(title), stateLine(fmt.Sprintf("%s %d/%d", r.stateGlyph(wave.State), wave.Done, wave.Total), wave.State), {}, {}, {}}, cols)
		waveLine.kind = lineKind(paintWave)
		if wave.State == "running" {
			waveLine.kind = lineKind(paintWaveActive)
		} else if wave.Number == 0 {
			waveLine.kind = lineKind(paintDim)
		}
		table = append(table, waveLine)
		for _, task := range wave.Rows {
			state := task.State
			if model.Attention.Task == task.Task && model.Attention.Kind != "none" && model.Attention.Kind != "" {
				state = model.Attention.Kind
			}
			gates := panelLine{}
			for _, gate := range task.Gates {
				part := segment{r.g.pend, paintStatePending}
				switch gate {
				case "pass", "silent":
					part = segment{r.g.ok, paintStateIntegrated}
				case "fail":
					part = segment{r.g.fail, paintStateBlocked}
				}
				gates.segments = append(gates.segments, part)
			}
			commit := ""
			if task.Commit != "" {
				commit = panelCommit(task.Commit)
			}
			taskLine := r.tableRow([]panelLine{textLine(task.Task), textLine("  " + task.Title), stateLine(r.status(state, true), state), textLine(renderAttempt(task.Attempt, task.AttemptLimit)), gates, textLine(commit)}, cols)
			if wave.State == "before_run" || task.State == "pending" {
				taskLine.kind = lineKind(paintDim)
			}
			if task.Task != "" && task.Task == model.Detail.Task {
				taskLine.kind = lineKind(paintSelected)
				if style.Focus == string(focusLog) {
					taskLine.kind = lineKind(paintSelectedUnfocused)
				}
			}
			table = append(table, taskLine)
		}
	}
	tableTitle := labels["table"]
	if style.Focus == string(focusTable) {
		tableTitle += " ·"
	}
	out = append(out, r.boxLines(tableTitle, table, width, style.Focus == string(focusTable))...)
	keys := labels["keys"]
	if style.Focus != "" {
		keys = labels["keys_focus"]
	}
	if width >= 100 {
		keyLine := r.fitLine(textLine(fmt.Sprintf(" ^ %d %s · v %d %s", model.RowsAbove, labels["above"], model.RowsBelow, labels["below"])), width-panelWidth(keys)-1)
		keyLine.segments = append(keyLine.segments, r.keyLine(keys+" ").segments...)
		out = append(out, keyLine)
	} else {
		keyParts := strings.Split(keys, " · ")
		compactKeys := keyParts[0]
		if style.Focus != "" && len(keyParts) > 1 {
			compactKeys += " · " + keyParts[1]
		}
		keyLine := textLine(fmt.Sprintf(" ^ %d · v %d · ", model.RowsAbove, model.RowsBelow))
		keyLine.segments = append(keyLine.segments, r.keyLine(compactKeys+" · ? · q").segments...)
		out = append(out, r.fitLine(keyLine, width))
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
		if style.Focus == string(focusLog) {
			title += " ·"
		}
		end := max(0, len(model.LogLines)-max(0, style.LogOffset))
		start := max(0, end-count)
		out = append(out, r.boxLines(title, r.logLines(model.LogLines[start:end]), width, style.Focus == string(focusLog))...)
	}
	painted := make([]string, len(out))
	for i, line := range out {
		line = r.fitLine(expandLineTabs(line), width)
		if style.Glyphs == "ascii" {
			for j := range line.segments {
				line.segments[j].text = strings.ReplaceAll(line.segments[j].text, "·", "-")
			}
		}
		painted[i] = r.paint(line)
	}
	return strings.Join(painted, "\n") + "\n"
}

func (r panelRenderer) attention(a PanelAttention) panelLine {
	if a.Kind == "" || a.Kind == "none" {
		return panelLine{segments: []segment{{text: r.labels["needs_none"]}}, kind: lineKind(paintDim)}
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
	kind := statePaint(a.Kind)
	switch kind {
	case paintStateWaiting:
		kind = paintAttentionWaiting
	case paintStateBlocked:
		kind = paintAttentionBlocked
	}
	return panelLine{segments: []segment{{text: text}}, kind: lineKind(kind)}
}
func (r panelRenderer) detail(d PanelDetail) []panelLine {
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
	label, value, kind := "criterion", criterion, paintPlain
	if d.CriterionTitle != "" {
		value += " · " + d.CriterionTitle
	}
	if d.Question != "" {
		label, value, kind = "question", d.Question, paintStateWaiting
	} else if d.Reason != "" {
		label, value = "reason", d.Reason
	}
	labelled := func(key, value string, kind paintKind) panelLine {
		return panelLine{segments: []segment{{r.labels[key], paintDim}, {text: "  "}, {value, kind}}}
	}
	return []panelLine{textLine(first), labelled(label, value, kind),
		labelled("last", d.LastRecord+" · "+renderAge(d.LastAge), paintPlain),
		labelled("worktree", d.Worktree, paintPlain), labelled("log", d.LogPath, paintPlain)}
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
func statePaint(state string) paintKind {
	switch state {
	case "integrated", "done", "pass":
		return paintStateIntegrated
	case "running", "executor", "candidate", "preparing":
		return paintStateRunning
	case "blocked", "fail", "failed", "conflict", "escalated":
		return paintStateBlocked
	case "waiting_input", "question", "limit_wait", "limit":
		return paintStateWaiting
	default:
		return paintStatePending
	}
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
	case "running", "preparing":
		if r.style.Frame < 0 {
			return ">"
		}
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		if r.style.Glyphs == "ascii" {
			frames = []rune("|/-\\")
		}
		return string(frames[r.style.Frame%len(frames)])
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
func (r panelRenderer) progress(label string, shown float64, done, total int) panelLine {
	if total <= 0 {
		return textLine(fmt.Sprintf("%s  %d", label, done))
	}
	fraction := max(0, min(shown, float64(total))) / float64(total)
	cells := 24 * fraction
	full := int(math.RoundToEven(cells))
	kind := paintBarRunning
	if fraction == 1 {
		kind = paintBarDone
	}
	stable := int(math.Floor(cells))
	line := panelLine{segments: []segment{{text: fmt.Sprintf("%s  %d/%d  [", label, done, total)}, {strings.Repeat(r.g.full, stable), kind}}}
	empty := 24 - full
	if cells > float64(stable) {
		glyph := r.g.full
		if full == stable {
			glyph = r.g.empty
			empty--
		}
		line.segments = append(line.segments, segment{glyph, paintBarEasing})
	}
	line.segments = append(line.segments, segment{text: strings.Repeat(r.g.empty, empty) + "]  "}, segment{fmt.Sprintf("%d%%", int(math.RoundToEven(100*fraction))), paintBold})
	return line
}
func (r panelRenderer) tableRow(values []panelLine, cols []int) panelLine {
	if len(cols) == 5 {
		values = values[:5]
	} else if len(cols) == 4 {
		values = []panelLine{values[0], values[1], values[2], values[4]}
	}
	return r.row(values, cols)
}
func (r panelRenderer) row(values []panelLine, cols []int) panelLine {
	out := panelLine{}
	for i, n := range cols {
		if i > 0 {
			out.segments = append(out.segments, segment{text: " "})
		}
		out.segments = append(out.segments, r.fitLine(values[i], n).segments...)
	}
	return out
}

// The legend also uses this plain-text box entry point.
func (r panelRenderer) box(title string, rows []string, width int) []string {
	lines := r.boxLines(title, textLines(rows), width)
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line.text()
	}
	return out
}

func (r panelRenderer) boxLines(title string, rows []panelLine, width int, focused ...bool) []panelLine {
	border := paintBorder
	if len(focused) > 0 && focused[0] {
		border = paintFocusBorder
	}
	inner := max(0, width-2)
	title = strings.TrimRight(r.fit(title, max(0, inner-3)), " ")
	out := []panelLine{{segments: []segment{{r.g.tl + r.g.h + " ", border}, {title, paintBold}, {" " + strings.Repeat(r.g.h, max(0, inner-panelWidth(title)-3)) + r.g.tr, border}}}}
	for _, row := range rows {
		padding := " "
		if r.style.Colour && (row.kind == lineKind(paintSelected) || row.kind == lineKind(paintSelectedUnfocused)) {
			padding = "▶"
			if r.style.Glyphs == "ascii" {
				padding = ">"
			}
		}
		line := panelLine{segments: []segment{{r.g.v, border}, {text: padding}}, kind: row.kind}
		line.segments = append(line.segments, r.fitLine(row, max(0, inner-2)).segments...)
		line.segments = append(line.segments, segment{text: " "}, segment{r.g.v, border})
		out = append(out, line)
	}
	return append(out, panelLine{segments: []segment{{r.g.bl + strings.Repeat(r.g.h, inner) + r.g.br, border}}})
}

func (r panelRenderer) fit(s string, n int) string {
	return r.fitLine(textLine(s), n).text()
}

func (r panelRenderer) fitLine(line panelLine, n int) panelLine {
	out := panelLine{kind: line.kind}
	if n <= 0 {
		return out
	}
	// Journal values are text, never terminal control sequences. Tabs retain the
	// generator's expansion order: after boxing and before the final width fit.
	clean := make([]segment, len(line.segments))
	for i, part := range line.segments {
		part.text = strings.Map(func(c rune) rune {
			if unicode.IsControl(c) && c != '\t' {
				return ' '
			}
			return c
		}, part.text)
		clean[i] = part
	}
	line.segments = clean
	limit := n
	truncate := panelWidth(line.text()) > n
	if truncate {
		limit--
	}
	used := 0
	for _, part := range clean {
		var b strings.Builder
		for _, c := range part.text {
			w := panelRuneWidth(c)
			if used+w > limit {
				b.WriteString(r.g.ell)
				used++
				out.segments = append(out.segments, segment{b.String(), part.kind})
				out.segments = append(out.segments, segment{text: strings.Repeat(" ", max(0, n-used))})
				return out
			}
			b.WriteRune(c)
			used += w
		}
		out.segments = append(out.segments, segment{b.String(), part.kind})
	}
	out.segments = append(out.segments, segment{text: strings.Repeat(" ", max(0, n-used))})
	return out
}

func panelWidth(s string) int {
	n := 0
	for len(s) > 0 {
		if strings.HasPrefix(s, "\x1b[") {
			end := 2
			for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == ';') {
				end++
			}
			if end < len(s) && s[end] == 'm' {
				s = s[end+1:]
				continue
			}
		}
		for _, c := range s {
			n += panelRuneWidth(c)
			s = s[len(string(c)):]
			break
		}
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
	return expandLineTabs(textLine(s)).text()
}

func expandLineTabs(line panelLine) panelLine {
	column := 0
	parts := make([]segment, len(line.segments))
	for i, part := range line.segments {
		var b strings.Builder
		for _, c := range part.text {
			if c == '\t' {
				n := 4 - column%4
				b.WriteString(strings.Repeat(" ", n))
				column += n
			} else {
				b.WriteRune(c)
				column++
			}
		}
		parts[i] = segment{b.String(), part.kind}
	}
	line.segments = parts
	return line
}

func (r panelRenderer) paint(line panelLine) string {
	if !r.style.Colour {
		return line.text()
	}
	var b strings.Builder
	outer := paintTable[paintKind(line.kind)]
	if outer != "" {
		b.WriteString("\x1b[" + outer + "m")
	}
	for _, part := range line.segments {
		if part.text == "" {
			continue
		}
		code := paintTable[part.kind]
		if part.kind == paintBorder || part.kind == paintFocusBorder {
			b.WriteString("\x1b[0m")
			for _, attribute := range strings.Split(outer, ";") {
				if attribute == "1" || attribute == "2" || attribute == "7" {
					b.WriteString("\x1b[" + attribute + "m")
				}
			}
		}
		if code != "" {
			b.WriteString("\x1b[" + code + "m")
		}
		b.WriteString(part.text)
		if code != "" {
			b.WriteString("\x1b[0m")
			if outer != "" {
				b.WriteString("\x1b[" + outer + "m")
			}
		}
	}
	if outer != "" {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func (r panelRenderer) keyLine(keys string) panelLine {
	line := panelLine{}
	for i, item := range strings.Split(keys, " · ") {
		if i > 0 {
			line.segments = append(line.segments, segment{text: " · "})
		}
		key, description, found := strings.Cut(item, " ")
		line.segments = append(line.segments, segment{key, paintChip})
		if strings.HasPrefix(description, "PgUp/PgDn ") {
			line.segments = append(line.segments, segment{text: " "}, segment{"PgUp/PgDn", paintChip})
			description = strings.TrimPrefix(description, "PgUp/PgDn ")
		}
		if found {
			line.segments = append(line.segments, segment{" " + description, paintDim})
		}
	}
	return line
}

var logErrorPattern = regexp.MustCompile(`(?i)\b(error|fail|failed|panic|fatal)\b`)

func (r panelRenderer) logLines(texts []string) []panelLine {
	lines := make([]panelLine, len(texts))
	for i, text := range texts {
		kind := paintPlain
		switch {
		case strings.Contains(text, "BATUTA-PROGRESS"):
			kind = paintLogProgress
		case logErrorPattern.MatchString(text):
			kind = paintLogError
		case strings.HasPrefix(text, "codex"), strings.HasPrefix(text, "claude"), strings.HasPrefix(text, "$ "):
			kind = paintLogPrompt
		}
		lines[i] = panelLine{segments: []segment{{text, kind}}}
		if i < len(texts)/3 {
			lines[i].kind = lineKind(paintDim)
		}
	}
	return lines
}
