package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/executor/acp"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

type capabilityRoute struct {
	Executor  string `json:"executor"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	Transport string `json:"transport"`
}

type capabilityProbeDetail struct {
	capabilityRoute
	RequestedTransport     string          `json:"requested_transport,omitempty"`
	Pass                   bool            `json:"pass"`
	Reason                 string          `json:"reason"`
	DurationMS             int64           `json:"duration_ms"`
	Tail                   string          `json:"tail,omitempty"`
	Receipt                json.RawMessage `json:"receipt,omitempty"`
	ReconciliationRequired bool            `json:"reconciliation_required,omitempty"`
}

type probeCLIBackend struct {
	backend   executor.Backend
	transport *string
}

func (b probeCLIBackend) Execute(ctx context.Context, execution executor.Execution) (executor.Result, error) {
	*b.transport = "cli"
	return b.backend.Execute(ctx, execution)
}

func (r *Runner) capabilityRoute(runtime routing.RuntimeValue) capabilityRoute {
	transport := "cli"
	if backend, ok := r.probeBackend.(executor.TransportBackend); ok && backend.Mode != "" {
		transport = backend.Mode
	}
	return capabilityRoute{Executor: runtime.Provider, Model: runtime.Model, Effort: runtime.Reasoning, Transport: transport}
}

func (r *Runner) probeRoute(ctx context.Context, ac attemptContext) (capabilityProbeDetail, error) {
	route := r.capabilityRoute(ac.runtime)
	for {
		r.mu.Lock()
		if intent, ok := r.pendingProbes[ac.taskID]; ok {
			r.mu.Unlock()
			return intent, nil
		}
		if detail, ok := r.probes[route]; ok {
			r.mu.Unlock()
			return detail, nil
		}
		if waiting, ok := r.probing[route]; ok {
			r.mu.Unlock()
			select {
			case <-waiting:
				continue
			case <-ctx.Done():
				return capabilityProbeDetail{}, ctx.Err()
			}
		}
		waiting := make(chan struct{})
		r.probing[route] = waiting
		r.mu.Unlock()

		started := time.Now()
		transport := route.Transport
		backend := r.probeBackend
		acpAttempted := false
		if selected, ok := backend.(executor.TransportBackend); ok {
			selected.CLI = probeCLIBackend{backend: selected.CLI, transport: &transport}
			if open := selected.ACP.Open; open != nil {
				selected.ACP.Open = func(ctx context.Context, execution executor.Execution) (*acp.Connection, func() error, error) {
					acpAttempted = true
					transport = "acp"
					intent := capabilityProbeDetail{capabilityRoute: route, Reason: "probe_unresolved", ReconciliationRequired: true}
					if route.Transport == "auto" {
						intent.RequestedTransport = "auto"
					}
					intent.Transport = "acp"
					if err := r.locked(KindCapabilityProbeIntent, ac.taskID, intent, func() error {
						r.pendingProbes[ac.taskID] = intent
						return nil
					}); err != nil {
						return nil, nil, err
					}
					return open(ctx, execution)
				}
			}
			backend = selected
		}
		probe, err := executor.ProbeCapability(ctx, backend, ac.adapter, executor.ProbeRoute{Model: route.Model, Effort: route.Effort}, ac.worktree.Root, min(r.opts.TaskTimeout, 5*time.Minute))
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		detail := capabilityProbeDetail{capabilityRoute: route, Pass: probe.Pass, Reason: probe.Reason, DurationMS: probe.Duration.Milliseconds(), Tail: r.redactDetail(probe.Tail)}
		if probe.Receipt != nil {
			receipt, marshalErr := executor.MarshalReceipt(*probe.Receipt)
			if marshalErr != nil {
				r.mu.Lock()
				delete(r.probing, route)
				close(waiting)
				r.mu.Unlock()
				return detail, marshalErr
			}
			detail.Receipt = receipt
		}
		detail.ReconciliationRequired = probe.Receipt != nil && probe.Receipt.Transport.Failure == "shutdown" ||
			err != nil && acpAttempted && (probe.Receipt == nil || probe.Receipt.Submission.State != executor.SubmissionNotSubmitted)
		if route.Transport == "auto" {
			detail.RequestedTransport = "auto"
		}
		detail.Transport = transport
		if err != nil && (ctx.Err() == nil || detail.ReconciliationRequired) {
			detail.Pass = false
			detail.Reason = "probe_error"
			tail := []rune(executor.DropSecretBearingLines(r.redactDetail(err.Error())))
			if len(tail) > 2048 {
				tail = tail[len(tail)-2048:]
			}
			detail.Tail = string(tail)
			detail.DurationMS = time.Since(started).Milliseconds()
			err = nil
		}
		r.mu.Lock()
		if err == nil {
			err = r.record(KindCapabilityProbe, ac.taskID, detail)
			if err == nil {
				if detail.ReconciliationRequired {
					r.pendingProbes[ac.taskID] = detail
				} else {
					r.probes[route] = detail
					if intent, ok := r.pendingProbes[ac.taskID]; ok && sameProbeRoute(intent.capabilityRoute, detail.capabilityRoute) {
						delete(r.pendingProbes, ac.taskID)
					}
				}
			}
		}
		delete(r.probing, route)
		close(waiting)
		r.mu.Unlock()
		return detail, err
	}
}

func (r *Runner) loadCapabilityProbes(records []journal.Record) error {
	for _, record := range records {
		if record.Kind != KindCapabilityProbe && record.Kind != KindCapabilityProbeIntent {
			continue
		}
		var detail capabilityProbeDetail
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			return fmt.Errorf("loop: capability probe journal: %w", err)
		}
		if record.Kind == KindCapabilityProbeIntent || detail.ReconciliationRequired {
			r.pendingProbes[record.TaskID] = detail
			continue
		}
		if intent, ok := r.pendingProbes[record.TaskID]; ok && sameProbeRoute(intent.capabilityRoute, detail.capabilityRoute) {
			delete(r.pendingProbes, record.TaskID)
		}
		r.probes[detail.capabilityRoute] = detail
		if detail.RequestedTransport == "auto" {
			route := detail.capabilityRoute
			route.Transport = "auto"
			r.probes[route] = detail
		}
	}
	return nil
}

func sameProbeRoute(a, b capabilityRoute) bool {
	return a.Executor == b.Executor && a.Model == b.Model && a.Effort == b.Effort
}

func (r *Runner) hasExternalFallback(taskID string, current routing.RuntimeValue) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cell := range r.generation.Cells {
		if !slices.Contains(cell.TaskIDs, taskID) {
			continue
		}
		candidates := append([]routing.RuntimeCandidate{cell.Selected}, cell.Fallbacks...)
		for index, candidate := range candidates {
			if candidate.ProviderID == current.Provider && candidate.ModelID == current.Model && candidate.Reasoning == current.Reasoning {
				return index+1 < len(candidates) && candidates[index+1].ProviderID != string(routing.ExecutorSelf)
			}
		}
	}
	return false
}

func (r *Runner) recordIncapable(ctx context.Context, ac attemptContext, probe capabilityProbeDetail) error {
	feedback := []string{"capability probe failed: " + probe.Reason}
	if probe.Tail != "" {
		feedback = append(feedback, probe.Tail)
	}
	policy := routing.FailurePolicy{RetryAllowed: r.hasExternalFallback(ac.taskID, ac.runtime)}
	return r.recordFailureWithPolicy(ctx, ac, nil, blockerExecutorIncapable, feedback, policy)
}
