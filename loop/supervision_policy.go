package loop

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

const SupervisionContinueApprovedTask = "continue_approved_task"

const SupervisionRoutineAnswer = "The existing approved plan assigns this task to you. Continue only that task within its approved scope and boundaries, preserving its tests and verification requirements. This answer grants no additional permissions, scope, quota, or authorization to replay uncertain execution; stop for reconciliation or any decision outside the existing approval."

// SupervisionPolicy is supplied by the operator, never derived from worker
// prose. Ownership explicitly attests that the approved plan assigns this task
// to its worker. Only this fixed routine clarification is supported.
type SupervisionPolicy struct {
	Delivery       string              `json:"delivery"`
	TaskID         string              `json:"task_id"`
	Execution      int                 `json:"execution"`
	QuestionID     string              `json:"question_id"`
	QuestionDigest string              `json:"question_digest"`
	Action         string              `json:"action"`
	Ownership      string              `json:"ownership"`
	PlanEvidence   SupervisionEvidence `json:"plan_evidence"`
	MaxAttempts    int                 `json:"max_attempts"`
}

type SupervisionDecision struct {
	EventID      string              `json:"event_id"`
	QuestionID   string              `json:"question_id"`
	Evidence     SupervisionEvidence `json:"evidence"`
	PlanEvidence SupervisionEvidence `json:"plan_evidence"`
	PolicyDigest string              `json:"policy_digest,omitempty"`
	Outcome      string              `json:"outcome"`
	Reason       string              `json:"reason"`
	Attempts     int                 `json:"attempts"`
	MaxAttempts  int                 `json:"max_attempts"`
	At           time.Time           `json:"at"`
}

type supervisionDecisions struct {
	Version  int                            `json:"version"`
	Delivery string                         `json:"delivery"`
	Entries  map[string]SupervisionDecision `json:"entries"`
}

// InterveneSupervision attempts at most one bound answer per call. Observation
// stays passive and a nil policy leaves the question pending. The delivery-wide
// ledger shares budgets across cursor paths; persisting before submission makes
// a crash consume an attempt. Neither a crash nor a changed policy resets it.
// Notification acknowledgment is independent of intervention.
func InterveneSupervision(opts SupervisionOptions, eventID string, policy *SupervisionPolicy) (SupervisionDecision, error) {
	opts, err := normalizeSupervisionOptions(opts)
	if err != nil {
		return SupervisionDecision{}, err
	}
	path := filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".supervision.json")
	release, err := guardPresence(path)
	if err != nil {
		return SupervisionDecision{}, err
	}
	defer release()
	records, err := readSupervisionRecords(opts)
	if err != nil {
		return SupervisionDecision{}, err
	}
	var event *SupervisionEvent
	for _, record := range records {
		if supervisionEventID(opts.Delivery, record.Seq) == eventID {
			event, err = supervisionEvent(opts.Delivery, record)
			break
		}
	}
	if err != nil {
		return SupervisionDecision{}, err
	}
	if event == nil {
		return SupervisionDecision{}, errors.New("loop: unknown supervision event")
	}
	decision := SupervisionDecision{EventID: event.ID, QuestionID: event.QuestionID, Evidence: event.Evidence, Outcome: "pending", Reason: "explicit_policy_required", At: opts.Now().UTC()}
	if policy == nil {
		return decision, nil
	}
	if !policy.matches(*event) {
		decision.Reason = "policy_mismatch"
		return decision, nil
	}
	encoded, _ := json.Marshal(policy)
	decision.PolicyDigest = fmt.Sprintf("%x", sha256.Sum256(encoded))
	decision.PlanEvidence, decision.MaxAttempts = policy.PlanEvidence, policy.MaxAttempts
	ledger, err := readSupervisionDecisions(path, opts.Delivery)
	if err != nil {
		return decision, err
	}
	key := fmt.Sprintf("%s:%d:%s", event.TaskID, event.Execution, event.QuestionID)
	if previous, ok := ledger.Entries[key]; ok {
		if previous.Outcome == "answered" {
			return previous, nil
		}
		decision.Attempts = previous.Attempts
		decision.MaxAttempts = max(decision.Attempts, min(previous.MaxAttempts, policy.MaxAttempts))
	}
	persist := func() (SupervisionDecision, error) {
		ledger.Entries[key] = decision
		return decision, writeSupervisionJSON(path, ledger)
	}
	if decision.Attempts >= decision.MaxAttempts {
		decision.Reason = "attempt_limit"
		return persist()
	}
	plan, planErr := readSupervisionFile(filepath.Join(opts.Workspace, policy.PlanEvidence.Path), 1<<20)
	if planErr != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(plan)) != policy.PlanEvidence.Digest {
		decision.Reason = "plan_evidence_mismatch"
		return persist()
	}
	if reason := supervisionPendingQuestion(records, *event); reason != "" {
		decision.Reason = reason
		return persist()
	}
	// Even stale or unreadable ownership requires reconciliation by the owner;
	// supervision does not infer permission to take over a delivery.
	_, _, ownerErr := inspectPresence(filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".lock"))
	if !errors.Is(ownerErr, os.ErrNotExist) {
		decision.Reason = "ownership_unresolved"
		return persist()
	}
	decision.Attempts++
	decision.Reason = "answer_attempt_recorded"
	if _, err := persist(); err != nil {
		return decision, err
	}
	_, answerErr := answerDelivery(opts.Workspace, opts.Delivery, event.TaskID, event.Execution, event.QuestionID, SupervisionRoutineAnswer, opts.Now().UTC())
	if answerErr != nil {
		decision.Reason = "bound_answer_rejected"
	} else {
		decision.Outcome, decision.Reason = "answered", "explicit_scoped_policy"
	}
	result, saveErr := persist()
	return result, errors.Join(answerErr, saveErr)
}

func (policy SupervisionPolicy) matches(event SupervisionEvent) bool {
	return event.Kind == KindQuestion && event.TaskID != "" && event.QuestionID != "" && event.Execution > 0 &&
		policy.Delivery == event.Delivery && policy.TaskID == event.TaskID && policy.Execution == event.Execution &&
		policy.QuestionID == event.QuestionID && policy.QuestionDigest == event.Evidence.Digest &&
		policy.Action == SupervisionContinueApprovedTask && policy.Ownership == "approved_task" &&
		filepath.IsLocal(policy.PlanEvidence.Path) && len(policy.PlanEvidence.Path) <= 1024 &&
		len(policy.PlanEvidence.Digest) == 71 && policy.MaxAttempts >= 1 && policy.MaxAttempts <= 3
}

func readSupervisionDecisions(path, delivery string) (supervisionDecisions, error) {
	ledger := supervisionDecisions{Version: 1, Delivery: delivery, Entries: map[string]SupervisionDecision{}}
	data, err := readSupervisionFile(path, 256<<20)
	if errors.Is(err, os.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	ledger = supervisionDecisions{}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, err
	}
	if ledger.Version != 1 || ledger.Delivery != delivery || ledger.Entries == nil {
		return ledger, errors.New("loop: invalid supervision decisions")
	}
	for _, decision := range ledger.Entries {
		if decision.MaxAttempts < 1 || decision.MaxAttempts > 3 || decision.Attempts < 0 || decision.Attempts > decision.MaxAttempts {
			return ledger, errors.New("loop: invalid supervision attempt budget")
		}
	}
	return ledger, nil
}

func supervisionPendingQuestion(records []journal.Record, event SupervisionEvent) string {
	// An interrupted ACP submission can precede graph reconciliation. Inspect
	// structured dispatch evidence, never worker claims about success or safety.
	dispatches := map[string]dispatchDetail{}
	for _, record := range records {
		if record.Kind == KindDispatchIntent || record.Kind == KindDispatchResult {
			var detail dispatchDetail
			if json.Unmarshal(record.Detail, &detail) != nil {
				return "reconciliation_required"
			}
			dispatches[attemptKey(record.TaskID, detail.Execution)] = detail
		}
	}
	for _, dispatch := range dispatches {
		if dispatch.Backend == "cli" || dispatch.Submission == executor.SubmissionNotSubmitted {
			continue
		}
		var receipt executor.Receipt
		if dispatch.Submission != executor.SubmissionSubmitted || json.Unmarshal(dispatch.Receipt, &receipt) != nil ||
			receipt.Submission.State != executor.SubmissionSubmitted || receipt.Transport.Outcome != executor.TransportCompleted {
			return "reconciliation_required"
		}
	}
	last := records[len(records)-1]
	if last.Kind == KindTerminal {
		var terminal terminalDetail
		if json.Unmarshal(last.Detail, &terminal) != nil || terminal.State != StateWaitingInput {
			return "delivery_not_waiting"
		}
	}
	var graph routing.DeliveryGraph
	if json.Unmarshal(last.Graph, &graph) != nil {
		return "question_not_pending"
	}
	for _, task := range graph.Tasks {
		if task.State == routing.GraphTaskRunning || task.BlockerCode == blockerSubmissionUncertain {
			return "reconciliation_required"
		}
		for _, attempt := range task.Attempts {
			if attempt.BlockerCode == blockerSubmissionUncertain {
				return "reconciliation_required"
			}
		}
	}
	task := graphTask(&graph, event.TaskID)
	if task == nil || task.State != routing.GraphTaskWaitingInput || len(task.Attempts) == 0 {
		return "question_not_pending"
	}
	attempt := task.Attempts[len(task.Attempts)-1]
	if attempt.Execution != event.Execution || attempt.Question == nil || attempt.Question.RequestID != event.QuestionID || attempt.Question.Answer != nil {
		return "question_not_pending"
	}
	return ""
}
