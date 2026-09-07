package loop

import (
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/core/journal"
)

type journalMsg struct {
	records   []journal.Record
	logLines  []string
	logTitle  string
	logLoaded bool
	poll      watchPollState
	err       error
}

type pagerDoneMsg struct{ err error }
type spinnerTickMsg struct{}
type progressTickMsg struct{}

type watchModel struct {
	workspace   string
	delivery    string
	store       *journal.Store
	records     []journal.Record
	now         func() time.Time
	currentTime time.Time
	interval    time.Duration
	ticker      watchTicker
	poll        watchPollState
	navigation  panelNavigation
	style       Style
	height      int
	panel       PanelView
	viewport    PanelView
	focus       panelFocus
	logOffset   int
	progress    progressAnimation
	progressSet bool

	answering      bool
	answerEditor   textarea.Model
	answerQuestion string
	answerTask     string
}

type progressAnimation struct {
	waves, tasks float64
	frame        int
	active       bool
}

type panelFocus string

const (
	focusTable panelFocus = "table"
	focusLog   panelFocus = "log"
)

var _ tea.Model = watchModel{}

func newWatchModel(workspace string, records []journal.Record, style Style, now func() time.Time) watchModel {
	return newPollingWatchModel(workspace, "", nil, records, 0, style, now, nil)
}

func newPollingWatchModel(workspace, delivery string, store *journal.Store, records []journal.Record, interval time.Duration, style Style, now func() time.Time, ticker watchTicker) watchModel {
	if now == nil {
		now = time.Now
	}
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	if ticker == nil {
		ticker = tea.Tick
	}
	style.Frame = 0
	m := watchModel{workspace: workspace, delivery: delivery, store: store, records: records, style: style, height: 40, now: now, currentTime: now(), interval: interval, ticker: ticker, focus: focusTable}
	m.refresh(true)
	if store != nil && delivery != "" {
		if state, err := m.pollState(m.currentTime); err == nil {
			m.poll = state
			m.refresh(false)
		}
	}
	return m
}

func (m watchModel) Init() tea.Cmd {
	return tea.Batch(m.pollCmd(), m.clockCmd(), m.spinnerCmd())
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.answering {
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			return m.updateAnswerKey(msg)
		case tea.MouseWheelMsg:
			return m, nil
		default:
			m.answerEditor, cmd = m.answerEditor.Update(msg)
		}
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		key := msg.String()
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "pgup":
			key = "pageUp"
		case "pgdown":
			key = "pageDown"
		}
		if key == "r" && m.canAnswer() {
			cmd = m.openAnswer()
			return m, cmd
		}
		if key == "l" {
			if m.focus == focusLog {
				m.focus = focusTable
			} else {
				m.focus = focusLog
			}
			m.logOffset = 0
			m.viewport = m.navigation.viewport(m.panel, m.renderStyle(), m.viewportHeight())
			return m, nil
		}
		if m.focus == focusLog && m.scrollLog(key) {
			m.viewport = m.navigation.viewport(m.panel, m.renderStyle(), m.viewportHeight())
			return m, nil
		}
		m.navigation.move(key, m.panel, max(1, len(panelTaskIDs(m.viewport))))
		path := panelKeyAction(key, m.workspace, m.panel, &m.navigation)
		if pager := strings.Fields(os.Getenv("PAGER")); path != "" && len(pager) > 0 {
			process := exec.Command(pager[0], append(pager[1:], path)...)
			cmd = tea.ExecProcess(process, func(err error) tea.Msg { return pagerDoneMsg{err: err} })
		}
	case tea.MouseWheelMsg:
		key := "down"
		if msg.Button == tea.MouseWheelUp {
			key = "up"
		} else if msg.Button != tea.MouseWheelDown {
			return m, nil
		}
		if m.focus == focusLog {
			m.scrollLog(key)
			m.viewport = m.navigation.viewport(m.panel, m.renderStyle(), m.viewportHeight())
			return m, nil
		}
		m.navigation.move(key, m.panel, max(1, len(panelTaskIDs(m.viewport))))
	case tea.WindowSizeMsg:
		m.style.Width, m.height = msg.Width, msg.Height
	case journalMsg:
		if msg.err != nil {
			m.navigation.notice = msg.err.Error()
			return m, m.pollCmd()
		}
		wasRunning := panelHasRunningTask(m.panel)
		wasEasing := m.progress.active
		m.records, m.poll = msg.records, msg.poll
		m.refresh(!msg.logLoaded)
		if msg.logLoaded {
			m.panel.LogLines, m.panel.LogTitle = msg.logLines, msg.logTitle
			m.logOffset = min(m.logOffset, m.maxLogOffset())
			m.viewport = m.navigation.viewport(m.panel, m.renderStyle(), m.viewportHeight())
		}
		var spinner, progress tea.Cmd
		if !wasRunning && panelHasRunningTask(m.panel) {
			spinner = m.spinnerCmd()
		}
		if !wasEasing && m.progress.active {
			progress = m.progressCmd()
		}
		return m, tea.Batch(m.pollCmd(), spinner, progress)
	case watchPollMsg:
		m.poll = msg.state
		return m, m.pollCmd()
	case clockMsg:
		m.currentTime = msg.at
		m.refresh(false)
		return m, m.clockCmd()
	case spinnerTickMsg:
		if !panelHasRunningTask(m.panel) {
			return m, nil
		}
		m.style.Frame++
		return m, m.spinnerCmd()
	case progressTickMsg:
		if !m.progress.active {
			return m, nil
		}
		m.progress.frame++
		fraction := float64(m.progress.frame) / 12
		m.panel.Progress.WavesShown = m.progress.waves + (float64(m.panel.Progress.WavesDone)-m.progress.waves)*fraction
		m.panel.Progress.TasksShown = m.progress.tasks + (float64(m.panel.Progress.TasksDone)-m.progress.tasks)*fraction
		if m.progress.frame == 12 {
			m.panel.Progress.WavesShown = float64(m.panel.Progress.WavesDone)
			m.panel.Progress.TasksShown = float64(m.panel.Progress.TasksDone)
			m.progress.active = false
		}
		m.viewport = m.navigation.viewport(m.panel, m.renderStyle(), m.viewportHeight())
		return m, m.progressCmd()
	case pagerDoneMsg:
		if msg.err != nil {
			m.navigation.notice = msg.err.Error()
		}
	default:
		return m, cmd
	}
	m.refresh(true)
	return m, cmd
}

func (m watchModel) spinnerCmd() tea.Cmd {
	if m.ticker == nil || !panelHasRunningTask(m.panel) {
		return nil
	}
	return m.ticker(80*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

func (m watchModel) progressCmd() tea.Cmd {
	if m.ticker == nil || !m.progress.active {
		return nil
	}
	return m.ticker(30*time.Millisecond, func(time.Time) tea.Msg { return progressTickMsg{} })
}

func panelHasRunningTask(panel PanelView) bool {
	for _, wave := range panel.Waves {
		for _, row := range wave.Rows {
			if row.State == "running" || row.State == "preparing" {
				return true
			}
		}
	}
	return false
}

func (m *watchModel) refresh(loadLog bool) {
	if m.answering {
		m.resizeAnswer()
	}
	previous := m.panel
	m.panel = m.navigation.model(m.records, m.currentTime)
	if m.store != nil && m.delivery != "" {
		m.panel.Header.Presence, m.panel.Header.Loops = m.poll.presence, m.poll.loops
	}
	switch m.panel.Header.State {
	case StateDone, StateAbandoned, StateCanceled:
		m.panel.Attention = PanelAttention{Kind: "stopped", Hint: "d picks another delivery"}
	case StateWaitingInput:
		if m.panel.Attention.Kind != "question" {
			m.panel.Attention = PanelAttention{Kind: "question", Hint: "r answers"}
		}
	case StateBlocked:
		if m.panel.Attention.Kind == "none" {
			m.panel.Attention = PanelAttention{Kind: "blocked"}
		}
	}
	if m.progressSet {
		changed := previous.Progress.WavesDone != m.panel.Progress.WavesDone || previous.Progress.TasksDone != m.panel.Progress.TasksDone
		if changed {
			m.progress = progressAnimation{waves: previous.Progress.WavesShown, tasks: previous.Progress.TasksShown}
			m.progress.active = m.progress.waves != m.panel.Progress.WavesShown || m.progress.tasks != m.panel.Progress.TasksShown
			m.panel.Progress.WavesShown = m.progress.waves
			m.panel.Progress.TasksShown = m.progress.tasks
		} else if m.progress.active {
			m.panel.Progress.WavesShown = previous.Progress.WavesShown
			m.panel.Progress.TasksShown = previous.Progress.TasksShown
		}
	}
	if loadLog {
		if err := loadPanelLog(m.workspace, &m.panel); err != nil {
			m.navigation.notice = err.Error()
		}
	} else if previous.Detail.LogPath == m.panel.Detail.LogPath {
		m.panel.LogLines, m.panel.LogTitle = previous.LogLines, previous.LogTitle
	}
	m.logOffset = min(m.logOffset, m.maxLogOffset())
	m.viewport = m.navigation.viewport(m.panel, m.renderStyle(), m.viewportHeight())
	m.progressSet = m.panel.message == ""
}

func (m watchModel) viewportHeight() int {
	height := m.height
	if m.answering {
		return max(0, height-panelLineCount(m.answerView()))
	}
	if m.navigation.legend {
		height -= panelLineCount(panelLegend(m.style))
	}
	if m.navigation.notice != "" {
		height--
	}
	return height
}

func (m watchModel) renderStyle() Style {
	style := m.style
	style.Focus = string(m.focus)
	if m.answering {
		style.Focus = ""
	}
	style.LogOffset = m.logOffset
	return style
}

func (m watchModel) visibleLogLines() int {
	if m.style.Width >= 100 {
		return 6
	}
	if m.style.Width >= 76 {
		return 3
	}
	return 0
}

func (m watchModel) maxLogOffset() int {
	return max(0, len(m.panel.LogLines)-m.visibleLogLines())
}

func (m *watchModel) scrollLog(key string) bool {
	page := max(1, m.visibleLogLines())
	switch key {
	case "up":
		m.logOffset++
	case "down":
		m.logOffset--
	case "pageUp":
		m.logOffset += page
	case "pageDown":
		m.logOffset -= page
	case "end":
		m.logOffset = 0
	default:
		return false
	}
	m.logOffset = max(0, min(m.logOffset, m.maxLogOffset()))
	return true
}

func (m watchModel) View() tea.View {
	style := m.renderStyle()
	content := Render(m.viewport, style)
	if m.answering {
		lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
		content = ""
		if height := min(len(lines), m.viewportHeight()); height > 0 {
			content = strings.Join(lines[:height], "\n") + "\n"
		}
		content += m.answerView()
	} else if m.navigation.legend {
		content += panelLegend(style)
	}
	if !m.answering && m.navigation.notice != "" {
		content += m.navigation.notice + "\n"
	}
	return tea.View{
		Content: content, AltScreen: true, MouseMode: tea.MouseModeCellMotion,
		KeyboardEnhancements: tea.KeyboardEnhancements{ReportEventTypes: true},
	}
}
