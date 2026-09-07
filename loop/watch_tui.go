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
	records []journal.Record
}

type pagerDoneMsg struct{ err error }

type watchModel struct {
	workspace  string
	records    []journal.Record
	now        func() time.Time
	navigation panelNavigation
	style      Style
	height     int
	panel      PanelView
	viewport   PanelView
}

var _ tea.Model = watchModel{}

func newWatchModel(workspace string, records []journal.Record, style Style, now func() time.Time) watchModel {
	if now == nil {
		now = time.Now
	}
	m := watchModel{workspace: workspace, records: records, style: style, height: 40, now: now}
	m.refresh()
	return m
}

func (m watchModel) Init() tea.Cmd { return nil }

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
		m.records = msg.records
	case pagerDoneMsg:
		if msg.err != nil {
			m.navigation.notice = msg.err.Error()
		}
	default:
		return m, nil
	}
	m.refresh()
	return m, cmd
}

func (m *watchModel) refresh() {
	m.panel = m.navigation.model(m.records, m.now())
	if err := loadPanelLog(m.workspace, &m.panel); err != nil {
		m.navigation.notice = err.Error()
	}
	height := m.height
	if m.navigation.legend {
		height -= panelLineCount(panelLegend(m.style))
	}
	if m.navigation.notice != "" {
		height--
	}
	m.viewport = m.navigation.viewport(m.panel, m.style, height)
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
