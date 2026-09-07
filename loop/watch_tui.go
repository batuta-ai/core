package loop

import (
	"os"
	"os/exec"
	"strings"
	"time"

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
}

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
	m := watchModel{workspace: workspace, delivery: delivery, store: store, records: records, style: style, height: 40, now: now, currentTime: now(), interval: interval, ticker: ticker}
	m.refresh(true)
	if store != nil && delivery != "" {
		if state, err := m.pollState(); err == nil {
			m.poll = state
		}
	}
	return m
}

func (m watchModel) Init() tea.Cmd { return tea.Batch(m.pollCmd(), m.clockCmd()) }

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
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
		m.navigation.move(key, m.panel, max(1, len(panelTaskIDs(m.viewport))))
		path := panelKeyAction(key, m.workspace, m.panel, &m.navigation)
		if pager := strings.Fields(os.Getenv("PAGER")); path != "" && len(pager) > 0 {
			process := exec.Command(pager[0], append(pager[1:], path)...)
			cmd = tea.ExecProcess(process, func(err error) tea.Msg { return pagerDoneMsg{err: err} })
		}
	case tea.WindowSizeMsg:
		m.style.Width, m.height = msg.Width, msg.Height
	case journalMsg:
		if msg.err != nil {
			m.navigation.notice = msg.err.Error()
			return m, m.pollCmd()
		}
		m.records, m.poll = msg.records, msg.poll
		m.refresh(!msg.logLoaded)
		if msg.logLoaded {
			m.panel.LogLines, m.panel.LogTitle = msg.logLines, msg.logTitle
			m.viewport = m.navigation.viewport(m.panel, m.style, m.viewportHeight())
		}
		if state := terminalState(m.records); state != "" && state != StateBlocked {
			return m, tea.Quit
		}
		return m, m.pollCmd()
	case watchPollMsg:
		m.poll = msg.state
		return m, m.pollCmd()
	case clockMsg:
		m.currentTime = msg.at
		m.refresh(false)
		return m, m.clockCmd()
	case pagerDoneMsg:
		if msg.err != nil {
			m.navigation.notice = msg.err.Error()
		}
	default:
		return m, nil
	}
	m.refresh(true)
	return m, cmd
}

func (m *watchModel) refresh(loadLog bool) {
	previous := m.panel
	m.panel = m.navigation.model(m.records, m.currentTime)
	if loadLog {
		if err := loadPanelLog(m.workspace, &m.panel); err != nil {
			m.navigation.notice = err.Error()
		}
	} else if previous.Detail.LogPath == m.panel.Detail.LogPath {
		m.panel.LogLines, m.panel.LogTitle = previous.LogLines, previous.LogTitle
	}
	m.viewport = m.navigation.viewport(m.panel, m.style, m.viewportHeight())
}

func (m watchModel) viewportHeight() int {
	height := m.height
	if m.navigation.legend {
		height -= panelLineCount(panelLegend(m.style))
	}
	if m.navigation.notice != "" {
		height--
	}
	return height
}

func (m watchModel) View() tea.View {
	content := Render(m.viewport, m.style)
	if m.navigation.legend {
		content += panelLegend(m.style)
	}
	if m.navigation.notice != "" {
		content += m.navigation.notice + "\n"
	}
	return tea.View{Content: content, AltScreen: true}
}
