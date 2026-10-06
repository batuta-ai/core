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
	if out, err := exec.Command("git", "-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	return dir
}

func probeHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestProbeCapabilityRequiresHead(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	head := probeHead(t, dir)
	backend := &probeBackend{run: func(e Execution) Result {
		if e.Request.Cwd != dir || !strings.Contains(e.Request.Brief, "git rev-parse HEAD") || !strings.Contains(e.Request.Brief, CapabilityMarker+" <sha>") {
			t.Errorf("request = %+v", e.Request)
		}
		return Result{Stdout: []byte("working\n  " + CapabilityMarker + " " + head + "  \n")}
	}}
	probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{}, dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Pass || probe.Reason != "" {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestProbeCapabilityMarkerReasons(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	head := probeHead(t, dir)
	tests := []struct {
		name   string
		dir    string
		stdout string
		want   string
	}{
		{"bare marker", dir, CapabilityMarker + "\n", ProbeMarkerMismatch},
		{"wrong SHA", dir, CapabilityMarker + " " + strings.Repeat("0", len(head)) + "\n", ProbeMarkerMismatch},
		{"extra field", dir, CapabilityMarker + " " + head + " copied\n", ProbeMarkerMismatch},
		{"missing marker", dir, "command unavailable\n", ProbeNoMarker},
		{"embedded marker", dir, "I will print " + CapabilityMarker + " " + head + " soon\n", ProbeNoMarker},
		{"no repository", tempDir(t), CapabilityMarker + " " + head + "\n", ProbeNoRepository},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backend := &probeBackend{run: func(Execution) Result { return Result{Stdout: []byte(tt.stdout)} }}
			probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{}, tt.dir, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if probe.Pass || probe.Reason != tt.want {
				t.Fatalf("probe = %+v, want %s", probe, tt.want)
			}
		})
	}
}

func TestProbeCapabilityTreeChangedFirst(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		result Result
	}{
		{"unfinished", Result{ExitCode: 1}},
		{"timeout", Result{ExitCode: -1, TimedOut: true}},
		{"limit", Result{RateLimited: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := gitRepo(t)
			backend := &probeBackend{run: func(e Execution) Result {
				if err := os.WriteFile(filepath.Join(e.Invocation.Dir, "stray.txt"), []byte("changed"), 0o644); err != nil {
					t.Fatal(err)
				}
				result := tt.result
				result.Stdout = []byte(CapabilityMarker + " " + probeHead(t, dir) + "\n")
				return result
			}}
			probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{}, dir, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if probe.Pass || probe.Reason != ProbeTreeChanged {
				t.Fatalf("probe = %+v", probe)
			}
		})
	}
	t.Run("committed change", func(t *testing.T) {
		t.Parallel()
		dir := gitRepo(t)
		oldHead := probeHead(t, dir)
		backend := &probeBackend{run: func(e Execution) Result {
			if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("changed"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "change.txt"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "changed"}} {
				if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			return Result{Stdout: []byte(CapabilityMarker + " " + oldHead + "\n")}
		}}
		probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{}, dir, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if probe.Pass || probe.Reason != ProbeTreeChanged {
			t.Fatalf("probe = %+v", probe)
		}
	})
}

func TestProbeCapability(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	head := probeHead(t, dir)
	backend := &probeBackend{run: func(Execution) Result {
		return Result{Stdout: []byte("working\n  BATUTA-CAPABLE " + head + "  \n"), Duration: 3 * time.Second}
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
	if strings.Contains(brief, "\n") || !strings.Contains(brief, "git rev-parse HEAD") {
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
			dir := gitRepo(t)
			head := probeHead(t, dir)
			result := test.result
			result.Stdout = []byte(strings.ReplaceAll(string(result.Stdout), CapabilityMarker+"\n", CapabilityMarker+" "+head+"\n"))
			backend := &probeBackend{run: func(Execution) Result { return result }}
			probe, err := ProbeCapability(context.Background(), backend, probeAdapter(), ProbeRoute{}, dir, time.Minute)
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
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsha=$(git rev-parse HEAD) || exit 1\nprintf 'BATUTA-CAPABLE %s\\n' \"$sha\"\n"), 0o755); err != nil {
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
