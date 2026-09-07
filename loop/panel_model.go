package loop

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

type PanelHeader struct {
	Delivery, Project, Branch, Head string
	Elapsed                         time.Duration
	State, Roadmap                  string
	Phase                           int
	PhaseTitle                      string
}

type PanelAttention struct {
	Kind, Task, Text, Hint string
}

type PanelContext struct {
	Executor, Model, Reasoning, TestCommand, Sandbox string
	Sessions, Retries, Escalations, PID              int
}

type PanelProgress struct {
	Criterion, CriterionTotal                    int
	CriterionTitle                               string
	WavesDone, WavesTotal, TasksDone, TasksTotal int
}

type PanelDetail struct {
	AttemptLimit, CriterionTotal                 int
	CriterionTitle                               string
	Task                                         string
	Attempt                                      int
	Title                                        string
	Criterion                                    int
	CriterionState, Question, Reason, LastRecord string
	LastAge                                      time.Duration
	Worktree, LogPath                            string
}

type PanelRow struct {
	AttemptLimit       int
	Task, Title, State string
	Attempt            int
	Gates              [4]string
	Commit             string
}

type PanelWave struct {
	Dependencies     []string
	Number           int
	Base, Integrated string
	Done, Total      int
	State            string
	Rows             []PanelRow
}

type PanelHealth struct {
	JournalAge time.Duration
	// A gate's change/silence verdict does not establish whether its tree is dirty.
	TreeDirty *bool
	LastError string
}

type PanelView struct {
	LogTitle               string
	LogLines               []string
	RowsAbove, RowsBelow   int
	Header                 PanelHeader
	Attention              PanelAttention
	Context                PanelContext
	Progress               PanelProgress
	Detail                 PanelDetail
	Waves                  []PanelWave
	Health                 PanelHealth
	message, last, elapsed string
	rows                   []panelTSVRow
	engineWaves            int
}

type panelTSVRow struct {
	task, lane, runtime, execution, state, detail string
}

type panelTaskView struct {
	task                 routing.GraphTask
	attempt              routing.GraphTaskAttempt
	detail               PanelDetail
	context              PanelContext
	report               gates.Report
	lastAt               time.Time
	limit                *panelEvent
	conflict, escalation string
}

type panelEvent struct {
	Execution    int                  `json:"execution"`
	RunID        string               `json:"run_id"`
	Executor     string               `json:"executor"`
	Model        string               `json:"model"`
	Reasoning    string               `json:"reasoning"`
	Worktree     json.RawMessage      `json:"worktree"`
	Criterion    int                  `json:"criterion"`
	State        string               `json:"state"`
	Question     string               `json:"question"`
	Reason       string               `json:"reason"`
	Error        string               `json:"error"`
	Feedback     []string             `json:"feedback"`
	Blocker      string               `json:"blocker"`
	Blocked      bool                 `json:"blocked"`
	SameRuntime  bool                 `json:"same_runtime"`
	NextRuntime  routing.RuntimeValue `json:"next_runtime"`
	Wave         int                  `json:"wave"`
	FinalHead    string               `json:"final_head"`
	ConflictTask string               `json:"conflict_task"`
	Wait         int                  `json:"wait"`
	Seconds      int                  `json:"seconds"`
	ResetAt      time.Time            `json:"reset_at"`
}

// PanelModel projects journal snapshots and events without consulting live state.
// Wave totals count first admissions, folding retries into the original wave.
// Number zero groups tasks not yet admitted.
func PanelModel(records []journal.Record, now time.Time, selected string) PanelView {
	model := PanelView{Attention: PanelAttention{Kind: "none"}}
	if len(records) == 0 {
		model.message = "no records"
		return model
	}
	last := records[len(records)-1]
	var graph routing.DeliveryGraph
	if json.Unmarshal(last.Graph, &graph) != nil {
		model.message = "invalid graph"
		return model
	}
	var opened openedDetail
	var started time.Time
	for _, record := range records {
		if record.Kind == KindOpened {
			if json.Unmarshal(record.Detail, &opened) == nil {
				started = record.At
			}
			break
		}
	}
	model.engineWaves = len(graph.Waves)
	model.Header = PanelHeader{Delivery: opened.Slug, Branch: opened.Branch, Head: opened.Head, State: "running", Roadmap: opened.Roadmap, Phase: opened.Phase, PhaseTitle: opened.PhaseTitle}
	if opened.Workspace != "" {
		model.Header.Project = filepath.Base(opened.Workspace)
	}
	model.Health.JournalAge = panelAge(last.At, now)
	model.last = panelRecordText(last)
	model.elapsed = panelElapsed(started, now)
	titles := make(map[string]string)
	for _, task := range opened.Tasks {
		titles[task.ID] = task.Title
	}
	tasks := make(map[string]*panelTaskView)
	for _, task := range graph.Tasks {
		view := &panelTaskView{task: task, detail: PanelDetail{Task: task.TaskID, Title: titles[task.TaskID], Reason: task.BlockerCode}}
		if len(task.Attempts) > 0 {
			view.attempt = task.Attempts[len(task.Attempts)-1]
			view.detail.Attempt = view.attempt.Execution
			if view.attempt.Question != nil && view.attempt.Question.Answer == nil {
				view.detail.Question = view.attempt.Question.Prompt
			}
		}
		tasks[task.TaskID] = view
	}
	settled, terminal, terminalAt := model.readEvents(records, tasks, now)
	model.Header.Elapsed = panelAge(started, now)
	if terminal != "" {
		model.Header.State = terminal
		model.Header.Elapsed = panelAge(started, terminalAt)
	}
	active := ""
	bestPriority := -1
	for _, task := range graph.Tasks {
		view := tasks[task.TaskID]
		attention := view.attention(now)
		priority := panelAttentionPriority(attention.Kind)
		if priority > bestPriority || (priority == bestPriority && active != "" && view.lastAt.After(tasks[active].lastAt)) {
			if priority > 0 || task.State == routing.GraphTaskRunning || task.State == routing.GraphTaskPreparing {
				active = task.TaskID
				bestPriority = priority
				model.Attention = attention
			}
		}
		if task.State == routing.GraphTaskIntegrated {
			model.Progress.TasksDone++
		}
		runtime, execution := "-", "-"
		if view.attempt.Execution > 0 {
			runtime = view.attempt.Runtime.Provider + "/" + view.attempt.Runtime.Model
			execution = fmt.Sprint(view.attempt.Execution)
		}
		model.rows = append(model.rows, panelTSVRow{task.TaskID, string(task.Domain) + "/" + string(task.Complexity), runtime, execution, string(task.State), panelTaskDetail(task, view.attempt, records, now)})
	}
	if active != "" {
		context := tasks[active].context
		model.Context.Executor, model.Context.Model, model.Context.Reasoning = context.Executor, context.Model, context.Reasoning
	}
	if terminal == "" {
		switch model.Attention.Kind {
		case "question":
			model.Header.State = StateWaitingInput
		case "limit":
			model.Header.State = "limit_wait"
		case "blocked":
			model.Header.State = StateBlocked
		}
	} else if terminal != StateWaitingInput && terminal != StateBlocked {
		model.Attention = PanelAttention{Kind: "none"}
	}
	if selected == "" {
		selected = active
	}
	if view := tasks[selected]; view != nil {
		model.Detail = view.detail
	}
	model.Progress.TasksTotal = len(graph.Tasks)
	model.groupWaves(graph, tasks, settled)
	return model
}

func (model *PanelView) readEvents(records []journal.Record, tasks map[string]*panelTaskView, now time.Time) (map[int]string, string, time.Time) {
	settled := make(map[int]string)
	sessions, failures := make(map[string]bool), make(map[string]bool)
	var terminalAt time.Time
	terminal := ""
	for _, record := range records {
		var event panelEvent
		if json.Unmarshal(record.Detail, &event) != nil {
			continue
		}
		switch record.Kind {
		case KindStarted, KindAttempts, KindAnswer:
			terminal = ""
		case KindTerminal:
			terminal, terminalAt = event.State, record.At
		}
		if event.Error != "" {
			model.Health.LastError = event.Error
		}
		if record.Kind == KindFailure {
			reason := panelFailureText(event)
			if reason != "" {
				model.Health.LastError = reason
			}
			key := fmt.Sprintf("%s/%d", record.TaskID, event.Execution)
			if !event.Blocked && !failures[key] {
				failures[key] = true
				if event.SameRuntime {
					model.Context.Retries++
				} else if event.NextRuntime.Provider != "" || event.NextRuntime.Model != "" {
					model.Context.Escalations++
				}
			}
		}
		if record.Kind == KindStarted {
			key := fmt.Sprintf("%s/%d", record.TaskID, event.Execution)
			if !sessions[key] {
				sessions[key] = true
				model.Context.Sessions++
			}
		}
		if record.Kind == KindSettled {
			if event.FinalHead != "" {
				model.Header.Head = event.FinalHead
				settled[event.Wave] = event.FinalHead
			}
			if task := tasks[event.ConflictTask]; task != nil {
				task.conflict = event.FinalHead
			}
		}
		if task := tasks[record.TaskID]; task != nil {
			task.apply(record, event, now)
		}
	}
	return settled, terminal, terminalAt
}

func (model *PanelView) groupWaves(graph routing.DeliveryGraph, tasks map[string]*panelTaskView, settled map[int]string) {
	assigned := make(map[string]int)
	for _, wave := range graph.Waves {
		row := PanelWave{Number: wave.Number, Base: wave.BaseHeadSHA, Integrated: settled[wave.Number]}
		for _, id := range wave.TaskIDs {
			if index, exists := assigned[id]; exists {
				if head := settled[wave.Number]; head != "" {
					model.Waves[index].Integrated = head
				}
			} else if task := tasks[id]; task != nil {
				row.Rows = append(row.Rows, task.row())
			}
		}
		if len(wave.TaskIDs) > 0 && len(row.Rows) == 0 {
			continue
		}
		for _, task := range row.Rows {
			assigned[task.Task] = len(model.Waves)
		}
		row.summarise()
		if row.Total > 0 && row.Done == row.Total {
			model.Progress.WavesDone++
		}
		model.Waves = append(model.Waves, row)
	}
	model.Progress.WavesTotal = len(model.Waves)
	pending := PanelWave{}
	for _, task := range graph.Tasks {
		if _, exists := assigned[task.TaskID]; !exists {
			pending.Rows = append(pending.Rows, tasks[task.TaskID].row())
			for _, dependency := range task.Dependencies {
				found := false
				for _, existing := range pending.Dependencies {
					if existing == dependency {
						found = true
						break
					}
				}
				if !found {
					pending.Dependencies = append(pending.Dependencies, dependency)
				}
			}
		}
	}
	if len(pending.Rows) > 0 {
		pending.summarise()
		model.Waves = append(model.Waves, pending)
	}
}

func (task *panelTaskView) apply(record journal.Record, event panelEvent, now time.Time) {
	task.detail.LastRecord = panelRecordText(record)
	task.detail.LastAge = panelAge(record.At, now)
	task.lastAt = record.At
	if record.Kind == KindFailure && !event.Blocked && !event.SameRuntime && event.Execution == task.attempt.Execution-1 {
		task.escalation = event.NextRuntime.Provider + "/" + event.NextRuntime.Model
	}
	if event.Execution != task.attempt.Execution {
		return
	}
	switch record.Kind {
	case KindStarted:
		task.context.Executor, task.context.Model, task.context.Reasoning = event.Executor, event.Model, event.Reasoning
		task.limit = nil
		fallthrough
	case KindWorktree:
		var root string
		if json.Unmarshal(event.Worktree, &root) != nil {
			var worktree attemptWorktree
			if json.Unmarshal(event.Worktree, &worktree) == nil {
				root = worktree.Root
			}
		}
		if root != "" {
			task.detail.Worktree = root
		}
	case KindProgress:
		if event.Criterion > 0 && (event.State == "START" || event.State == "DONE") {
			task.detail.Criterion, task.detail.CriterionState = event.Criterion, event.State
		}
		task.limit = nil
	case KindQuestion:
		task.detail.Question = event.Question
		task.limit = nil
	case KindAnswer:
		task.detail.Question = ""
	case KindLimitWait:
		task.limit = &event
	case KindFinished:
		task.limit = nil
		task.detail.Reason = event.Error
	case KindGates:
		var report gates.Report
		if json.Unmarshal(record.Detail, &report) == nil {
			task.report = report
		}
		task.limit = nil
	case KindFailure:
		task.detail.Reason = panelFailureText(event)
		task.limit = nil
	case KindCandidate:
		task.conflict = ""
		task.escalation = ""
	}
	if event.RunID != "" {
		task.detail.LogPath = filepath.Join(".batuta", "runs", event.RunID+".out.log")
	}
}

func (task *panelTaskView) attention(now time.Time) PanelAttention {
	attention := PanelAttention{Kind: "none"}
	switch task.task.State {
	case routing.GraphTaskBlocked:
		return PanelAttention{"blocked", task.task.TaskID, task.detail.Reason, "o opens the log"}
	case routing.GraphTaskWaitingInput:
		return PanelAttention{"question", task.task.TaskID, task.detail.Question, "r answers"}
	case routing.GraphTaskRunning, routing.GraphTaskPreparing:
		attention.Task = task.task.TaskID
		switch {
		case task.limit != nil:
			event := task.limit
			attention.Kind = "limit"
			attention.Text = fmt.Sprintf("%s usage limit · wait %d", task.context.Executor, event.Wait)
			if !event.ResetAt.IsZero() {
				attention.Text += fmt.Sprintf(" · resumes %s (%s)", event.ResetAt.Format("15:04"), panelAge(now, event.ResetAt).Round(time.Second))
			} else if event.Seconds > 0 {
				attention.Text += fmt.Sprintf(" · waits %ds", event.Seconds)
			}
			attention.Hint = "wait for reset"
		case task.conflict != "":
			attention.Kind, attention.Text, attention.Hint = "conflict", "re-executing on "+task.conflict, "o opens the log"
		case task.escalation != "":
			attention.Kind, attention.Text, attention.Hint = "escalated", "escalated to "+task.escalation, "o opens the log"
		default:
			attention.Task = ""
		}
	}
	return attention
}

func panelAttentionPriority(kind string) int {
	switch kind {
	case "blocked":
		return 5
	case "question":
		return 4
	case "limit":
		return 3
	case "conflict":
		return 2
	case "escalated":
		return 1
	default:
		return 0
	}
}

func (task *panelTaskView) row() PanelRow {
	commit := task.task.IntegratedCommitSHA
	if commit == "" {
		commit = task.attempt.CandidateCommitSHA
	}
	return PanelRow{Task: task.task.TaskID, Title: task.detail.Title, State: string(task.task.State), Attempt: task.detail.Attempt, Gates: panelGateColumns(task.report), Commit: commit}
}

func (wave *PanelWave) summarise() {
	wave.Total = len(wave.Rows)
	wave.State = "pending"
	for _, row := range wave.Rows {
		switch row.State {
		case "integrated":
			wave.Done++
		case "blocked":
			wave.State = "blocked"
		case "running", "preparing", "waiting_input", "candidate":
			if wave.State != "blocked" {
				wave.State = "running"
			}
		}
	}
	if wave.Total > 0 && wave.Done == wave.Total {
		wave.State = "integrated"
	}
}

func panelGateColumns(report gates.Report) [4]string {
	columns := [4]string{panelGate(report.Finished), panelGate(report.Tree), panelGate(report.Tests), panelGate(report.Scope)}
	if columns[1] == "pass" && strings.Contains(report.Tree.Signal, "silent") {
		columns[1] = "silent"
	}
	for _, proof := range report.Proofs {
		if panelGate(proof) == "fail" {
			columns[3] = "fail"
		} else if panelGate(proof) == "pending" && columns[3] != "fail" {
			columns[3] = "pending"
		}
	}
	if report.Verifier != nil {
		verdict := panelGate(*report.Verifier)
		if verdict == "fail" {
			columns[3] = "fail"
		} else if verdict == "pending" && columns[3] != "fail" {
			columns[3] = "pending"
		}
	}
	return columns
}

func panelGate(verdict gates.Verdict) string {
	if verdict.Name == "" {
		return "pending"
	}
	if verdict.Pass {
		return "pass"
	}
	return "fail"
}

func panelFailureText(event panelEvent) string {
	if len(event.Feedback) > 0 {
		return strings.Join(event.Feedback, "\n")
	}
	if event.Error != "" {
		return event.Error
	}
	if event.Reason != "" {
		return event.Reason
	}
	return event.Blocker
}

func panelRecordText(record journal.Record) string {
	return strings.Join(strings.Fields(string(record.Kind)+" "+record.TaskID+" "+recordSummary(record)), " ")
}

func panelAge(at, now time.Time) time.Duration {
	if at.IsZero() {
		return 0
	}
	return max(0, now.Sub(at))
}
