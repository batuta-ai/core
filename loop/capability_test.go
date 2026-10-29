package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/executor/acp"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

type stubProbeBackend func(executor.Execution) (executor.Result, error)

func (b stubProbeBackend) Execute(_ context.Context, e executor.Execution) (executor.Result, error) {
	return b(e)
}

// probeLeaks runs a delivery whose probe backend is stubbed and returns every
// journaled capability_probe and executor_incapable failure detail.
func probeLeaks(t *testing.T, backend stubProbeBackend) (root string, details []string) {
	t.Helper()
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatal(err)
	}
	r.probeBackend = backend
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v\n%s", err, out.String())
	}
	for _, record := range readJournal(t, f, r.Delivery()) {
		detail := string(record.Detail)
		if record.Kind == KindCapabilityProbe || record.Kind == KindFailure && strings.Contains(detail, `"blocker":"executor_incapable"`) {
			details = append(details, detail)
		}
	}
	return r.root, details
}

func requireRedacted(t *testing.T, root string, details []string) {
	t.Helper()
	var probes, failures int
	for _, detail := range details {
		if strings.Contains(detail, `"blocker":"executor_incapable"`) {
			failures++
		} else {
			probes++
		}
		for _, leak := range []string{"OPENAI_API_KEY", "sk-test-123", root} {
			if strings.Contains(detail, leak) {
				t.Fatalf("detail leaks %q: %s", leak, detail)
			}
		}
		if !strings.Contains(detail, "neighbour") {
			t.Fatalf("detail lost neighbouring line: %s", detail)
		}
	}
	if probes == 0 || failures == 0 {
		t.Fatalf("probes=%d failures=%d in %v", probes, failures, details)
	}
}

func TestLoopProbeTailRedacted(t *testing.T) {
	t.Parallel()
	var root string
	backend := stubProbeBackend(func(e executor.Execution) (executor.Result, error) {
		root = e.Invocation.Dir
		return executor.Result{Finished: true, Stdout: []byte("neighbour at " + root + "/file.txt\nOPENAI_API_KEY=sk-test-123\n")}, nil
	})
	root, details := probeLeaks(t, backend)
	requireRedacted(t, root, details)
}

func TestLoopProbeErrorRedacted(t *testing.T) {
	t.Parallel()
	backend := stubProbeBackend(func(e executor.Execution) (executor.Result, error) {
		return executor.Result{}, errors.New("neighbour failed in " + e.Invocation.Dir + "/x\nOPENAI_API_KEY=sk-test-123")
	})
	root, details := probeLeaks(t, backend)
	requireRedacted(t, root, details)
}

func TestLoopProbeErrorEmbeddedSecret(t *testing.T) {
	t.Parallel()
	backend := stubProbeBackend(func(e executor.Execution) (executor.Result, error) {
		return executor.Result{}, errors.New("neighbour failed in " + e.Invocation.Dir + "/x\nprovider error: OPENAI_API_KEY=sk-test-123")
	})
	root, details := probeLeaks(t, backend)
	requireRedacted(t, root, details)
}

func probeModels(t *testing.T, f fixture) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.state, "probes"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func probeRecords(t *testing.T, records []journal.Record) []capabilityProbeDetail {
	t.Helper()
	var probes []capabilityProbeDetail
	for _, record := range records {
		if record.Kind != KindCapabilityProbe {
			continue
		}
		if !bytes.Contains(record.Detail, []byte(`"reason":`)) {
			t.Fatalf("capability probe missing reason: %s", record.Detail)
		}
		var detail capabilityProbeDetail
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		probes = append(probes, detail)
	}
	return probes
}

func TestLoopProbeUsesRouteTransport(t *testing.T) {
	for _, mode := range []string{"cli", "acp"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			taskPrompts, probeOpens, acpOpens, acpShutdowns, activeSessions := 0, 0, 0, 0, 0
			if mode == "acp" {
				transport := loopACPTransport(t, func(e executor.Execution) (string, string) {
					taskPrompts++
					if e.Request.Brief != "task brief" {
						t.Errorf("ACP task session brief = %q", e.Request.Brief)
					}
					return "task answer\n", "end_turn"
				})
				open := transport.ACP.Open
				transport.ACP.Open = func(ctx context.Context, e executor.Execution) (*acp.Connection, func() error, error) {
					if activeSessions != 0 {
						t.Error("ACP task opened while probe session was still active")
					}
					if strings.Contains(e.Request.Brief, executor.CapabilityMarker) {
						probeOpens++
					}
					conn, shutdown, err := open(ctx, e)
					if err != nil {
						return conn, shutdown, err
					}
					acpOpens++
					activeSessions++
					return conn, func() error {
						defer func() { activeSessions--; acpShutdowns++ }()
						return shutdown()
					}, nil
				}
				opts.Transport = transport
			}
			r, err := New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := r.adapterLocked("codex")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "acp" {
				adapter.ACP = &executor.ACPLaunch{Run: "codex-acp", Version: "fixture-v1"}
			}
			if err := r.open(); err != nil {
				t.Fatal(err)
			}
			ac := attemptContext{taskID: "task_1", adapter: adapter, runtime: routing.RuntimeValue{Provider: "codex", Model: "fake-low", Reasoning: "low"}, worktree: attemptWorktree{Root: f.root}}
			probe, err := r.probeRoute(context.Background(), ac)
			if err != nil || !probe.Pass || probe.Transport != mode {
				t.Fatalf("probe = %+v, %v", probe, err)
			}
			if taskPrompts != 0 {
				t.Fatalf("task prompts during probe = %d", taskPrompts)
			}
			if mode == "acp" {
				if probeOpens != 1 || acpOpens != 1 || acpShutdowns != 1 || activeSessions != 0 {
					t.Fatalf("after probe: probe opens=%d opens=%d shutdowns=%d active=%d", probeOpens, acpOpens, acpShutdowns, activeSessions)
				}
				request := executor.Request{Brief: "task brief", Cwd: f.root, Model: "fake-low", Effort: "low"}
				invocation, err := adapter.Command(request)
				if err != nil {
					t.Fatal(err)
				}
				result, err := r.backend.Execute(context.Background(), executor.Execution{Adapter: adapter, Request: request, Invocation: invocation})
				if err != nil || !result.Finished || taskPrompts != 1 || probeOpens != 1 || acpOpens != 2 || acpShutdowns != 2 || activeSessions != 0 {
					t.Fatalf("task = %+v, %v; task prompts=%d probe opens=%d opens=%d shutdowns=%d active=%d", result, err, taskPrompts, probeOpens, acpOpens, acpShutdowns, activeSessions)
				}
			}
			if mode == "cli" {
				if models := probeModels(t, f); strings.Join(models, ",") != "fake-low" {
					t.Fatalf("CLI probes = %v", models)
				}
			} else if _, err := os.Stat(filepath.Join(f.state, "probes")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("CLI probe file exists or cannot be inspected: %v", err)
			}
			probes := probeRecords(t, readJournal(t, f, r.Delivery()))
			if len(probes) != 1 || probes[0].Transport != mode || !probes[0].Pass {
				t.Fatalf("journaled probes = %+v", probes)
			}
		})
	}
}

func TestLoopProbeAutoFallback(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	transport := loopACPTransport(t, func(executor.Execution) (string, string) {
		t.Error("unqualified ACP probe was submitted")
		return "", "end_turn"
	})
	transport.Mode = "auto"
	transport.Qualifications = nil
	opts.Transport = transport
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := r.adapterLocked("codex")
	if err != nil {
		t.Fatal(err)
	}
	adapter.ACP = &executor.ACPLaunch{Run: "codex-acp", Version: "fixture-v1"}
	if err := r.open(); err != nil {
		t.Fatal(err)
	}
	ac := attemptContext{taskID: "task_1", adapter: adapter, runtime: routing.RuntimeValue{Provider: "codex", Model: "fake-low", Reasoning: "low"}, worktree: attemptWorktree{Root: f.root}}
	probe, err := r.probeRoute(context.Background(), ac)
	if err != nil || !probe.Pass || probe.Transport != "cli" {
		t.Fatalf("probe = %+v, %v", probe, err)
	}
	if models := probeModels(t, f); strings.Join(models, ",") != "fake-low" {
		t.Fatalf("CLI probes = %v", models)
	}
	probes := probeRecords(t, readJournal(t, f, r.Delivery()))
	if len(probes) != 1 || probes[0].Transport != "cli" || probes[0].RequestedTransport != "auto" || !probes[0].Pass {
		t.Fatalf("journaled probes = %+v", probes)
	}
}

func TestLoopACPProbeShutdownRequiresReconciliation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "shutdown", true: "canceled_shutdown"}[canceled], func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prompts, opens := 0, 0
			transport := loopACPTransport(t, func(executor.Execution) (string, string) {
				prompts++
				t.Error("task prompt started after uncertain probe")
				return "", "end_turn"
			})
			open := transport.ACP.Open
			transport.ACP.Open = func(ctx context.Context, e executor.Execution) (*acp.Connection, func() error, error) {
				opens++
				if err := os.WriteFile(filepath.Join(e.Request.Cwd, "probe-work.txt"), []byte("uncertain probe work"), 0o644); err != nil {
					return nil, nil, err
				}
				conn, shutdown, err := open(ctx, e)
				if err != nil {
					return conn, shutdown, err
				}
				return conn, func() error {
					if err := shutdown(); err != nil {
						return err
					}
					if canceled {
						cancel()
					}
					return errors.New("shutdown unresolved")
				}, nil
			}
			opts.Transport = transport
			r := prepareACPAttempt(t, f, opts)
			if _, err := r.runPreparingWaves(ctx); err != nil {
				t.Fatal(err)
			}
			task, _ := r.graph.Task("task_1")
			if task.State != routing.GraphTaskBlocked || len(task.Attempts) != 1 || task.BlockerCode != blockerSubmissionUncertain {
				t.Fatalf("uncertain probe did not block: %+v", task)
			}
			if opens != 1 || prompts != 0 {
				t.Fatalf("ACP opens=%d task prompts=%d", opens, prompts)
			}
			records := readJournal(t, f, r.Delivery())
			probes := probeRecords(t, records)
			if len(probes) != 1 || !probes[0].ReconciliationRequired {
				t.Fatalf("probe evidence = %+v", probes)
			}
			if len(probes[0].Receipt) > executor.ReceiptLimit {
				t.Fatalf("probe receipt exceeds %d bytes: %d", executor.ReceiptLimit, len(probes[0].Receipt))
			}
			var receipt executor.Receipt
			if err := json.Unmarshal(probes[0].Receipt, &receipt); err != nil || receipt.Transport.Failure != "shutdown" {
				t.Fatalf("probe receipt = %+v, %v", receipt, err)
			}
			if counts := kinds(records); counts[KindFailure] != 1 || counts[KindSnapshot] == 0 || counts[KindStarted] != 0 {
				t.Fatalf("probe failure records = %v", counts)
			}
			if body, err := os.ReadFile(filepath.Join(task.Attempts[0].WorktreeRoot, "probe-work.txt")); err != nil || string(body) != "uncertain probe work" {
				t.Fatalf("probe work = %q, %v", body, err)
			}
			if err := r.Release(); err != nil {
				t.Fatal(err)
			}
			report, err := Reconcile(context.Background(), f.root, r.Delivery(), "task_1")
			if err != nil || !slices.Contains(report.Changed, "probe-work.txt") {
				t.Fatalf("probe work unavailable to reconciliation: %+v, %v", report, err)
			}
			resumeOpts := f.options("default", &out)
			resumeOpts.Transport = transport
			resumeOpts.Resume = r.Delivery()
			resumed, err := Resume(context.Background(), resumeOpts)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := resumed.Run(context.Background()); err != nil || state != StateBlocked {
				t.Fatalf("resumed Run() = %s, %v", state, err)
			}
			if opens != 1 || prompts != 0 {
				t.Fatalf("resume replayed probe: opens=%d task prompts=%d", opens, prompts)
			}
		})
	}
}

func TestLoopUnresolvedProbeIntentBlocksResume(t *testing.T) {
	for _, resumeMode := range []string{"acp", "default_cli", "auto"} {
		t.Run(resumeMode, func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			opens := 0
			transport := loopACPTransport(t, func(executor.Execution) (string, string) {
				t.Error("task prompt started after unresolved probe")
				return "", "end_turn"
			})
			open := transport.ACP.Open
			transport.ACP.Open = func(ctx context.Context, e executor.Execution) (*acp.Connection, func() error, error) {
				opens++
				return open(ctx, e)
			}
			opts.Transport = transport
			r := prepareACPAttempt(t, f, opts)
			if err := r.graph.BeginWaveAttempts(1, r.generation); err != nil {
				t.Fatal(err)
			}
			task, _ := r.graph.Task("task_1")
			attempt := task.Attempts[0]
			ac := attemptContext{taskID: "task_1", execution: attempt.Execution, runtime: attempt.Runtime, base: attempt.BaseHeadSHA}
			if err := r.ensureWorktree(context.Background(), &ac, attempt); err != nil {
				t.Fatal(err)
			}
			intent := capabilityProbeDetail{capabilityRoute: r.capabilityRoute(ac.runtime), Reason: "probe_unresolved", ReconciliationRequired: true}
			if err := r.record(KindCapabilityProbeIntent, ac.taskID, intent); err != nil {
				t.Fatal(err)
			}
			if err := r.Release(); err != nil {
				t.Fatal(err)
			}
			switch resumeMode {
			case "default_cli":
				opts.Transport = nil
			case "auto":
				auto := *transport
				auto.Mode = "auto"
				opts.Transport = &auto
			}
			opts.Resume = r.Delivery()
			resumed, err := Resume(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := resumed.Run(context.Background()); err != nil || state != StateBlocked {
				t.Fatalf("resumed Run() = %s, %v\n%s", state, err, out.String())
			}
			task, _ = resumed.graph.Task("task_1")
			if task.BlockerCode != blockerSubmissionUncertain || len(task.Attempts) != 1 || opens != 0 {
				t.Fatalf("unresolved probe replayed: task=%+v opens=%d", task, opens)
			}
			if counts := kinds(readJournal(t, f, r.Delivery())); counts[KindStarted] != 0 || counts[KindCapabilityProbe] != 0 {
				t.Fatalf("resume dispatched task or probe: %v", counts)
			}
		})
	}
}

func TestLoopProbeIntentOwnershipAndFinalFallback(t *testing.T) {
	t.Parallel()
	r := &Runner{
		probes:        map[capabilityRoute]capabilityProbeDetail{},
		pendingProbes: map[string]capabilityProbeDetail{},
		probing:       map[capabilityRoute]chan struct{}{},
	}
	intent := capabilityProbeDetail{capabilityRoute: capabilityRoute{Executor: "codex", Model: "fake-low", Effort: "low", Transport: "acp"}, Reason: "probe_unresolved", ReconciliationRequired: true}
	final := capabilityProbeDetail{capabilityRoute: intent.capabilityRoute, Pass: true, RequestedTransport: "auto"}
	final.Transport = "cli"
	record := func(kind journal.Kind, taskID string, detail capabilityProbeDetail) journal.Record {
		t.Helper()
		encoded, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		return journal.Record{Kind: kind, TaskID: taskID, Detail: encoded}
	}
	if err := r.loadCapabilityProbes([]journal.Record{
		record(KindCapabilityProbeIntent, "task_1", intent),
		record(KindCapabilityProbe, "task_2", final),
	}); err != nil {
		t.Fatal(err)
	}
	ac := attemptContext{taskID: "task_1", runtime: routing.RuntimeValue{Provider: "codex", Model: "fake-low", Reasoning: "low"}}
	probe, err := r.probeRoute(context.Background(), ac)
	if err != nil || !probe.ReconciliationRequired || probe.Reason != "probe_unresolved" {
		t.Fatalf("unrelated task cleared intent: %+v, %v", probe, err)
	}
	if err := r.loadCapabilityProbes([]journal.Record{record(KindCapabilityProbe, "task_1", final)}); err != nil {
		t.Fatal(err)
	}
	probe, err = r.probeRoute(context.Background(), ac)
	if err != nil || !probe.Pass || probe.ReconciliationRequired || len(r.pendingProbes) != 0 {
		t.Fatalf("final CLI fallback left unresolved intent: %+v, %v; pending=%v", probe, err, r.pendingProbes)
	}
}

func TestLoopOrdinaryIncapableProbeEscalatesOnce(t *testing.T) {
	for _, reason := range []string{"no_marker", "backend_error"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			var out bytes.Buffer
			r := prepareACPAttempt(t, f, f.options("default", &out))
			defer r.Release()
			r.probeBackend = stubProbeBackend(func(executor.Execution) (executor.Result, error) {
				if reason == "backend_error" {
					return executor.Result{}, errors.New("probe unavailable")
				}
				return executor.Result{Finished: true}, nil
			})
			if _, err := r.runPreparingWaves(context.Background()); err != nil {
				t.Fatal(err)
			}
			task, _ := r.graph.Task("task_1")
			if task.State != routing.GraphTaskPreparing || len(task.Attempts) != 2 || task.Attempts[1].Runtime.Model != "fake-mid" || task.Attempts[0].BlockerCode != blockerExecutorIncapable {
				t.Fatalf("ordinary probe escalation = %+v", task)
			}
			records := readJournal(t, f, r.Delivery())
			if probes := probeRecords(t, records); len(probes) != 1 || probes[0].ReconciliationRequired {
				t.Fatalf("ordinary probe rows = %+v", probes)
			}
			if counts := kinds(records); counts[KindFailure] != 1 || counts[KindCapabilityProbeIntent] != 0 {
				t.Fatalf("ordinary probe records = %v", counts)
			}
		})
	}
}

func TestLoopProbesRouteOnce(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	models := probeModels(t, f)
	if strings.Join(models, ",") != "fake-low,fake-mid" {
		t.Fatalf("probe models = %v", models)
	}
	probes := probeRecords(t, readJournal(t, f, r.Delivery()))
	if len(probes) != 2 {
		t.Fatalf("probe records = %+v", probes)
	}
	for _, probe := range probes {
		if !probe.Pass || probe.Executor != "codex" || probe.Effort != "low" && probe.Effort != "medium" || probe.Transport != "cli" || probe.DurationMS < 0 {
			t.Fatalf("probe detail = %+v", probe)
		}
	}
}

func TestLoopProbesLimitFallbackRoute(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("limit-budget", &out)
	opts.MaxLimitWaits = 1
	opts.MaxWaves = 1
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), ErrStopped.Error()) {
		t.Fatalf("Run() error = %v, want ErrStopped\n%s", err, out.String())
	}
	if models := probeModels(t, f); strings.Join(models, ",") != "fake-low,fake-mid" {
		t.Fatalf("probe models = %v", models)
	}
}

func TestLoopProbeReusedAfterResume(t *testing.T) {
	t.Parallel()
	f := setup(t)
	planPath := filepath.Join(f.root, ".batuta", "plans", "greetings.md")
	plan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	plan = []byte(strings.Replace(string(plan), "Add greeting three — backend/medium", "Add greeting three — backend/low", 1))
	if err := os.WriteFile(planPath, plan, 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", planPath)
	f.run(t, "commit", "-q", "-m", "test: route task three through low lane")
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.MaxWaves = 1
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), ErrStopped.Error()) {
		t.Fatalf("Run() error = %v, want ErrStopped\n%s", err, out.String())
	}
	if models := probeModels(t, f); strings.Join(models, ",") != "fake-low" {
		t.Fatalf("before resume probes = %v", models)
	}
	resumeOpts := f.options("default", &out)
	resumeOpts.Resume = r.Delivery()
	resumed, err := Resume(context.Background(), resumeOpts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("resumed Run() = %s, %v\n%s", state, err, out.String())
	}
	if models := probeModels(t, f); strings.Join(models, ",") != "fake-low" {
		t.Fatalf("after resume probes = %v", models)
	}
	if probes := probeRecords(t, readJournal(t, f, r.Delivery())); len(probes) != 1 {
		t.Fatalf("probe records = %+v", probes)
	}
}

func TestLoopIncapableEscalates(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("incapable-low", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	if models := probeModels(t, f); strings.Join(models, ",") != "fake-low,fake-mid" {
		t.Fatalf("probe models = %v", models)
	}
	var failures, startedLow, submittedLow int
	for _, record := range readJournal(t, f, r.Delivery()) {
		switch record.Kind {
		case KindFailure:
			if strings.Contains(string(record.Detail), `"blocker":"executor_incapable"`) {
				failures++
				if !strings.Contains(string(record.Detail), "no_marker") || !strings.Contains(string(record.Detail), "shell command unavailable") || strings.Contains(string(record.Detail), `"same_runtime":true`) {
					t.Fatalf("failure = %s", record.Detail)
				}
			}
		case KindStarted:
			if strings.Contains(string(record.Detail), `"model":"fake-low"`) {
				startedLow++
			}
		case KindDispatchIntent:
			if strings.Contains(string(record.Detail), `"model":"fake-low"`) {
				submittedLow++
			}
		}
	}
	if failures != 2 || startedLow != 0 || submittedLow != 0 {
		t.Fatalf("failures=%d startedLow=%d submittedLow=%d", failures, startedLow, submittedLow)
	}
}

func TestLoopIncapableAtTopBlocks(t *testing.T) {
	t.Parallel()
	f := setup(t)
	planPath := filepath.Join(f.root, ".batuta", "plans", "greetings.md")
	plan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	plan = []byte(strings.Replace(string(plan), "Add greeting one — backend/low", "Add greeting one — backend/high", 1))
	if err := os.WriteFile(planPath, plan, 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", planPath)
	f.run(t, "commit", "-q", "-m", "test: route task one through high lane")
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("incapable-all", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateBlocked {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	var topBlocked bool
	for _, record := range readJournal(t, f, r.Delivery()) {
		if record.Kind == KindFailure && record.TaskID == "task_1" && strings.Contains(string(record.Detail), `"blocker":"executor_incapable"`) && strings.Contains(string(record.Detail), `"blocked":true`) {
			topBlocked = true
		}
	}
	if !topBlocked {
		t.Fatalf("top route did not block with executor_incapable\n%s", out.String())
	}
}
