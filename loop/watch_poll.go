package loop

import (
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
)

type watchTicker func(time.Duration, func(time.Time) tea.Msg) tea.Cmd

type watchFileStamp struct {
	size    int64
	modTime time.Time
}

type watchPollState struct {
	journal watchFileStamp
	logSize int64
}

type watchPollMsg struct {
	state watchPollState
}

type clockMsg struct {
	at time.Time
}

func statWatchFile(path string) (watchFileStamp, error) {
	if path == "" {
		return watchFileStamp{}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return watchFileStamp{}, err
	}
	return watchFileStamp{size: info.Size(), modTime: info.ModTime()}, nil
}

func (m watchModel) pollState() (watchPollState, error) {
	journalStamp, err := statWatchFile(m.store.Path(m.delivery))
	if err != nil {
		return watchPollState{}, err
	}
	logStamp, err := statWatchFile(panelLogPath(m.workspace, m.panel))
	if err != nil && !os.IsNotExist(err) {
		return watchPollState{}, err
	}
	return watchPollState{journal: journalStamp, logSize: logStamp.size}, nil
}

func (m watchModel) pollCmd() tea.Cmd {
	if m.store == nil || m.delivery == "" || m.ticker == nil {
		return nil
	}
	return m.ticker(m.interval, func(time.Time) tea.Msg {
		state, err := m.pollState()
		if err != nil {
			return journalMsg{err: err}
		}
		if state.journal.size == m.poll.journal.size && state.journal.modTime.Equal(m.poll.journal.modTime) && state.logSize == m.poll.logSize {
			return watchPollMsg{state: state}
		}
		records, err := m.store.Read(m.delivery)
		if err != nil {
			return journalMsg{err: err, poll: state}
		}
		panel := m.navigation.model(records, m.currentTime)
		if err := loadPanelLog(m.workspace, &panel); err != nil {
			return journalMsg{err: err, poll: state}
		}
		return journalMsg{records: records, logLines: panel.LogLines, logTitle: panel.LogTitle, logLoaded: true, poll: state}
	})
}

func (m watchModel) clockCmd() tea.Cmd {
	if m.ticker == nil {
		return nil
	}
	return m.ticker(time.Second, func(at time.Time) tea.Msg {
		return clockMsg{at: at}
	})
}
