package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/journal"
)

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
