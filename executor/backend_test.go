package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
)

type backendCommandRunner func(context.Context, publication.Command) (publication.CommandResult, error)

func (f backendCommandRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	return f(ctx, command)
}

func executeCLI(ctx context.Context, subprocess Subprocess, adapter Adapter, invocation Invocation, timeout time.Duration, progress func(ProgressEvent), stdout, stderr io.Writer) (Result, error) {
	var backend Backend = CLIBackend{Subprocess: subprocess}
	return backend.Execute(ctx, Execution{
		Adapter: adapter, Invocation: invocation, Timeout: timeout,
		Progress: progress, Stdout: stdout, Stderr: stderr,
	})
}

func TestCLIBackendPreservesOutcomes(t *testing.T) {
	t.Parallel()
	startErr := errors.New("cannot start")
	tests := []struct {
		name        string
		raw         publication.CommandResult
		runErr      error
		finished    bool
		rateLimited bool
		question    string
		wantErr     error
	}{
		{name: "success", finished: true},
		{name: "nonzero exit", raw: publication.CommandResult{ExitCode: 7}, runErr: errors.New("exit 7")},
		{name: "limit", raw: publication.CommandResult{ExitCode: 2, Stderr: []byte("quota exceeded")}, runErr: errors.New("exit 2"), rateLimited: true},
		{name: "question", raw: publication.CommandResult{Stdout: []byte("BATUTA-QUESTION: which greeting?\n")}, finished: true, question: "which greeting?"},
		{name: "truncated stdout", raw: publication.CommandResult{StdoutTruncated: true}, finished: true},
		{name: "truncated stderr", raw: publication.CommandResult{StderrTruncated: true}, finished: true},
		{name: "start error", raw: publication.CommandResult{ExitCode: -1}, runErr: startErr, wantErr: startErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			subprocess := Subprocess{Runner: backendCommandRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
				return test.raw, test.runErr
			})}
			result, err := executeCLI(context.Background(), subprocess, Adapter{LimitRegex: "quota exceeded"}, Invocation{Executable: filepath.Join(t.TempDir(), "worker")}, 0, nil, nil, nil)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, test.wantErr)
			}
			if result.ExitCode != test.raw.ExitCode || result.Finished != test.finished || result.RateLimited != test.rateLimited || result.Question != test.question || result.TimedOut || result.Truncated != (test.raw.StdoutTruncated || test.raw.StderrTruncated) {
				t.Fatalf("Execute() = %#v", result)
			}
			if !bytes.Equal(result.Stdout, test.raw.Stdout) || !bytes.Equal(result.Stderr, test.raw.Stderr) {
				t.Fatalf("output changed: %#v", result)
			}
		})
	}
}

func TestCLIBackendKeepsInvocationSinksSeparate(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	executable := filepath.Join(directory, "worker")
	subprocess := Subprocess{
		Lookup:      func(string) (string, error) { return executable, nil },
		Environment: []string{"GIT_CONFIG_COUNT=0", "BACKEND_TEST=value"},
		Progress:    func(ProgressEvent) { t.Error("shared progress sink used") },
		Stdout:      backendErrorWriter{t}, Stderr: backendErrorWriter{t},
		Runner: backendCommandRunner(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
			if command.Executable != executable || command.Directory != directory || len(command.Args) != 1 || len(command.Stdin) != 0 || command.StdoutLimit != outputLimit || command.StderrLimit != outputLimit {
				t.Errorf("command changed: %#v", command)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Error("missing invocation timeout")
			}
			if !strings.Contains(strings.Join(command.Environment, "\n"), "BACKEND_TEST=value\nGIT_CONFIG_COUNT=1\nGIT_CONFIG_KEY_0=commit.gpgsign\nGIT_CONFIG_VALUE_0=false") {
				t.Errorf("environment changed: %q", command.Environment)
			}
			stdout := []byte("BATUTA-PROGRESS " + command.Args[0] + " START\n")
			stderr := []byte("BATUTA-PROGRESS " + command.Args[0] + " DONE")
			if _, err := command.Observer.Write(stdout); err != nil {
				return publication.CommandResult{ExitCode: -1}, err
			}
			if _, err := command.StderrObserver.Write(stderr); err != nil {
				return publication.CommandResult{ExitCode: -1}, err
			}
			return publication.CommandResult{Stdout: stdout, Stderr: stderr}, nil
		}),
	}
	var backend Backend = CLIBackend{Subprocess: subprocess}
	for criterion := 1; criterion <= 3; criterion++ {
		t.Run(fmt.Sprint(criterion), func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			var events []ProgressEvent
			invocation := Invocation{Executable: "worker", Args: []string{fmt.Sprint(criterion)}, Dir: directory}
			result, err := backend.Execute(context.Background(), Execution{
				Invocation: invocation, Timeout: time.Minute,
				Progress: func(event ProgressEvent) { events = append(events, event) },
				Stdout:   &stdout, Stderr: &stderr,
			})
			if err != nil || !result.Finished {
				t.Fatalf("Execute() = %#v, %v", result, err)
			}
			if !bytes.Equal(stdout.Bytes(), result.Stdout) || !bytes.Equal(stderr.Bytes(), result.Stderr) {
				t.Fatalf("streamed output differs from result: %q, %q", stdout.String(), stderr.String())
			}
			assertProgressEvents(t, events, []ProgressEvent{{Criterion: criterion, State: "START"}, {Criterion: criterion, State: "DONE"}})
			if !reflect.DeepEqual(events, result.Progress) {
				t.Fatalf("callback events = %#v, result events = %#v", events, result.Progress)
			}
		})
	}
}

type backendErrorWriter struct{ t *testing.T }

func (w backendErrorWriter) Write(p []byte) (int, error) {
	w.t.Error("shared output sink used")
	return len(p), nil
}

func TestCLIBackendPreservesCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	subprocess := Subprocess{Runner: backendCommandRunner(func(ctx context.Context, _ publication.Command) (publication.CommandResult, error) {
		cancel()
		return publication.CommandResult{ExitCode: -1, Stdout: []byte("partial work")}, ctx.Err()
	})}
	defer cancel()
	result, err := executeCLI(ctx, subprocess, Adapter{}, Invocation{Executable: filepath.Join(t.TempDir(), "worker")}, 0, nil, nil, nil)
	if !errors.Is(err, context.Canceled) || result.Finished || result.TimedOut || result.ExitCode != -1 || string(result.Stdout) != "partial work" {
		t.Fatalf("Execute() = %#v, %v", result, err)
	}
}
