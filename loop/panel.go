package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
	"github.com/charmbracelet/x/term"
)

var isTerminal = term.IsTerminal
var newWatchProgram = tea.NewProgram

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

// Watch follows the journal until the delivery ends or the context is canceled.
// Interactive terminals use Bubble Tea; other inputs receive plain snapshots.
func Watch(ctx context.Context, workspace, delivery string, interval time.Duration, w io.Writer) error {
	root, store, err := openStore(workspace)
	if err != nil {
		return err
	}
	delivery, err = panelDelivery(store, delivery)
	if err != nil {
		return err
	}
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	if !isTerminal(os.Stdin.Fd()) {
		if delivery == "" {
			_, err := fmt.Fprintln(w, "no open deliveries")
			return err
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		return watchPlain(ctx, root, delivery, store, w, ticker.C)
	}
	var records []journal.Record
	if delivery != "" {
		records, err = store.Read(delivery)
		if err != nil {
			return err
		}
	}
	model := newPollingWatchModel(root, delivery, store, records, interval, StyleForWriter(w), nil, nil)
	if delivery == "" {
		if err := model.openPicker(); err != nil {
			return err
		}
	}
	_, err = newWatchProgram(model, tea.WithContext(ctx), tea.WithOutput(w)).Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func watchPlain(ctx context.Context, root, delivery string, store *journal.Store, w io.Writer, ticks <-chan time.Time) error {
	style, height := watchPanelSize(w)
	style.Colour = false
	var previous watchFileStamp
	first := true
	for {
		if ctx.Err() != nil {
			return nil
		}
		stamp, err := statWatchFile(store.Path(delivery))
		if err != nil {
			return err
		}
		if first || stamp != previous {
			records, err := store.Read(delivery)
			if err != nil {
				return err
			}
			panel, err := renderWatchPanel(root, records, time.Now(), style, height)
			if err != nil {
				return err
			}
			if !first {
				panel = "\n" + panel
			}
			if _, err := io.WriteString(w, panel); err != nil {
				return err
			}
			if terminalState(records) != "" {
				return nil
			}
			previous, first = stamp, false
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
		}
	}
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
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
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
	if panelLineCount(Render(model, style)) > height {
		// Keep the current scroll position and trim only the viewport's history.
		// The watch model retains the complete log for subsequent scrolling.
		visible := 3
		if style.Width >= 100 {
			visible = 6
		}
		offset := max(0, style.LogOffset)
		model.LogLines = model.LogLines[max(0, len(model.LogLines)-offset-visible):]
		for len(model.LogLines) > offset+2 && panelLineCount(Render(model, style)) > height {
			model.LogLines = model.LogLines[1:]
		}
	}
	for panelTableRows(model) > 2 && panelLineCount(Render(model, style)) > height {
		model = limitPanelRows(model, panelTableRows(model)-1)
	}
	return model
}

func panelLineCount(rendered string) int {
	if rendered == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(rendered, "\n"), "\n") + 1
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
