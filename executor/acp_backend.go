package executor

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/batuta-ai/core/executor/acp"
)

// ACPBackend runs one configured session per execution. Open must create a
// fresh connection and return a bounded shutdown function that verifies worker
// exit after the connection closes. On an Open error, Open owns cleanup.
// The factory is the process-lifecycle boundary; no CLI fallback or retry runs.
type ACPBackend struct {
	Open           func(context.Context, Execution) (*acp.Connection, func() error, error)
	ClientInfo     acp.Implementation
	ModelConfigID  string
	EffortConfigID string
	// PermissionPolicy receives the existing execution identity and untrusted
	// action data. Nil rejects; the callback must obey acp.PermissionPolicy's contract.
	PermissionPolicy func(context.Context, Execution, acp.PermissionRequest) string
}

// Only verified shutdown after a pre-prompt compatibility rejection permits a
// caller to try CLI. Start errors do not establish worker cleanup.
type acpCompatibilityError struct{ error }

func (e *acpCompatibilityError) Unwrap() error { return e.error }

func (b ACPBackend) Execute(ctx context.Context, execution Execution) (result Result, err error) {
	started := time.Now()
	receipt := &Receipt{Submission: Submission{State: SubmissionNotSubmitted}, Transport: Transport{Outcome: TransportNotStarted}, Worker: WorkerClaim{Outcome: WorkerClaimUnknown}}
	result = Result{ExitCode: -1, Receipt: receipt}
	defer func() { result.Duration = time.Since(started) }()
	prompt := execution.Request.Brief
	if prompt == "" {
		prompt = execution.Request.Prompt
	}
	if b.Open == nil || !filepath.IsAbs(execution.Request.Cwd) || strings.TrimSpace(prompt) == "" {
		receipt.Transport.Failure = "configuration"
		return result, acp.ErrConfiguration
	}
	runCtx := ctx
	if execution.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, execution.Timeout)
		defer cancel()
	}
	if err := runCtx.Err(); err != nil {
		receipt.Transport.Outcome = TransportCanceled
		receipt.Transport.Failure = "canceled"
		return result, err
	}
	conn, shutdown, openErr := b.Open(runCtx, execution)
	if openErr != nil {
		receipt.Transport.Failure = "start"
		return result, errors.New("executor: ACP worker did not start")
	}
	if conn == nil || shutdown == nil {
		if conn != nil {
			conn.Close()
		}
		if shutdown != nil {
			_ = shutdown()
		}
		receipt.Transport.Failure = "start"
		return result, errors.New("executor: invalid ACP worker ownership")
	}
	defer func() {
		conn.Close()
		if shutdownErr := shutdown(); shutdownErr != nil {
			receipt.Transport = Transport{Outcome: TransportFailed, Failure: "shutdown"}
			result.Finished = false
			result.ExitCode = -1
			err = errors.Join(err, errors.New("executor: ACP worker shutdown unverified"))
		} else if receipt.Submission.State == SubmissionNotSubmitted && (errors.Is(err, acp.ErrConfiguration) || errors.Is(err, acp.ErrVersion)) {
			err = &acpCompatibilityError{err}
		}
	}()
	var policy acp.PermissionPolicy
	if b.PermissionPolicy != nil {
		policy = func(ctx context.Context, request acp.PermissionRequest) string {
			return b.PermissionPolicy(ctx, execution, request)
		}
	}
	config := acp.SessionConfig{Cwd: execution.Request.Cwd, Model: execution.Request.Model, Effort: execution.Request.Effort, ModelConfigID: b.ModelConfigID, EffortConfigID: b.EffortConfigID, ClientInfo: b.ClientInfo, PermissionPolicy: policy}
	if execution.Adapter.ACP != nil {
		config.Mode = execution.Adapter.ACP.Mode
		config.Meta = execution.Adapter.ACP.SessionMeta
	}
	session, sessionErr := acp.NewSession(runCtx, conn, config)
	if session != nil && session.EffortNotApplicable() {
		receipt.Effort = "not_applicable"
	}
	turn := acp.TurnResult{}
	sink := &progressSink{callback: execution.Progress}
	observer := &progressObserver{sink: sink}
	if sessionErr == nil {
		turn, sessionErr = session.Prompt(runCtx, prompt, func(text string) error {
			retained := text[:min(len(text), outputLimit-len(result.Stdout))]
			result.Truncated = result.Truncated || len(retained) != len(text)
			result.Stdout = append(result.Stdout, retained...)
			if len(retained) == 0 {
				return nil
			}
			if _, writeErr := observer.Write([]byte(retained)); writeErr != nil {
				return writeErr
			}
			if execution.Stdout != nil {
				n, writeErr := io.WriteString(execution.Stdout, retained)
				if writeErr != nil {
					return writeErr
				}
				if n != len(retained) {
					return io.ErrShortWrite
				}
			}
			return nil
		})
	}
	// A truncated final line cannot establish a question or progress event.
	if !result.Truncated {
		observer.flush()
	}
	result.Progress = sink.events
	if turn.SubmissionAttempted {
		receipt.Submission.State = SubmissionUncertain
	}
	if turn.Completed {
		receipt.Submission.State = SubmissionSubmitted
	}
	if turn.Usage != nil {
		receipt.Usage = &Usage{
			InputTokens:         turn.Usage.InputTokens,
			OutputTokens:        turn.Usage.OutputTokens,
			CacheReadTokens:     turn.Usage.CacheReadTokens,
			CacheWriteTokens:    turn.Usage.CacheWriteTokens,
			ReasoningTokens:     turn.Usage.ReasoningTokens,
			ReportedTotalTokens: turn.Usage.ReportedTotalTokens,
			CostAmount:          turn.Usage.CostAmount,
			CostCurrency:        turn.Usage.CostCurrency,
			CacheSemantics:      CacheSemantics(turn.Usage.CacheSemantics),
			Provenance:          turn.Usage.Provenance,
		}
	}
	if sessionErr != nil {
		receipt.Transport = acpFailure(sessionErr)
		result.TimedOut = errors.Is(sessionErr, context.DeadlineExceeded) && ctx.Err() == nil
		return result, sessionErr
	}
	receipt.Transport.Outcome = TransportCompleted
	receipt.Worker = WorkerClaim{Outcome: WorkerClaimedFailure, Detail: turn.StopReason}
	result.ExitCode = 1
	if turn.StopReason == "end_turn" {
		receipt.Worker.Outcome = WorkerClaimedSuccess
		result.ExitCode = 0
	}
	execution.Adapter.Outcome(&result)
	if result.Truncated {
		result.Question = ""
	}
	return result, nil
}

func acpFailure(err error) Transport {
	transport := Transport{Outcome: TransportFailed, Failure: "protocol"}
	var rpcErr *acp.RPCError
	switch {
	case errors.Is(err, acp.ErrPermissionDenied):
		transport.Failure = "permission_denied"
	case errors.Is(err, context.Canceled):
		transport.Outcome = TransportCanceled
		transport.Failure = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		transport.Failure = "timeout"
	case errors.Is(err, io.EOF), errors.Is(err, acp.ErrClosed), errors.Is(err, acp.ErrTransport):
		transport.Outcome = TransportDisconnected
		transport.Failure = "disconnected"
	case errors.Is(err, acp.ErrConfiguration):
		transport.Failure = "configuration"
	case errors.Is(err, acp.ErrOutput):
		transport.Failure = "output"
	case errors.As(err, &rpcErr):
		transport.Failure = "remote_error"
	}
	return transport
}
