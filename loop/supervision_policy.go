package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

const SupervisionProposeCorrection = "propose_correction"

const SupervisionContinueApprovedTask = "continue_approved_task"

const SupervisionRoutineAnswer = "The existing approved plan assigns this task to you. Continue only that task within its approved scope and boundaries, preserving its tests and verification requirements. This answer grants no additional permissions, scope, quota, or authorization to replay uncertain execution; stop for reconciliation or any decision outside the existing approval."

// SupervisionPolicy is supplied by the operator, never derived from worker
// prose. Ownership attests either the existing task assignment or an approved
// correction of the exact reviewed contract. Supervise can resume only the
// approved task answer, with explicit execution settings.
type SupervisionPolicy struct {
	Correction     *SupervisionCorrectionPolicy `json:"correction,omitempty"`
	Delivery       string                       `json:"delivery"`
	TaskID         string                       `json:"task_id"`
	Execution      int                          `json:"execution"`
	QuestionID     string                       `json:"question_id"`
	QuestionDigest string                       `json:"question_digest"`
	Action         string                       `json:"action"`
	Ownership      string                       `json:"ownership"`
	PlanEvidence   SupervisionEvidence          `json:"plan_evidence"`
	MaxAttempts    int                          `json:"max_attempts"`
}

type SupervisionDecision struct {
	Correction   *SupervisionCorrectionProposal `json:"correction,omitempty"`
	EventID      string                         `json:"event_id"`
	QuestionID   string                         `json:"question_id"`
	Evidence     SupervisionEvidence            `json:"evidence"`
	PlanEvidence SupervisionEvidence            `json:"plan_evidence"`
	PolicyDigest string                         `json:"policy_digest,omitempty"`
	Outcome      string                         `json:"outcome"`
	Reason       string                         `json:"reason"`
	Attempts     int                            `json:"attempts"`
	MaxAttempts  int                            `json:"max_attempts"`
	At           time.Time                      `json:"at"`
	Continuation string                         `json:"continuation,omitempty"`
	RunState     string                         `json:"run_state,omitempty"`
}

type supervisionDecisions struct {
	Version  int                            `json:"version"`
	Delivery string                         `json:"delivery"`
	Entries  map[string]SupervisionDecision `json:"entries"`
}

// InterveneSupervision attempts at most one bound answer or correction proposal
// per call. Observation stays passive and a nil policy leaves judgment pending. The delivery-wide
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
		job, loadErr := loadSupervisionReview(opts, supervisionReviewCandidateInWorkspace(opts.Workspace, opts.Delivery, records))
		if loadErr != nil {
			return SupervisionDecision{}, loadErr
		}
		reviewEvent, eventErr := supervisionReviewEvent(opts, job, len(records))
		if eventErr != nil {
			return SupervisionDecision{}, eventErr
		}
		if reviewEvent != nil && reviewEvent.ID == eventID {
			return proposeSupervisionCorrection(opts, *reviewEvent, job, policy)
		}
		reviewEvent, eventErr = loadSupervisionReviewReceipt(opts, job, eventID, len(records))
		if eventErr != nil {
			return SupervisionDecision{}, eventErr
		}
		if reviewEvent != nil {
			return proposeSupervisionCorrection(opts, *reviewEvent, job, policy)
		}
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
		if supervisionBoundAnswer(records, *event) != nil {
			if previous.PolicyDigest != decision.PolicyDigest {
				decision.Reason = "policy_mismatch"
				return decision, nil
			}
			previous.Outcome, previous.Reason = "answered", "explicit_scoped_policy"
			ledger.Entries[key] = previous
			return previous, writeSupervisionJSON(path, ledger)
		}
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
		if terminal.CleanupPending || terminal.BookkeepingPending || len(terminal.Deletions) != 0 {
			return "reconciliation_required"
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

// Correction authorization is operator evidence, never extracted from findings.
// The proposal reserves a child identity; it does not create or resume a runner.
type SupervisionCorrectionPolicy struct {
	ReviewID       string `json:"review_id"`
	ReportDigest   string `json:"report_digest"`
	SpecDigest     string `json:"spec_digest"`
	Delivery       string `json:"delivery"`
	MaxCorrections int    `json:"max_corrections"`
}

type SupervisionCorrectionProposal struct {
	RootReviewID   string `json:"root_review_id"`
	ReviewID       string `json:"review_id"`
	SourceDelivery string `json:"source_delivery"`
	Delivery       string `json:"delivery,omitempty"`
	FinalCommit    string `json:"final_commit"`
	SpecDigest     string `json:"spec_digest"`
	Depth          int    `json:"depth"`
	MaxCorrections int    `json:"max_corrections"`
}

type supervisionCorrections struct {
	Version int                            `json:"version"`
	Entries map[string]SupervisionDecision `json:"entries"`
}

func proposeSupervisionCorrection(opts SupervisionOptions, event SupervisionEvent, job *SupervisionReviewJob, policy *SupervisionPolicy) (SupervisionDecision, error) {
	decision := SupervisionDecision{EventID: event.ID, Evidence: event.Evidence, Outcome: "pending", Reason: "explicit_policy_required", At: opts.Now().UTC()}
	if policy == nil {
		return decision, nil
	}
	p := policy.Correction
	if p == nil || policy.Delivery != opts.Delivery || policy.Action != SupervisionProposeCorrection || policy.Ownership != "approved_correction" ||
		p.ReviewID != job.ID || p.ReportDigest != event.Evidence.Digest || p.SpecDigest != job.SpecDigest || !journal.ValidDeliveryID(p.Delivery) || p.Delivery == opts.Delivery ||
		p.MaxCorrections < 1 || p.MaxCorrections > 3 || policy.MaxAttempts < 1 || policy.MaxAttempts > 3 || !filepath.IsLocal(policy.PlanEvidence.Path) || len(policy.PlanEvidence.Path) > 1024 || len(policy.PlanEvidence.Digest) != 71 {
		decision.Reason = "policy_mismatch"
		return decision, nil
	}
	current, err := json.Marshal(job)
	if err != nil {
		return decision, err
	}
	currentDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(current))
	evidenceMatches := currentDigest == event.Evidence.Digest
	path := filepath.Join(opts.Workspace, journal.Dir, "supervision-corrections.json")
	release, err := guardPresence(path)
	if err != nil {
		return decision, err
	}
	defer release()
	ledger := supervisionCorrections{Version: 1, Entries: map[string]SupervisionDecision{}}
	data, err := readSupervisionFile(path, 256<<20)
	if err == nil {
		if err = json.Unmarshal(data, &ledger); err != nil {
			return decision, err
		}
		if ledger.Version != 1 || ledger.Entries == nil {
			return decision, errors.New("loop: invalid correction ledger")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return decision, err
	}
	proposal := SupervisionCorrectionProposal{RootReviewID: job.ID, ReviewID: job.ID, SourceDelivery: opts.Delivery, FinalCommit: job.FinalCommit, SpecDigest: job.SpecDigest, Depth: 1, MaxCorrections: p.MaxCorrections}
	decision.MaxAttempts = policy.MaxAttempts
	for id, entry := range ledger.Entries {
		c := entry.Correction
		if c == nil || c.ReviewID != id || c.RootReviewID == "" || c.Depth < 1 || c.MaxCorrections < 1 || c.MaxCorrections > 3 || entry.MaxAttempts < 1 || entry.MaxAttempts > 3 || entry.Attempts < 0 || entry.Attempts > entry.MaxAttempts {
			return decision, errors.New("loop: invalid correction budget")
		}
		if c.Delivery == opts.Delivery {
			proposal.RootReviewID = c.RootReviewID
			proposal.Depth = c.Depth + 1
			proposal.MaxCorrections = min(proposal.MaxCorrections, c.MaxCorrections)
			decision.MaxAttempts = min(decision.MaxAttempts, entry.MaxAttempts)
			if job.FinalCommit == c.FinalCommit || job.Base != c.FinalCommit || job.SpecDigest != c.SpecDigest {
				decision.Reason = "correction_identity_mismatch"
				return decision, nil
			}
		}
	}
	if previous, ok := ledger.Entries[job.ID]; ok {
		proposal = *previous.Correction
		proposal.MaxCorrections = min(proposal.MaxCorrections, p.MaxCorrections)
		decision.MaxAttempts = min(decision.MaxAttempts, previous.MaxAttempts)
	}
	// Attempts are a chain-wide usage budget for this local fixed policy. There
	// is no conductor model or token estimate, and no paid fallback is invoked.
	for _, entry := range ledger.Entries {
		if entry.Correction.RootReviewID == proposal.RootReviewID {
			decision.Attempts = max(decision.Attempts, entry.Attempts)
			decision.MaxAttempts = min(decision.MaxAttempts, entry.MaxAttempts)
			proposal.MaxCorrections = min(proposal.MaxCorrections, entry.Correction.MaxCorrections)
		}
	}
	decision.MaxAttempts = max(decision.Attempts, decision.MaxAttempts)
	encoded, _ := json.Marshal(policy)
	decision.PolicyDigest = fmt.Sprintf("%x", sha256.Sum256(encoded))
	decision.PlanEvidence = policy.PlanEvidence
	decision.Correction = &proposal
	persist := func() (SupervisionDecision, error) {
		ledger.Entries[job.ID] = decision
		return decision, writeSupervisionJSON(path, ledger)
	}
	previous := ledger.Entries[job.ID]
	if !evidenceMatches {
		decision.Reason = "review_evidence_mismatch"
		if previous.Outcome != "proposed" || previous.Evidence.Digest != currentDigest || job.State != "reported" || (job.Outcome != "FIX_BEFORE_SHIP" && job.Outcome != "REWORK") {
			return persist()
		}
		// A stale receipt cannot replace a current proposal, but the job digest
		// alone does not verify the artifact and spec bytes checked below.
	}
	if previous.Outcome != "proposed" && (decision.Attempts >= decision.MaxAttempts || proposal.Depth > proposal.MaxCorrections) {
		decision.Outcome = "exhausted"
		decision.Reason = "correction_chain_budget"
		return persist()
	}
	if job.State != "reported" || (job.Outcome != "FIX_BEFORE_SHIP" && job.Outcome != "REWORK") {
		decision.Reason = "review_requires_judgment"
		return persist()
	}
	if err := supervisionReviewArtifacts(job, false); err != nil {
		decision.Reason = "review_evidence_mismatch"
		return persist()
	}
	spec, err := readSupervisionFile(job.Spec, 32<<20)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(spec)) != job.SpecContentDigest {
		decision.Reason = "review_evidence_mismatch"
		return persist()
	}
	reviewed, err := routing.ParsePlan(job.Slug, spec)
	if err != nil || reviewed.Set.Digest != job.SpecDigest {
		decision.Reason = "review_evidence_mismatch"
		return persist()
	}
	if previous.Outcome == "proposed" {
		if !evidenceMatches {
			return decision, nil
		}
		return previous, nil
	}
	// Readonly local validation is safe to repeat after a crash. Persisting the
	// reservation first charges failed attempts and never replays external work.
	decision.Attempts++
	decision.Reason = "proposal_attempt_recorded"
	if _, err := persist(); err != nil {
		return decision, err
	}
	plan, err := readSupervisionFile(filepath.Join(opts.Workspace, policy.PlanEvidence.Path), 1<<20)
	if err != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(plan)) != policy.PlanEvidence.Digest {
		decision.Reason = "plan_evidence_mismatch"
		return persist()
	}
	parsed, err := routing.ParsePlan(job.Slug, plan)
	if err != nil || parsed.Set.Digest != job.SpecDigest {
		decision.Reason = "scope_expansion_requires_decision"
		return persist()
	}
	// Historical task digests omit plan prose that Brief executes. Bind that
	// prose to the immutable reviewed spec without changing journal identities.
	if parsed.Title != reviewed.Title {
		decision.Reason = "plan_title_mismatch"
		return persist()
	}
	if parsed.Goal != reviewed.Goal {
		decision.Reason = "plan_goal_mismatch"
		return persist()
	}
	for _, task := range reviewed.Tasks {
		if parsed.ContextFor(task.Number) != reviewed.ContextFor(task.Number) {
			decision.Reason = "plan_context_mismatch"
			return persist()
		}
	}
	entries, err := os.ReadDir(filepath.Join(opts.Workspace, journal.Dir))
	if err != nil {
		return decision, err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".lock") {
			decision.Reason = "ownership_unresolved"
			return persist()
		}
	}
	if proposal.Delivery != "" && proposal.Delivery != p.Delivery {
		decision.Reason = "correction_identity_mismatch"
		return persist()
	}
	if _, err := os.Lstat(filepath.Join(opts.Workspace, journal.Dir, p.Delivery+".jsonl")); !errors.Is(err, os.ErrNotExist) {
		decision.Reason = "correction_delivery_exists"
		return persist()
	}
	for id, entry := range ledger.Entries {
		if entry.Correction.SourceDelivery == p.Delivery || (id != job.ID && entry.Correction.Delivery == p.Delivery) {
			decision.Reason = "correction_delivery_reserved"
			return persist()
		}
	}
	proposal.Delivery = p.Delivery
	decision.Outcome, decision.Reason = "proposed", "explicit_scoped_policy; conductor must create the correction delivery and review its new commit"
	return persist()
}

// The intent is separate from the answer budget: answering is not execution.
// Its guard spans Resume and Run; normal delivery ownership also excludes CLI runners.
type supervisionContinuation struct {
	Version      int                 `json:"version"`
	Delivery     string              `json:"delivery"`
	EventID      string              `json:"event_id"`
	PolicyDigest string              `json:"policy_digest"`
	Settings     json.RawMessage     `json:"settings"`
	Stage        string              `json:"stage"`
	Answer       SupervisionEvidence `json:"answer"`
	Activity     SupervisionEvidence `json:"activity"`
	RunState     string              `json:"run_state,omitempty"`
}

func supervisionSettings(opts Options) ([]byte, error) {
	if opts.Transport == nil || opts.Transport.Mode == "" || executor.ValidateTransport(opts.Transport.Mode) != nil || opts.Skills == "" || opts.Inventory == nil || opts.Plan != "" {
		return nil, errors.New("loop: supervision continuation requires explicit transport, skills and inventory, without a plan override")
	}
	verifier := "cli"
	if opts.VerifierTransport != nil {
		verifier = opts.VerifierTransport.Mode
	}
	environment, _ := json.Marshal(opts.Environment)
	return json.Marshal(struct {
		Skills, Transport, Verifier, EnvironmentDigest                        string
		Parallel, MaxWaves, MaxLimitWaits                                     int
		TaskTimeout, TestTimeout, LimitWaitDefault, LimitBuffer, LimitHorizon time.Duration
		KeepWorktrees                                                         bool
	}{opts.Skills, opts.Transport.Mode, verifier, fmt.Sprintf("%x", sha256.Sum256(environment)),
		opts.Parallel, opts.MaxWaves, opts.MaxLimitWaits, opts.TaskTimeout, opts.TestTimeout, opts.LimitWaitDefault, opts.LimitBuffer, opts.LimitHorizon, opts.KeepWorktrees})
}

func supervisionBoundAnswer(records []journal.Record, event SupervisionEvent) *journal.Record {
	for i := range records {
		record := &records[i]
		if record.Seq <= event.Sequence || record.Kind != KindAnswer || record.TaskID != event.TaskID {
			continue
		}
		var detail struct {
			Execution int
			Answer    string
		}
		var graph routing.DeliveryGraph
		if json.Unmarshal(record.Detail, &detail) != nil || detail.Execution != event.Execution || detail.Answer != SupervisionRoutineAnswer || json.Unmarshal(record.Graph, &graph) != nil {
			continue
		}
		task := graphTask(&graph, event.TaskID)
		if task == nil || len(task.Attempts) != event.Execution+1 {
			continue
		}
		attempt := task.Attempts[event.Execution-1]
		if attempt.Execution == event.Execution && attempt.Question != nil && attempt.Question.RequestID == event.QuestionID && attempt.Question.Answer != nil &&
			attempt.Question.Answer.QuestionOperationID == event.QuestionID && attempt.Question.Answer.LoopRunID == attempt.ChildRunID && attempt.Question.Answer.Value == SupervisionRoutineAnswer {
			return record
		}
	}
	return nil
}

func continueSupervision(ctx context.Context, opts SuperviseOptions, event SupervisionEvent) (decision SupervisionDecision, resultErr error) {
	execution := *opts.Execution
	if (execution.Workspace != "" && filepath.Clean(execution.Workspace) != opts.Observer.Workspace) || (execution.Resume != "" && execution.Resume != opts.Observer.Delivery) {
		return decision, errors.New("loop: supervision execution belongs to another delivery")
	}
	settings, err := supervisionSettings(execution)
	if err != nil {
		return decision, err
	}
	if !opts.Policy.matches(event) {
		return InterveneSupervision(opts.Observer, event.ID, opts.Policy)
	}
	encoded, _ := json.Marshal(opts.Policy)
	intent := supervisionContinuation{Version: 1, Delivery: event.Delivery, EventID: event.ID, PolicyDigest: fmt.Sprintf("%x", sha256.Sum256(encoded)), Settings: settings, Stage: "pending"}
	path := filepath.Join(opts.Observer.Workspace, journal.Dir, fmt.Sprintf("%s.continuation-%d.json", event.Delivery, event.Sequence))
	release, err := guardPresence(path)
	if err != nil {
		return decision, err
	}
	defer release()
	intent, err = readSupervisionContinuation(path, intent)
	if err != nil {
		return decision, err
	}

	if ctx.Err() != nil {
		return decision, ctx.Err()
	}
	decision, err = InterveneSupervision(opts.Observer, event.ID, opts.Policy)
	if err != nil || decision.Outcome != "answered" {
		return decision, err
	}
	if decision.PolicyDigest != intent.PolicyDigest {
		return decision, errors.New("loop: supervision answer policy changed; reconciliation required")
	}
	decision.Continuation, decision.RunState = intent.Stage, intent.RunState
	persist := func(stage string) error {
		intent.Stage, decision.Continuation = stage, stage
		return writeSupervisionJSON(path, intent)
	}
	records, err := readSupervisionRecords(opts.Observer)
	if err != nil {
		return decision, err
	}
	answer := supervisionBoundAnswer(records, event)
	if answer == nil {
		decision.Continuation = "answer_unresolved"
		return decision, nil
	}
	evidence := SupervisionEvidence{Path: event.Evidence.Path, Sequence: answer.Seq, Digest: answer.Digest}
	if intent.Answer.Sequence != 0 && intent.Answer != evidence {
		return decision, errors.New("loop: supervision continuation answer changed")
	}
	intent.Answer = evidence
	activity := supervisionContinuationActivity(records, event, answer.Seq)
	if (intent.Stage == "resumed" && activity.Sequence == 0) || (intent.Activity.Sequence != 0 && intent.Activity != activity) {
		return decision, errors.New("loop: supervision continuation activity changed; reconciliation required")
	}
	if intent.Stage == "resumed" {
		return decision, nil
	}
	// Any new journal activity consumes this continuation. Recovery of an
	// interrupted execution belongs to the normal explicit reconciliation path.
	if records[len(records)-1].Seq != answer.Seq {
		if activity.Sequence != 0 {
			intent.Activity = activity
			saveErr := persist("resumed")
			return decision, saveErr
		}
		decision.Continuation = "reconciliation_required"
		return decision, nil
	}
	if answer.Seq < 2 || supervisionPendingQuestion(records[:answer.Seq-1], event) != "" {
		decision.Continuation = "reconciliation_required"
		return decision, nil
	}
	_, _, ownerErr := inspectPresence(filepath.Join(opts.Observer.Workspace, journal.Dir, event.Delivery+".lock"))
	if !errors.Is(ownerErr, os.ErrNotExist) {
		decision.Continuation = "ownership_unresolved"
		return decision, nil
	}
	plan, err := readSupervisionFile(filepath.Join(opts.Observer.Workspace, opts.Policy.PlanEvidence.Path), 1<<20)
	if err != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(plan)) != opts.Policy.PlanEvidence.Digest {
		decision.Continuation = "plan_evidence_mismatch"
		return decision, nil
	}
	if ctx.Err() != nil {
		return decision, ctx.Err()
	}
	if err := persist("acquiring"); err != nil {
		return decision, err
	}
	execution.Workspace, execution.Resume = opts.Observer.Workspace, event.Delivery
	runner, err := Resume(ctx, execution)
	if err != nil {
		decision.Continuation = "resume_failed"
		return decision, err
	}
	defer func() { resultErr = errors.Join(resultErr, runner.Release()) }()
	// Resume owns the delivery now. Recheck the exact journal boundary to close
	// the race with a manual answer/resume or a replaced/stale owner.
	current, err := readSupervisionRecords(opts.Observer)
	if err != nil {
		return decision, err
	}
	if runner.ownership.takenOver != nil || len(current) != len(records) || current[len(current)-1].Digest != answer.Digest {
		decision.Continuation = "reconciliation_required"
		return decision, nil
	}
	if err := persist("acquired"); err != nil {
		return decision, err
	}
	if ctx.Err() != nil {
		return decision, ctx.Err()
	}
	if err := persist("running"); err != nil {
		return decision, err
	}
	state, runErr := runner.Run(ctx)
	intent.RunState, decision.RunState = state, state
	current, readErr := readSupervisionRecords(opts.Observer)
	if readErr == nil {
		if activity := supervisionContinuationActivity(current, event, answer.Seq); activity.Sequence != 0 {
			intent.Activity = activity
			saveErr := persist("resumed")
			return decision, errors.Join(runErr, saveErr)
		}
	}
	decision.Continuation = "reconciliation_required"
	return decision, errors.Join(runErr, readErr)
}

func supervisionContinuationActivity(records []journal.Record, event SupervisionEvent, answerSequence int) SupervisionEvidence {
	for _, record := range records {
		if record.Seq <= answerSequence || record.TaskID != event.TaskID || (record.Kind != KindStarted && record.Kind != KindDispatchIntent) {
			continue
		}
		var detail struct{ Execution int }
		if json.Unmarshal(record.Detail, &detail) == nil && detail.Execution == event.Execution+1 {
			return SupervisionEvidence{Path: event.Evidence.Path, Sequence: record.Seq, Digest: record.Digest}
		}
	}
	return SupervisionEvidence{}
}

func readSupervisionContinuation(path string, expected supervisionContinuation) (supervisionContinuation, error) {
	data, err := readSupervisionFile(path, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return expected, writeSupervisionJSON(path, expected)
	}
	if err != nil {
		return expected, err
	}
	var previous supervisionContinuation
	if json.Unmarshal(data, &previous) != nil || previous.Version != 1 || previous.Delivery != expected.Delivery || previous.EventID != expected.EventID {
		return expected, errors.New("loop: invalid supervision continuation")
	}
	if previous.PolicyDigest != expected.PolicyDigest || !bytes.Equal(previous.Settings, expected.Settings) {
		return expected, errors.New("loop: supervision continuation policy or execution settings changed; reconciliation required")
	}
	switch previous.Stage {
	case "pending", "acquiring", "acquired", "running", "resumed":
	default:
		return expected, errors.New("loop: invalid supervision continuation stage")
	}
	return previous, nil
}
