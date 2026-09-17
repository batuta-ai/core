package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/worktree"
)

const SupervisionEventLimit = 4 << 10

// SupervisionOptions binds a passive observer to one delivery and durable cursor.
// CursorPath defaults to a private delivery cursor under .batuta/runs/supervision.
// An explicit path must be absolute and outside the journal directory. Now observes
// freshness only; the observer has no executor, answer or process-launch hook.
type SupervisionOptions struct {
	Workspace  string
	Delivery   string
	CursorPath string
	Now        func() time.Time
}

type SupervisionEvidence struct {
	Path     string `json:"path"`
	Sequence int    `json:"sequence"`
	Digest   string `json:"digest"`
}

// SupervisionEvent contains allowlisted metadata and a reference to the full
// record. Raw questions, errors, graphs, logs and artifact paths stay in the
// journal; consumers must separately redact any evidence they choose to load.
type SupervisionEvent struct {
	ReviewID        string              `json:"review_id,omitempty"`
	ReviewOutcome   string              `json:"review_outcome,omitempty"`
	ReviewState     string              `json:"review_state,omitempty"`
	ID              string              `json:"id"`
	Delivery        string              `json:"delivery"`
	Sequence        int                 `json:"sequence"`
	Kind            journal.Kind        `json:"kind"`
	At              time.Time           `json:"at"`
	TaskID          string              `json:"task_id,omitempty"`
	Execution       int                 `json:"execution,omitempty"`
	QuestionID      string              `json:"question_id,omitempty"`
	TerminalState   string              `json:"terminal_state,omitempty"`
	Completed       bool                `json:"completed"`
	RecoveryPending bool                `json:"recovery_pending,omitempty"`
	Action          string              `json:"action"`
	Evidence        SupervisionEvidence `json:"evidence"`
}

type SupervisionObservation struct {
	Review        *SupervisionReviewJob `json:"review,omitempty"`
	Cursor        int                   `json:"cursor"`
	Presence      string                `json:"presence"`
	LastActivity  time.Time             `json:"last_activity"`
	TerminalState string                `json:"terminal_state,omitempty"`
	Completed     bool                  `json:"completed"`
	Events        []SupervisionEvent    `json:"events"`
	Pending       []SupervisionEvent    `json:"pending"`
}

type supervisionEntry struct {
	Event        SupervisionEvent `json:"event"`
	Acknowledged bool             `json:"acknowledged"`
}

type supervisionCursor struct {
	Review    *SupervisionReviewJob `json:"review,omitempty"`
	Version   int                   `json:"version"`
	Workspace string                `json:"workspace"`
	Delivery  string                `json:"delivery"`
	Sequence  int                   `json:"sequence"`
	Digest    string                `json:"digest"`
	Outbox    []supervisionEntry    `json:"outbox"`
}

// ObserveSupervision performs one local observation without model calls or
// delivery mutations. Events contains only newly persisted events. Pending
// includes durable unread events from previous observations, including crashes
// after persistence but before the caller received the result.
func ObserveSupervision(opts SupervisionOptions) (SupervisionObservation, error) {
	opts, err := normalizeSupervisionOptions(opts)
	if err != nil {
		return SupervisionObservation{}, err
	}
	release, err := guardPresence(opts.CursorPath)
	if err != nil {
		return SupervisionObservation{}, err
	}
	defer release()
	cursor, err := readSupervisionCursor(opts)
	if err != nil {
		return SupervisionObservation{}, err
	}
	records, err := readSupervisionRecords(opts)
	if err != nil {
		return SupervisionObservation{}, err
	}
	if cursor.Sequence > len(records) || (cursor.Sequence > 0 && records[cursor.Sequence-1].Digest != cursor.Digest) {
		return SupervisionObservation{}, errors.New("loop: supervision journal no longer matches durable cursor")
	}
	result := SupervisionObservation{Cursor: len(records), Review: supervisionReviewCandidateInWorkspace(opts.Workspace, opts.Delivery, records)}
	result.Review, err = loadSupervisionReview(opts, result.Review)
	if err != nil {
		return SupervisionObservation{}, err
	}
	reviewAdded := cursor.Review == nil && result.Review != nil
	cursor.Review = result.Review
	for _, record := range records[cursor.Sequence:] {
		event, err := supervisionEvent(opts.Delivery, record)
		if err != nil {
			return SupervisionObservation{}, err
		}
		if event != nil {
			cursor.Outbox = append(cursor.Outbox, supervisionEntry{Event: *event})
			result.Events = append(result.Events, *event)
		}
	}
	reviewEvent, err := supervisionReviewEvent(opts, result.Review, len(records))
	if err != nil {
		return SupervisionObservation{}, err
	}
	if reviewEvent != nil {
		exists := false
		for _, entry := range cursor.Outbox {
			exists = exists || entry.Event.ID == reviewEvent.ID
		}
		if !exists {
			cursor.Outbox = append(cursor.Outbox, supervisionEntry{Event: *reviewEvent})
			result.Events = append(result.Events, *reviewEvent)
			reviewAdded = true
		}
	}

	if len(records) > 0 {
		last := records[len(records)-1]
		changed := cursor.Sequence != last.Seq
		result.LastActivity = last.At
		if last.Kind == KindTerminal {
			event, err := supervisionEvent(opts.Delivery, last)
			if err != nil {
				return SupervisionObservation{}, err
			}
			var terminal terminalDetail
			if err := json.Unmarshal(last.Detail, &terminal); err != nil {
				return SupervisionObservation{}, err
			}
			if !terminal.CleanupPending && !terminal.BookkeepingPending && len(terminal.Deletions) > 0 && supervisionDeletionsAbsent(opts.Workspace, terminal.Deletions) {
				event.RecoveryPending = false
				event.Completed = terminal.State == StateDone
			}
			// Refresh the current terminal event under its original identity. An
			// acknowledgment remains consumed even when deletion evidence changes.
			for i := range cursor.Outbox {
				entry := &cursor.Outbox[i]
				if entry.Event.ID == event.ID && (entry.Event.Completed != event.Completed || entry.Event.RecoveryPending != event.RecoveryPending) {
					entry.Event = *event
					changed = true
				}
			}
			if last.Seq > cursor.Sequence {
				for i := range result.Events {
					if result.Events[i].ID == event.ID {
						result.Events[i] = *event
					}
				}
			}
			result.TerminalState, result.Completed = event.TerminalState, event.Completed
		}
		if changed || reviewAdded {
			cursor.Sequence, cursor.Digest = last.Seq, last.Digest
			if err := writeSupervisionCursor(opts.CursorPath, cursor); err != nil {
				return SupervisionObservation{}, err
			}
		}
	}
	for _, entry := range cursor.Outbox {
		if !entry.Acknowledged {
			result.Pending = append(result.Pending, entry.Event)
		}
	}
	result.Presence, _ = Presence(opts.Workspace, opts.Delivery, opts.Now())
	return result, nil
}

func supervisionDeletionsAbsent(workspace string, refs []worktree.ParkedRef) bool {
	// Bound the entire lookup, including malformed or oversized historical intent.
	if len(refs) > 128 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	git, err := (publication.ExecutableResolver{}).Resolve("git")
	if err != nil {
		return false
	}
	run := func(args ...string) (publication.CommandResult, error) {
		return (publication.ExecRunner{}).Run(ctx, publication.Command{
			Executable: git, Args: args, Directory: workspace,
			StdoutLimit: SupervisionEventLimit, StderrLimit: SupervisionEventLimit,
		})
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Ref, "refs/") || len(ref.Ref) > SupervisionEventLimit {
			return false
		}
		if _, err := run("check-ref-format", ref.Ref); err != nil {
			return false
		}
		// --exists checks exact names, including dangling symbolic refs. Only
		// exit 2 means absent; unsupported git versions and lookup errors do not.
		result, err := run("show-ref", "--exists", "--", ref.Ref)
		if err == nil || result.ExitCode != 2 || ctx.Err() != nil || result.StdoutTruncated || result.StderrTruncated {
			return false
		}
	}
	return true
}

// AcknowledgeSupervision records successful consumption of an existing event.
// A sink must accept the event ID idempotently to avoid duplicate notifications:
// a crash after sending but before acknowledgment leaves an at-least-once window.
// With no configured sink, leave the event pending; a finished chat turn does
// not itself provide a running observer or asynchronous notification delivery.
func AcknowledgeSupervision(opts SupervisionOptions, eventID string) error {
	opts, err := normalizeSupervisionOptions(opts)
	if err != nil {
		return err
	}
	release, err := guardPresence(opts.CursorPath)
	if err != nil {
		return err
	}
	defer release()
	cursor, err := readSupervisionCursor(opts)
	if err != nil {
		return err
	}
	for i := range cursor.Outbox {
		if cursor.Outbox[i].Event.ID == eventID {
			if cursor.Outbox[i].Acknowledged {
				return nil
			}
			cursor.Outbox[i].Acknowledged = true
			return writeSupervisionCursor(opts.CursorPath, cursor)
		}
	}
	return errors.New("loop: unknown supervision event")
}

func normalizeSupervisionOptions(opts SupervisionOptions) (SupervisionOptions, error) {
	if opts.CursorPath == "" && filepath.IsAbs(opts.Workspace) && journal.ValidDeliveryID(opts.Delivery) {
		directory := filepath.Join(opts.Workspace, ".batuta", "runs", "supervision")
		if err := os.MkdirAll(directory, 0700); err != nil {
			return opts, err
		}
		opts.CursorPath = filepath.Join(directory, opts.Delivery+".json")
	}
	if !filepath.IsAbs(opts.Workspace) || !filepath.IsAbs(opts.CursorPath) || !journal.ValidDeliveryID(opts.Delivery) {
		return opts, errors.New("loop: supervision requires absolute workspace and cursor paths and an explicit delivery ID")
	}
	opts.Workspace, opts.CursorPath = filepath.Clean(opts.Workspace), filepath.Clean(opts.CursorPath)
	journalDir := filepath.Join(opts.Workspace, journal.Dir)
	if opts.CursorPath == journalDir || strings.HasPrefix(opts.CursorPath, journalDir+string(filepath.Separator)) {
		return opts, errors.New("loop: supervision cursor must be outside the journal directory")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return opts, nil
}

func readSupervisionRecords(opts SupervisionOptions) ([]journal.Record, error) {
	path := filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".jsonl")
	data, err := readSupervisionFile(path, 256<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil, journal.ErrUnknownDelivery
	}
	if err != nil {
		return nil, err
	}
	// Append publishes a record with its newline. Do not interpret an incomplete
	// suffix, even if it already happens to contain syntactically valid JSON.
	end := bytes.LastIndexByte(data, '\n') + 1
	return journal.Decode(bytes.NewReader(data[:end]))
}

func readSupervisionCursor(opts SupervisionOptions) (supervisionCursor, error) {
	cursor := supervisionCursor{Version: 1, Workspace: opts.Workspace, Delivery: opts.Delivery}
	data, err := readSupervisionFile(opts.CursorPath, 256<<20)
	if errors.Is(err, os.ErrNotExist) {
		return cursor, nil
	}
	if err != nil {
		return cursor, err
	}
	cursor = supervisionCursor{}
	if err := json.Unmarshal(data, &cursor); err != nil {
		return cursor, fmt.Errorf("loop: supervision cursor: %w", err)
	}
	if cursor.Version != 1 || cursor.Workspace != opts.Workspace || cursor.Delivery != opts.Delivery || cursor.Sequence < 0 || (cursor.Sequence == 0) != (cursor.Digest == "") {
		return cursor, errors.New("loop: invalid or mismatched supervision cursor")
	}
	previous := 0
	for _, entry := range cursor.Outbox {
		event := entry.Event
		if event.Kind == "review" {
			if event.Delivery != opts.Delivery || event.Sequence < 1 || event.Sequence > cursor.Sequence || len(event.ReviewID) != 64 || len(event.Evidence.Digest) != 71 || event.ID != opts.Delivery+":review:"+event.ReviewID+":"+event.Evidence.Digest[7:] {
				return cursor, errors.New("loop: invalid supervision review outbox identity")
			}
			continue
		}
		if event.Sequence <= previous || event.Sequence > cursor.Sequence || event.Delivery != opts.Delivery || event.ID != supervisionEventID(opts.Delivery, event.Sequence) {
			return cursor, errors.New("loop: invalid supervision outbox identity")
		}
		previous = event.Sequence
	}
	return cursor, nil
}

func writeSupervisionCursor(path string, cursor supervisionCursor) error {
	return writeSupervisionJSON(path, cursor)
}

func writeSupervisionJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 256<<20 {
		return errors.New("loop: supervision cursor is over budget")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".supervision-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		return err
	}
	return replaceSupervisionFile(file.Name(), path)
}

func supervisionEventID(delivery string, sequence int) string {
	return fmt.Sprintf("%s:%d", delivery, sequence)
}

func supervisionEvent(delivery string, record journal.Record) (*SupervisionEvent, error) {
	action := ""
	switch record.Kind {
	case KindStarted:
		action = "Executor is running; observe progress while the runner retains execution ownership."
	case KindQuestion:
		action = "Inspect the referenced question and use the bound-answer API only within an authorized policy."
	case KindFailure:
		action = "Inspect the recorded failure and attempt evidence; classification alone does not authorize a retry."
	case KindFinished:
		action = "Executor exited; wait for gates and an explicit delivery terminal record."
	case KindInterrupted:
		action = "Inspect the interruption; reconcile uncertain submissions before considering a resume."
	case KindLimitWait:
		action = "Observe the recorded limit wait; no intervention is authorized by this event."
	case KindTerminal:
		action = "Inspect the delivery terminal state and any pending recovery work."
	default:
		return nil, nil
	}
	var detail struct {
		Execution  int    `json:"execution"`
		QuestionID string `json:"request_id"`
	}
	if len(record.Detail) > 0 {
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			return nil, fmt.Errorf("loop: supervision record %d metadata: %w", record.Seq, err)
		}
	}
	event := &SupervisionEvent{
		ID: supervisionEventID(delivery, record.Seq), Delivery: delivery, Sequence: record.Seq,
		Kind: record.Kind, At: record.At, TaskID: supervisionIdentifier(record.TaskID),
		Execution: max(0, detail.Execution), QuestionID: supervisionIdentifier(detail.QuestionID), Action: action,
		Evidence: SupervisionEvidence{Path: journal.Dir + "/" + delivery + ".jsonl", Sequence: record.Seq, Digest: record.Digest},
	}
	if record.Kind == KindTerminal {
		var terminal terminalDetail
		if err := json.Unmarshal(record.Detail, &terminal); err != nil {
			return nil, fmt.Errorf("loop: supervision terminal record %d: %w", record.Seq, err)
		}
		switch terminal.State {
		case StateDone, StateBlocked, StateWaitingInput, StateWaitingPlan, StateCanceled, StateAbandoned:
			event.TerminalState = terminal.State
		default:
			return nil, fmt.Errorf("loop: supervision terminal record %d has unknown state", record.Seq)
		}
		event.RecoveryPending = terminal.CleanupPending || terminal.BookkeepingPending || len(terminal.Deletions) > 0
		event.Completed = terminal.State == StateDone && !event.RecoveryPending
	}
	data, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if len(data) > SupervisionEventLimit {
		return nil, errors.New("loop: supervision event exceeds 4 KiB")
	}
	return event, nil
}

func supervisionIdentifier(value string) string {
	if len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("_.:-", char)) {
			return ""
		}
	}
	return value
}

// SuperviseOptions configures one foreground observer. Interval must be between
// 100 ms and one minute. Sleep allows an embedding host to supply its clock.
// Review optionally invokes the engine only after a completed delivery.
// Execution enables runner continuation after an authorized policy answer.
type SuperviseOptions struct {
	Review    *SupervisionReviewOptions
	Observer  SupervisionOptions
	Interval  time.Duration
	Once      bool
	Sink      SupervisionSink
	Policy    *SupervisionPolicy
	Output    io.Writer
	Sleep     func(context.Context, time.Duration) error
	Execution *Options
}

type supervisionReport struct {
	Review    *SupervisionReviewJob `json:"review,omitempty"`
	Delivery  string                `json:"delivery"`
	Cursor    int                   `json:"cursor"`
	State     string                `json:"state"`
	Presence  string                `json:"presence"`
	Completed bool                  `json:"completed"`
	Pending   int                   `json:"pending"`
	Outbox    string                `json:"outbox"`
	Decision  *SupervisionDecision  `json:"decision,omitempty"`
}

type supervisionDeliveryResult struct {
	SupervisionNotification
	Notification string `json:"notification"`
}

// Supervise observes until completion, once mode or cancellation. Only changed
// reports are written as JSON lines. No sink leaves events durably unread; a
// failed sink is retried on the bounded interval (once mode returns an error).
// A foreground process must remain running to observe new work. Neither stdout
// nor a completed chat turn provides asynchronous chat notification delivery.
func Supervise(ctx context.Context, opts SuperviseOptions) error {
	if opts.Interval < 100*time.Millisecond || opts.Interval > time.Minute {
		return errors.New("loop: supervision interval must be between 100ms and 1m")
	}
	observer, err := normalizeSupervisionOptions(opts.Observer)
	if err != nil {
		return err
	}
	opts.Observer = observer
	if opts.Policy != nil && opts.Policy.Delivery != observer.Delivery {
		return errors.New("loop: supervision policy belongs to another delivery")
	}
	if opts.Output == nil {
		opts.Output = io.Discard
	}
	if opts.Sleep == nil {
		opts.Sleep = (&Runner{}).sleep
	}
	previous := map[string][32]byte{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		observation, err := ObserveSupervision(observer)
		if err != nil {
			return err
		}
		report := supervisionReport{Delivery: observer.Delivery, Cursor: observation.Cursor,
			State: observation.TerminalState, Presence: observation.Presence, Completed: observation.Completed, Pending: len(observation.Pending), Outbox: observer.CursorPath}
		if report.State == "" {
			report.State = "open"
		}
		report.Review = observation.Review
		if opts.Review != nil && observation.Completed {
			report.Review, err = RunSupervisionReview(ctx, observer, *opts.Review)
			if err != nil {
				return err
			}
			observation, err = ObserveSupervision(observer)
			if err != nil {
				return err
			}
			report.Pending = len(observation.Pending)
		}
		failed := false
		current := map[string][32]byte{}
		// Bound each observation to 32 deliveries; the cursor retains overflow.
		for _, event := range observation.Pending[:min(32, len(observation.Pending))] {
			if ctx.Err() != nil {
				return nil
			}
			result := supervisionDeliveryResult{SupervisionNotification: SupervisionNotification{Event: event, State: report.State, Presence: report.Presence}, Notification: "unconfigured"}
			if _, err := encodeSupervisionNotification(result.SupervisionNotification); err != nil {
				return err
			}
			if opts.Sink != nil {
				notifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := opts.Sink.Notify(notifyCtx, result.SupervisionNotification)
				cancel()
				result.Notification = "failed"
				if err == nil {
					if err := AcknowledgeSupervision(observer, event.ID); err != nil {
						return err
					}
					result.Notification = "acknowledged"
					report.Pending--
				} else {
					failed = true
				}
			}
			if err := writeSupervisionReport(opts.Output, event.ID, result, previous, current); err != nil {
				return err
			}
		}
		var policyErr error
		if opts.Policy != nil && ctx.Err() == nil {
			report.Decision, policyErr = supervisePolicy(ctx, opts)
			if report.Decision != nil && report.Decision.Outcome == "answered" {
				observation, err = ObserveSupervision(observer)
				if err != nil {
					return err
				}
				report.Cursor, report.State, report.Presence = observation.Cursor, observation.TerminalState, observation.Presence
				report.Completed, report.Pending = observation.Completed, len(observation.Pending)
				report.Review = observation.Review
				if policyErr == nil && opts.Review != nil && observation.Completed {
					report.Review, err = RunSupervisionReview(ctx, observer, *opts.Review)
					if err != nil {
						return err
					}
					observation, err = ObserveSupervision(observer)
					if err != nil {
						return err
					}
					report.Pending = len(observation.Pending)
				}
				if report.State == "" {
					report.State = "open"
				}
			}
		}
		if err := writeSupervisionReport(opts.Output, "summary", report, previous, current); err != nil {
			return err
		}
		previous = current
		if ctx.Err() != nil {
			return nil
		}
		if policyErr != nil {
			return policyErr
		}
		if opts.Once {
			if failed {
				return errors.New("loop: local notification failed; events remain pending in the supervision cursor")
			}
			return nil
		}
		reviewWaiting := opts.Review != nil && report.Review != nil && report.Review.ID != "" && report.Review.State == "pending"
		if observation.Completed && !reviewWaiting && !failed && (opts.Sink == nil || report.Pending == 0) {
			return nil
		}
		if err := opts.Sleep(ctx, opts.Interval); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func writeSupervisionReport(output io.Writer, key string, value any, previous, current map[string][32]byte) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > SupervisionEventLimit {
		return errors.New("loop: supervision output exceeds 4 KiB; inspect the durable cursor")
	}
	digest := sha256.Sum256(data)
	current[key] = digest
	if previous[key] == digest {
		return nil
	}
	_, err = fmt.Fprintln(output, string(data))
	return err
}

func supervisePolicy(ctx context.Context, opts SuperviseOptions) (*SupervisionDecision, error) {
	// Notification acknowledgments do not consume intervention authorization.
	cursor, err := readSupervisionCursor(opts.Observer)
	if err != nil {
		return nil, err
	}
	for _, entry := range cursor.Outbox {
		event := entry.Event
		if event.Kind == "review" && opts.Policy.Correction != nil && event.ReviewID == opts.Policy.Correction.ReviewID && event.Evidence.Digest == opts.Policy.Correction.ReportDigest {
			decision, err := InterveneSupervision(opts.Observer, event.ID, opts.Policy)
			return &decision, err
		}
		if event.Kind != KindQuestion || event.TaskID != opts.Policy.TaskID || event.Execution != opts.Policy.Execution || event.QuestionID != opts.Policy.QuestionID {
			continue
		}
		if opts.Execution != nil {
			decision, err := continueSupervision(ctx, opts, event)
			return &decision, err
		}
		decision, err := InterveneSupervision(opts.Observer, event.ID, opts.Policy)
		return &decision, err
	}
	return nil, nil
}
