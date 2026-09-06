package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
)

func TestParseProgress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		line      string
		criterion int
		state     string
		ok        bool
	}{
		{name: "start", line: "BATUTA-PROGRESS 1 START", criterion: 1, state: "START", ok: true},
		{name: "done with surrounding spaces", line: "  BATUTA-PROGRESS 42 DONE\t", criterion: 42, state: "DONE", ok: true},
		{name: "zero criterion", line: "BATUTA-PROGRESS 0 START"},
		{name: "negative criterion", line: "BATUTA-PROGRESS -1 DONE"},
		{name: "missing criterion", line: "BATUTA-PROGRESS DONE"},
		{name: "unknown state", line: "BATUTA-PROGRESS 1 WORKING"},
		{name: "lowercase state", line: "BATUTA-PROGRESS 1 done"},
		{name: "embedded", line: "message: BATUTA-PROGRESS 1 START"},
		{name: "trailing text", line: "BATUTA-PROGRESS 1 DONE now"},
		{name: "extra separator", line: "BATUTA-PROGRESS  1 START"},
		{name: "empty", line: ""},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			criterion, state, ok := ParseProgress(test.line)
			if criterion != test.criterion || state != test.state || ok != test.ok {
				t.Fatalf("ParseProgress(%q) = (%d, %q, %t), want (%d, %q, %t)", test.line, criterion, state, ok, test.criterion, test.state, test.ok)
			}
		})
	}
}

func TestSubprocessStreamsProgressEventsFromBothStreams(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}

	directory := t.TempDir()
	release := filepath.Join(directory, "release")
	executable := filepath.Join(directory, "progress-helper")
	script := "#!/bin/sh\n" +
		"echo 'BATUTA-PROGRESS 1 START'\n" +
		"while [ ! -e \"$BATUTA_PROGRESS_RELEASE\" ]; do :; done\n" +
		"printf 'BATUTA-PROGRESS 1 ' >&2\n" +
		"printf 'DONE' >&2\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{
		Executable: executable,
		Dir:        directory,
	}
	want := []ProgressEvent{
		{Criterion: 1, State: "START"},
		{Criterion: 1, State: "DONE"},
	}
	var observed []ProgressEvent
	subprocess := NewSubprocess()
	subprocess.Environment = []string{"BATUTA_PROGRESS_RELEASE=" + release}
	subprocess.Progress = func(event ProgressEvent) {
		observed = append(observed, event)
		if event.Criterion == 1 && event.State == "START" {
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Errorf("release helper: %v", err)
			}
		}
	}

	result, err := subprocess.Execute(context.Background(), Adapter{}, invocation, 5*time.Second)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("Execute() result = %#v", result)
	}
	assertProgressEvents(t, observed, want)
	assertProgressEvents(t, result.Progress, want)
	for index := range observed {
		if observed[index] != result.Progress[index] {
			t.Errorf("callback event %d = %#v, result event = %#v", index, observed[index], result.Progress[index])
		}
	}
}

func TestSubprocessSerializesProgressFromBothStreams(t *testing.T) {
	t.Parallel()

	const eventsPerStream = 100
	observed := 0
	subprocess := Subprocess{
		Runner: concurrentProgressRunner{eventsPerStream: eventsPerStream},
		Progress: func(ProgressEvent) {
			observed++
		},
	}

	result, err := subprocess.Execute(context.Background(), Adapter{}, Invocation{Executable: "/progress-helper"}, 0)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := observed, 2*eventsPerStream; got != want {
		t.Fatalf("callback events = %d, want %d", got, want)
	}
	if got, want := len(result.Progress), 2*eventsPerStream; got != want {
		t.Fatalf("result progress events = %d, want %d", got, want)
	}
}

type concurrentProgressRunner struct {
	eventsPerStream int
}

func (r concurrentProgressRunner) Run(_ context.Context, command publication.Command) (publication.CommandResult, error) {
	var wait sync.WaitGroup
	for _, observer := range []interface{ Write([]byte) (int, error) }{command.Observer, command.StderrObserver} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for criterion := 1; criterion <= r.eventsPerStream; criterion++ {
				_, _ = fmt.Fprintf(observer, "BATUTA-PROGRESS %d START\n", criterion)
			}
		}()
	}
	wait.Wait()
	return publication.CommandResult{}, nil
}

func assertProgressEvents(t *testing.T, got, want []ProgressEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("progress events = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index].Criterion != want[index].Criterion || got[index].State != want[index].State || got[index].At.IsZero() {
			t.Errorf("progress event %d = %#v, want criterion %d, state %q, and a timestamp", index, got[index], want[index].Criterion, want[index].State)
		}
	}
}
