package executor

import (
	"context"
	"io"
	"time"
)

// Execution carries one invocation and its sinks. Sinks belong to the caller
// and must not be retained or used after Execute returns. Progress callbacks
// must be serialized within an execution.
type Execution struct {
	Adapter    Adapter
	Request    Request
	Invocation Invocation // the prepared CLI command; other backends use Request
	Timeout    time.Duration
	Progress   func(ProgressEvent)
	Stdout     io.Writer
	Stderr     io.Writer
}

// Backend executes independent invocations, including concurrent calls.
type Backend interface {
	Execute(context.Context, Execution) (Result, error)
}

// CLIBackend preserves the bounded, finite-stdin subprocess execution path.
type CLIBackend struct {
	Subprocess Subprocess
}

func (b CLIBackend) Execute(ctx context.Context, execution Execution) (Result, error) {
	subprocess := b.Subprocess
	subprocess.Progress = execution.Progress
	subprocess.Stdout = execution.Stdout
	subprocess.Stderr = execution.Stderr
	return subprocess.Execute(ctx, execution.Adapter, execution.Invocation, execution.Timeout)
}
