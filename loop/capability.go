package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/batuta-ai/core/executor"
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
	RequestedTransport string `json:"requested_transport,omitempty"`
	Pass               bool   `json:"pass"`
	Reason             string `json:"reason"`
	DurationMS         int64  `json:"duration_ms"`
	Tail               string `json:"tail,omitempty"`
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
		if selected, ok := backend.(executor.TransportBackend); ok {
			selected.CLI = probeCLIBackend{backend: selected.CLI, transport: &transport}
			backend = selected
		}
		probe, err := executor.ProbeCapability(ctx, backend, ac.adapter, executor.ProbeRoute{Model: route.Model, Effort: route.Effort}, ac.worktree.Root, min(r.opts.TaskTimeout, 5*time.Minute))
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		detail := capabilityProbeDetail{capabilityRoute: route, Pass: probe.Pass, Reason: probe.Reason, DurationMS: probe.Duration.Milliseconds(), Tail: r.redactDetail(probe.Tail)}
		if route.Transport == "auto" {
			detail.RequestedTransport = "auto"
		}
		detail.Transport = transport
		if err != nil && ctx.Err() == nil {
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
				r.probes[route] = detail
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
		if record.Kind != KindCapabilityProbe {
			continue
		}
		var detail capabilityProbeDetail
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			return fmt.Errorf("loop: capability probe journal: %w", err)
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
