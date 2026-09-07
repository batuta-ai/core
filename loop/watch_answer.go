package loop

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

func (m watchModel) canAnswer() bool {
	if strings.TrimSpace(m.panel.Detail.Question) == "" {
		return false
	}
	for _, wave := range m.panel.Waves {
		for _, row := range wave.Rows {
			if row.Task == m.panel.Detail.Task && row.State == "waiting_input" {
				return true
			}
		}
	}
	return false
}

func (m *watchModel) openAnswer() tea.Cmd {
	m.answering = true
	m.answerQuestion, m.answerTask = m.panel.Detail.Question, m.panel.Detail.Task
	m.navigation.notice = ""
	m.answerEditor = textarea.New()
	m.answerEditor.Placeholder = m.answerLabel("answer_placeholder")
	m.answerEditor.ShowLineNumbers = false
	m.answerEditor.Prompt = ""
	m.answerEditor.MaxWidth = 0
	cmd := m.answerEditor.Focus()
	m.refresh(false)
	return cmd
}

func (m watchModel) updateAnswerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeAnswer()
	case "ctrl+enter", "alt+enter", "ctrl+s":
		text := strings.TrimSpace(m.answerEditor.Value())
		if text == "" {
			m.navigation.notice = m.answerLabel("answer_empty")
		} else if _, err := Answer(m.workspace, m.answerTask, text); err != nil {
			m.navigation.notice = err.Error()
		} else {
			m.closeAnswer()
		}
	default:
		m.answerEditor, cmd = m.answerEditor.Update(msg)
	}
	m.refresh(false)
	return m, cmd
}

func (m *watchModel) closeAnswer() {
	m.answering = false
	m.answerEditor.Blur()
	m.answerEditor.Reset()
	m.answerQuestion, m.answerTask = "", ""
	m.navigation.notice = ""
}

func (m watchModel) answerLabel(key string) string {
	labels := panelLabels[m.style.Lang]
	if labels == nil {
		labels = panelLabels["en"]
	}
	return labels[key]
}

func (m watchModel) answerHelp() string {
	return wrapAnswerText(m.answerLabel("answer_keys"), max(1, m.style.Width)) + "\n"
}

func (m watchModel) answerNotice() string {
	if m.navigation.notice == "" {
		return ""
	}
	return wrapAnswerText(m.navigation.notice, max(1, m.style.Width)) + "\n"
}

func (m watchModel) answerPrompt() string {
	lines := strings.Split(wrapAnswerText(m.answerQuestion, max(1, m.style.Width-4)), "\n")
	available := max(1, m.height-panelLineCount(m.answerHelp())-panelLineCount(m.answerNotice())-1)
	return strings.Join(lines[:min(len(lines), available)], "\n") + "\n"
}

func (m *watchModel) resizeAnswer() {
	m.answerEditor.SetWidth(max(1, m.style.Width-4))
	available := m.height - panelLineCount(m.answerPrompt()) - panelLineCount(m.answerHelp()) - panelLineCount(m.answerNotice())
	m.answerEditor.SetHeight(max(1, min(8, available)))
}

func (m watchModel) answerView() string {
	return m.answerPrompt() + m.answerEditor.View() + "\n" + m.answerNotice() + m.answerHelp()
}

func wrapAnswerText(text string, width int) string {
	var result strings.Builder
	column := 0
	for _, char := range expandPanelTabs(text) {
		if char == '\n' {
			result.WriteRune(char)
			column = 0
			continue
		}
		cells := panelRuneWidth(char)
		if column > 0 && column+cells > width {
			result.WriteByte('\n')
			column = 0
		}
		result.WriteRune(char)
		column += cells
	}
	return result.String()
}
