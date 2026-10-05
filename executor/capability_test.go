package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type probeBackend struct {
	run func(Execution) Result
	got Execution
}

func (b *probeBackend) Execute(_ context.Context, e Execution) (Result, error) {
	b.got = e
	result := b.run(e)
	e.Adapter.Outcome(&result)
	return result, nil
}

func probeAdapter() Adapter {
	return Adapter{Name: "fake", Run: "fake {model_flags} {brief}", Finished: "exit_code", LimitRegex: "usage limit"}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := tempDir(t)
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func TestProbeCapability(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	backend := &probeBackend{run: func(Execution) Result {
		return Result{Stdout: []byte("working\n  BATUTA-CAPABLE  \n"), Duration: 3 * time.Second}
	}}
	probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{Model: DefaultModel}, dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Pass || probe.Reason != "" || probe.ExitCode != 0 || probe.Duration != 3*time.Second {
		t.Fatalf("probe = %+v", probe)
	}
	if !strings.Contains(probe.Tail, CapabilityMarker) {
		t.Fatalf("tail = %q", probe.Tail)
	}
	if backend.got.Invocation.Dir != dir || backend.got.Timeout != time.Minute {
		t.Fatalf("execution = %+v", backend.got)
	}
	brief := backend.got.Request.Brief
	if strings.Contains(brief, "\n") || !strings.Contains(brief, "git status --porcelain && echo ok") {
		t.Fatalf("brief = %q", brief)
	}
}

func TestProbeCapabilityBoundsTail(t *testing.T) {
	t.Parallel()
	backend := &probeBackend{run: func(Execution) Result {
		return Result{Stdout: []byte(strings.Repeat("x", 10000) + "\n" + CapabilityMarker)}
	}}
	probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{}, tempDir(t), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.Tail) > probeTailBytes {
		t.Fatalf("tail is %d bytes", len(probe.Tail))
	}
}

func TestProbeCapabilityRedactsSecrets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		result Result
	}{
		{"stdout", Result{Stdout: []byte("before\nOPENAI_API_KEY=sk-test-123\nafter\n")}},
		{"stderr", Result{Stderr: []byte("before\nOPENAI_API_KEY=sk-test-123\nafter\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backend := &probeBackend{run: func(Execution) Result { return tt.result }}
			probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{}, tempDir(t), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(probe.Tail, "OPENAI_API_KEY") || strings.Contains(probe.Tail, "sk-test-123") {
				t.Fatalf("tail leaks secret: %q", probe.Tail)
			}
			if !strings.Contains(probe.Tail, "before") || !strings.Contains(probe.Tail, "after") {
				t.Fatalf("tail lost neighbouring lines: %q", probe.Tail)
			}
		})
	}
}

func TestProbeCapabilityReasons(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		result Result
		want   string
	}{
		{"marker inside a sentence", Result{Stdout: []byte("I will print BATUTA-CAPABLE soon\n")}, ProbeNoMarker},
		{"no output", Result{}, ProbeNoMarker},
		{"unfinished", Result{ExitCode: 1, Stdout: []byte(CapabilityMarker + "\n")}, ProbeUnfinished},
		{"timeout", Result{ExitCode: -1, TimedOut: true, Stdout: []byte(CapabilityMarker + "\n")}, ProbeTimeout},
		{"limit", Result{Stderr: []byte("Usage limit reached\n"), Stdout: []byte(CapabilityMarker + "\n")}, ProbeLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := &probeBackend{run: func(Execution) Result { return test.result }}
			probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{}, tempDir(t), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if probe.Pass || probe.Reason != test.want {
				t.Fatalf("probe = %+v, want reason %s", probe, test.want)
			}
		})
	}
}

func TestProbeCapabilityTreeChanged(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	backend := &probeBackend{run: func(e Execution) Result {
		if err := os.WriteFile(filepath.Join(e.Invocation.Dir, "stray.txt"), []byte("x"), 0o644); err != nil {
			t.Error(err)
		}
		return Result{Stdout: []byte(CapabilityMarker + "\n")}
	}}
	probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{}, dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Pass || probe.Reason != ProbeTreeChanged {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestProbeCapabilityRunsRealSubprocess(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := gitRepo(t)
	script := filepath.Join(tempDir(t), "capable")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ngit status --porcelain >/dev/null && echo BATUTA-CAPABLE\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{Name: "fake", Run: script + " {brief}", Finished: "exit_code"}
	probe, err := ProbeCapability(context.Background(), CLIBackend{Subprocess: NewSubprocess()}, adapter, ProbeRoute{}, dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Pass {
		t.Fatalf("probe = %+v", probe)
	}
}
