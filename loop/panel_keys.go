package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/batuta-ai/core/journal"
)

type panelTerminal interface {
	enterRaw() (func() error, error)
	readKey(context.Context) (string, error)
}

type panelKeyEvent struct {
	key string
	err error
}
type panelKeySession struct {
	keys    chan panelKeyEvent
	done    chan struct{}
	cancel  context.CancelFunc
	restore func() error
	err     error
}

func startPanelKeys(ctx context.Context, terminal panelTerminal) (*panelKeySession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	restore, err := terminal.enterRaw()
	if err != nil {
		return nil, err
	}
	readCtx, cancel := context.WithCancel(ctx)
	session := &panelKeySession{keys: make(chan panelKeyEvent), done: make(chan struct{}), cancel: cancel, restore: restore}
	go func() {
		defer close(session.done)
		defer func() { session.err = session.restore() }()
		defer close(session.keys)
		for {
			key, err := terminal.readKey(readCtx)
			select {
			case session.keys <- panelKeyEvent{key, err}:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return session, nil
}

func (s *panelKeySession) stop() error {
	if s == nil {
		return nil
	}
	s.cancel()
	<-s.done
	return s.err
}

type panelNavigation struct {
	selected string
	offset   int
	legend   bool
	notice   string
}

func (n *panelNavigation) model(records []journal.Record, now time.Time) PanelView {
	model := PanelModel(records, now, n.selected)
	if n.selected != "" && model.Detail.Task == "" {
		n.selected = ""
		model = PanelModel(records, now, "")
	}
	return model
}

func panelTaskIDs(model PanelView) []string {
	var ids []string
	for _, wave := range model.Waves {
		for _, row := range wave.Rows {
			ids = append(ids, row.Task)
		}
	}
	return ids
}

func (n *panelNavigation) move(key string, model PanelView, page int) {
	if key == "f" {
		n.selected = ""
		return
	}
	ids := panelTaskIDs(model)
	if len(ids) == 0 {
		return
	}
	index := 0
	for i, id := range ids {
		if id == model.Detail.Task {
			index = i
			break
		}
	}
	switch key {
	case "up":
		index--
	case "down":
		index++
	case "pageUp":
		index -= max(1, page)
	case "pageDown":
		index += max(1, page)
	default:
		return
	}
	n.selected = ids[max(0, min(index, len(ids)-1))]
}

func panelWindow(model PanelView, offset, budget int) PanelView {
	var waves []PanelWave
	skipped, shown, used := 0, 0, 0
	for _, wave := range model.Waves {
		copyWave := wave
		copyWave.Rows = nil
		for _, row := range wave.Rows {
			if skipped < offset {
				skipped++
				continue
			}
			cost := 1
			if len(copyWave.Rows) == 0 {
				cost++
			}
			if used+cost > budget {
				break
			}
			used += cost
			shown++
			copyWave.Rows = append(copyWave.Rows, row)
		}
		if len(copyWave.Rows) > 0 {
			waves = append(waves, copyWave)
		}
		if used >= budget {
			break
		}
	}
	model.RowsAbove = skipped
	model.RowsBelow = len(panelTaskIDs(model)) - skipped - shown
	model.Waves = waves
	return model
}

func (n *panelNavigation) viewport(model PanelView, style Style, height int) PanelView {
	fitted := fitPanelHeight(model, style, height)
	model.LogLines = fitted.LogLines
	budget := panelTableRows(fitted)
	if style.Width < 76 {
		empty := model
		empty.Waves = nil
		budget = max(2, height-panelLineCount(Render(empty, style)))
	}
	budget = max(2, budget)
	ids := panelTaskIDs(model)
	selected := 0
	for i, id := range ids {
		if id == model.Detail.Task {
			selected = i
			break
		}
	}
	n.offset = max(0, min(n.offset, selected, len(ids)-budget))
	view := panelWindow(model, n.offset, budget)
	for n.offset < selected && selected >= n.offset+len(panelTaskIDs(view)) {
		n.offset++
		view = panelWindow(model, n.offset, budget)
	}
	return view
}

var panelLegendLabels = map[string][]string{
	"en": {"Legend", "pass / integrated", "failed / blocked", "pending", "> running   ? waiting answer", "~ usage limit   < re-executing   ^ escalated", "G0 executor finished   G1 tree change", "G2 tests   G3 scope, proofs and verification"},
	"pt": {"Legenda", "passou / integrada", "falhou / bloqueada", "pendente", "> em execução   ? aguarda resposta", "~ limite de uso   < reexecutando   ^ escalada", "G0 executor terminou   G1 alteração na árvore", "G2 testes   G3 escopo, provas e verificação"},
}

func panelLegend(style Style) string {
	labels := panelLegendLabels[style.Lang]
	if labels == nil {
		labels = panelLegendLabels["en"]
	}
	r := panelRenderer{style: style, g: glyphsFor(style), labels: panelLabels[style.Lang]}
	rows := []string{r.g.ok + " " + labels[1] + "   " + r.g.fail + " " + labels[2], r.g.pend + " " + labels[3]}
	rows = append(rows, labels[4:]...)
	return strings.Join(r.box(labels[0], rows, style.Width), "\n") + "\n"
}

func panelShellQuote(value string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func panelAnswerCommand(workspace, task string) string {
	return "batuta loop --workspace " + panelShellQuote(workspace) + " --answer " + panelShellQuote(task) + ` "<text>"`
}

func openPanelPager(ctx context.Context, path string, w io.Writer) error {
	pager := strings.TrimSpace(os.Getenv("PAGER"))
	if pager == "" {
		return nil
	}
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		command = exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", pager+" "+panelShellQuote(path))
	} else {
		command = exec.CommandContext(ctx, "sh", "-c", pager+` "$1"`, "batuta-pager", path)
	}
	command.Stdin = os.Stdin
	command.Stdout = w
	command.Stderr = w
	return command.Run()
}

func panelLogPath(root string, model PanelView) string {
	if model.Detail.LogPath == "" {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(model.Detail.LogPath))
}

func loadPanelLog(root string, model *PanelView) error {
	path := panelLogPath(root, *model)
	if path == "" {
		return nil
	}
	lines, err := readPanelLog(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	model.LogLines = lines
	model.LogTitle = panelLogTitle(model.Detail)
	return nil
}

func panelKeyAction(key, workspace string, model PanelView, n *panelNavigation) string {
	switch key {
	case "?":
		n.legend = !n.legend
	case "r":
		for _, wave := range model.Waves {
			for _, row := range wave.Rows {
				if row.Task == model.Detail.Task && row.State == "waiting_input" {
					n.notice = panelAnswerCommand(workspace, row.Task)
				}
			}
		}
	case "o":
		path := panelLogPath(workspace, model)
		if path != "" {
			n.notice = path
			return path
		}
	}
	return ""
}

func panelInputError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("dashboard input: %w", err)
}

func decodePanelKey(sequence *string, b byte) string {
	if b == 3 {
		*sequence = ""
		return "interrupt"
	}
	if b == 0x1b {
		*sequence = "\x1b"
		return ""
	}
	if *sequence == "" {
		return string(b)
	}
	*sequence += string(b)
	switch *sequence {
	case "\x1b[A", "\x1bOA":
		*sequence = ""
		return "up"
	case "\x1b[B", "\x1bOB":
		*sequence = ""
		return "down"
	case "\x1b[5~":
		*sequence = ""
		return "pageUp"
	case "\x1b[6~":
		*sequence = ""
		return "pageDown"
	case "\x1b[", "\x1bO", "\x1b[5", "\x1b[6":
		return ""
	default:
		*sequence = ""
		return ""
	}
}
