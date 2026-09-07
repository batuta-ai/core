package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

const panelClearScreen = "\x1b[2J\x1b[H"

// Snapshot writes one dashboard frame without entering interactive mode.
func Snapshot(workspace, delivery string, w io.Writer) error {
	root, store, err := openStore(workspace)
	if err != nil {
		return err
	}
	delivery, err = panelDelivery(store, delivery)
	if err != nil {
		return err
	}
	if delivery == "" {
		_, err := fmt.Fprintln(w, "no open deliveries")
		return err
	}
	records, err := store.Read(delivery)
	if err != nil {
		return err
	}
	style, height := watchPanelSize(w)
	panel, err := renderWatchPanel(root, records, time.Now(), style, height)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, panel)
	return err
}

func panelDelivery(store *journal.Store, delivery string) (string, error) {
	if delivery != "" {
		return delivery, nil
	}
	ids, err := store.List()
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		records, err := store.Read(id)
		if err == nil && len(records) > 0 && records[0].Kind == KindOpened && terminalState(records) == "" {
			return id, nil
		}
	}
	return "", nil
}

// Watch redraws the most recent journal state until the delivery ends or
// the context is canceled. It only reads the journal.
func Watch(ctx context.Context, workspace, delivery string, interval time.Duration, w io.Writer) error {
	return watchWithTerminal(ctx, workspace, delivery, interval, w, newPanelTerminal(os.Stdin), func(ctx context.Context, path string) error {
		return openPanelPager(ctx, path, w)
	})
}

func watchWithTerminal(ctx context.Context, workspace, delivery string, interval time.Duration, w io.Writer, input panelTerminal, pager func(context.Context, string) error) (result error) {
	root, store, err := openStore(workspace)
	if err != nil {
		return err
	}
	delivery, err = panelDelivery(store, delivery)
	if err != nil {
		return err
	}
	if delivery == "" {
		_, err := fmt.Fprintln(w, "no open deliveries")
		return err
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	var session *panelKeySession
	var keys <-chan panelKeyEvent
	if input != nil {
		session, err = startPanelKeys(ctx, input)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		keys = session.keys
	}
	defer func() { result = errors.Join(result, session.stop()) }()
	navigation := panelNavigation{}
	var model, visible PanelView
	redraw := func() (bool, error) {
		records, err := store.Read(delivery)
		if err != nil {
			return false, err
		}
		now := time.Now()
		model = navigation.model(records, now)
		panel := RenderPanel(records, now)
		style, height := watchPanelSize(w)
		if panelJournalHasWorkspace(records) {
			if err := loadPanelLog(root, &model); err != nil {
				return false, err
			}
			if navigation.legend {
				height -= panelLineCount(panelLegend(style))
			}
			if navigation.notice != "" {
				height--
			}
			visible = navigation.viewport(model, style, height)
			panel = Render(visible, style)
		}
		if navigation.legend {
			panel += panelLegend(style)
		}
		if navigation.notice != "" {
			panel += navigation.notice + "\n"
		}
		if _, err := io.WriteString(w, panelClearScreen+panel); err != nil {
			return false, err
		}
		state := terminalState(records)
		return state != "" && (input == nil || (state != StateWaitingInput && state != StateBlocked)), nil
	}
	terminal, err := redraw()
	if err != nil || terminal {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-keys:
			if !ok || event.err != nil {
				if err := session.stop(); err != nil {
					return err
				}
				keys = nil
				input = nil
				navigation.selected = ""
				if event.err != nil && panelInputError(event.err) != nil {
					return panelInputError(event.err)
				}
			} else {
				if event.key == "q" || event.key == "interrupt" {
					return nil
				}
				navigation.move(event.key, model, max(1, len(panelTaskIDs(visible))))
				path := panelKeyAction(event.key, root, model, &navigation)
				if path != "" && pager != nil {
					if err := session.stop(); err != nil {
						return err
					}
					if _, err := fmt.Fprintln(w, path); err != nil {
						return err
					}
					pagerErr := pager(ctx, path)
					if ctx.Err() != nil {
						return nil
					}
					session, err = startPanelKeys(ctx, input)
					if err != nil {
						return err
					}
					keys = session.keys
					if pagerErr != nil {
						navigation.notice = path + ": " + pagerErr.Error()
					}
				}
			}
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return nil
		}
		terminal, err = redraw()
		if err != nil || terminal {
			return err
		}
	}
}

func panelJournalHasWorkspace(records []journal.Record) bool {
	for _, record := range records {
		if record.Kind != KindOpened {
			continue
		}
		var detail openedDetail
		return json.Unmarshal(record.Detail, &detail) == nil && detail.Workspace != ""
	}
	return false
}

func watchPanelSize(w io.Writer) (Style, int) {
	style := StyleForWriter(w)
	height := 40
	if file, ok := w.(interface{ Fd() uintptr }); ok {
		_, height = TerminalSize(file.Fd())
	}
	return style, height
}

func renderWatchPanel(workspace string, records []journal.Record, now time.Time, style Style, height int) (string, error) {
	model := PanelModel(records, now, "")
	if err := loadPanelLog(workspace, &model); err != nil {
		return "", err
	}
	return Render(fitPanelHeight(model, style, height), style), nil
}

func readPanelLog(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSuffix(string(content), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return lines, nil
}

func panelLogTitle(detail PanelDetail) string {
	if detail.Task == "" || detail.Attempt <= 0 {
		return ""
	}
	return strings.ReplaceAll(detail.Task, "_", "-") + "-e" + fmt.Sprint(detail.Attempt)
}

func fitPanelHeight(model PanelView, style Style, height int) PanelView {
	if height <= 0 || style.Width < 76 {
		return model
	}
	if style.Width >= 100 && len(model.LogLines) > 3 && panelLineCount(Render(model, style)) > height {
		model.LogLines = model.LogLines[len(model.LogLines)-3:]
	}
	for panelTableRows(model) > 8 && panelLineCount(Render(model, style)) > height {
		model = limitPanelRows(model, panelTableRows(model)-1)
	}
	if panelLineCount(Render(model, style)) > height {
		model.LogLines = nil
	}
	return model
}

func panelLineCount(rendered string) int {
	return strings.Count(rendered, "\n")
}

func panelTableRows(model PanelView) int {
	rows := 0
	for _, wave := range model.Waves {
		rows += 1 + len(wave.Rows)
	}
	return rows
}

func limitPanelRows(model PanelView, limit int) PanelView {
	waves := make([]PanelWave, 0, len(model.Waves))
	remaining := limit
	for _, wave := range model.Waves {
		if remaining <= 0 {
			break
		}
		copyWave := wave
		copyWave.Rows = nil
		remaining--
		if remaining > 0 {
			count := min(len(wave.Rows), remaining)
			copyWave.Rows = append(copyWave.Rows, wave.Rows[:count]...)
			remaining -= count
		}
		waves = append(waves, copyWave)
	}
	model.RowsBelow = panelTableRows(model) - panelTableRows(PanelView{Waves: waves})
	model.Waves = waves
	return model
}

// RenderPanel renders one delivery from its journal without reading or writing
// external state. The opening record identifies the delivery by its plan slug.
// Journals currently omit delivery IDs, criterion totals and planned wave counts, so the
// panel displays completed items and admitted waves without denominators.
func RenderPanel(records []journal.Record, now time.Time) string {
	model := PanelModel(records, now, "")
	if model.message != "" {
		return model.message + "\n"
	}
	var b strings.Builder
	phase := ""
	if model.Header.Phase > 0 && model.Header.PhaseTitle != "" {
		phase = fmt.Sprintf("   phase %d · %s", model.Header.Phase, model.Header.PhaseTitle)
	}
	fmt.Fprintf(&b, "delivery %s%s   branch %s @ %s   wave %d   elapsed %s\n", panelValue(model.Header.Delivery), phase, panelValue(model.Header.Branch), panelCommit(model.Header.Head), model.engineWaves, model.elapsed)
	table := tabwriter.NewWriter(&b, 0, 8, 2, ' ', 0)
	fmt.Fprintln(table, "task\tlane\texecutor/model\texec\tstate\tdetail")
	for _, row := range model.rows {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", row.task, row.lane, row.runtime, row.execution, row.state, strings.Join(strings.Fields(row.detail), " "))
	}
	_ = table.Flush()
	fmt.Fprintf(&b, "last     %s\n", model.last)
	return b.String()
}

func panelTaskDetail(task routing.GraphTask, attempt routing.GraphTaskAttempt, records []journal.Record, now time.Time) string {
	switch task.State {
	case routing.GraphTaskIntegrated:
		return "commit " + panelCommit(task.IntegratedCommitSHA)
	case routing.GraphTaskPending:
		if len(task.Dependencies) > 0 {
			return "after " + strings.Join(task.Dependencies, ", ")
		}
		return "-"
	case routing.GraphTaskRunning:
		return panelRunningDetail(task.TaskID, attempt.Execution, records, now)
	case routing.GraphTaskBlocked:
		return panelValue(task.BlockerCode)
	case routing.GraphTaskWaitingInput:
		if attempt.Question != nil {
			return attempt.Question.Prompt
		}
	case routing.GraphTaskCandidate:
		return "commit " + panelCommit(attempt.CandidateCommitSHA)
	}
	return "-"
}

func panelRunningDetail(task string, execution int, records []journal.Record, now time.Time) string {
	var started time.Time
	done := make(map[int]bool)
	gate := "—"
	for _, record := range records {
		if record.TaskID != task {
			continue
		}
		var detail struct {
			Execution int    `json:"execution"`
			Criterion int    `json:"criterion"`
			State     string `json:"state"`
			Passed    *bool  `json:"passed"`
		}
		if json.Unmarshal(record.Detail, &detail) != nil || detail.Execution != execution {
			continue
		}
		switch record.Kind {
		case KindStarted:
			started = record.At
		case KindProgress:
			if detail.State == "DONE" && detail.Criterion > 0 {
				done[detail.Criterion] = true
			}
		case KindGates:
			if detail.Passed != nil {
				gate = "fail"
				if *detail.Passed {
					gate = "ok"
				}
			}
		}
	}
	return fmt.Sprintf("%s · %d items · gate %s", panelElapsed(started, now), len(done), gate)
}

func panelElapsed(started, now time.Time) string {
	if started.IsZero() {
		return "—"
	}
	seconds := max(0, int64(now.Sub(started)/time.Second))
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}

func panelValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func panelCommit(sha string) string {
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return panelValue(sha)
}
