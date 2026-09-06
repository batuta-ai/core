package loop

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

// RenderPanel renders one delivery from its journal without reading or writing
// external state. The opening record identifies the delivery by its plan slug.
// Journals currently omit delivery IDs, criterion totals and planned wave counts, so the
// panel displays completed items and admitted waves without denominators.
func RenderPanel(records []journal.Record, now time.Time) string {
	if len(records) == 0 {
		return "no records\n"
	}
	last := records[len(records)-1]
	var graph routing.DeliveryGraph
	if err := json.Unmarshal(last.Graph, &graph); err != nil {
		return "invalid graph\n"
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
	head := opened.Head
	for _, record := range records {
		if record.Kind == KindSettled {
			var detail struct {
				Head string `json:"final_head"`
			}
			if json.Unmarshal(record.Detail, &detail) == nil && detail.Head != "" {
				head = detail.Head
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "delivery %s   branch %s @ %s   wave %d   elapsed %s\n", panelValue(opened.Slug), panelValue(opened.Branch), panelCommit(head), len(graph.Waves), panelElapsed(started, now))
	table := tabwriter.NewWriter(&b, 0, 8, 2, ' ', 0)
	fmt.Fprintln(table, "task\tlane\texecutor/model\texec\tstate\tdetail")
	for _, task := range graph.Tasks {
		runtime, execution := "-", "-"
		var attempt routing.GraphTaskAttempt
		if len(task.Attempts) > 0 {
			attempt = task.Attempts[len(task.Attempts)-1]
			runtime = attempt.Runtime.Provider + "/" + attempt.Runtime.Model
			execution = strconv.Itoa(attempt.Execution)
		}
		detail := panelTaskDetail(task, attempt, records, now)
		fmt.Fprintf(table, "%s\t%s/%s\t%s\t%s\t%s\t%s\n", task.TaskID, task.Domain, task.Complexity, runtime, execution, task.State, strings.Join(strings.Fields(detail), " "))
	}
	_ = table.Flush()
	fmt.Fprintf(&b, "last     %s\n", strings.Join(strings.Fields(string(last.Kind)+" "+last.TaskID+" "+recordSummary(last)), " "))
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
